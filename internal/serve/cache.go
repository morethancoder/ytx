package serve

import (
	"context"
	"sync"
	"time"
)

// cache keeps answers for a time and makes identical requests that are in
// flight at once share one fetch.
type cache struct {
	mu      sync.Mutex
	max     int
	now     func() time.Time
	items   map[string]entry
	flights map[string]*flight
}

type entry struct {
	val any
	err error
	exp time.Time
}

type flight struct {
	done chan struct{}
	val  any
	err  error
}

func newCache(max int, now func() time.Time) *cache {
	if now == nil {
		now = time.Now
	}
	return &cache{max: max, now: now, items: map[string]entry{}, flights: map[string]*flight{}}
}

// outcome says where an answer came from.
type outcome int

const (
	fetched   outcome = iota // this call ran the fetch
	hit                      // served from memory
	coalesced                // waited for another call's fetch
)

// do returns the cached answer for key, or runs fetch once for every caller
// asking at the same time. The fetch runs in its own goroutine so a caller that
// gives up does not cancel it for the others, and a panic in it becomes an error.
// A success is kept for ttl, a failure for errTTL (0 means not kept).
func (c *cache) do(ctx context.Context, key string, ttl, errTTL time.Duration, fetch func() (any, error)) (any, outcome, error) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok {
		if c.now().Before(e.exp) {
			c.mu.Unlock()
			return e.val, hit, e.err
		}
		delete(c.items, key)
	}
	f, running := c.flights[key]
	if !running {
		f = &flight{done: make(chan struct{})}
		c.flights[key] = f
		go c.run(key, f, ttl, errTTL, fetch)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, fetched, ctx.Err()
	}
	if running {
		return f.val, coalesced, f.err
	}
	return f.val, fetched, f.err
}

func (c *cache) run(key string, f *flight, ttl, errTTL time.Duration, fetch func() (any, error)) {
	defer func() {
		if r := recover(); r != nil {
			f.val, f.err = nil, panicError{r}
		}
		c.mu.Lock()
		delete(c.flights, key)
		keep := ttl
		if f.err != nil {
			keep = errTTL
		}
		if keep > 0 {
			c.store(key, entry{val: f.val, err: f.err, exp: c.now().Add(keep)})
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = fetch()
}

// store adds an entry and keeps the map under its cap: expired entries go
// first, then the ones closest to expiring. Callers hold c.mu.
func (c *cache) store(key string, e entry) {
	c.items[key] = e
	if len(c.items) <= c.max {
		return
	}
	now := c.now()
	for k, v := range c.items {
		if !now.Before(v.exp) {
			delete(c.items, k)
		}
	}
	for len(c.items) > c.max {
		var oldK string
		var oldE time.Time
		for k, v := range c.items {
			if oldK == "" || v.exp.Before(oldE) {
				oldK, oldE = k, v.exp
			}
		}
		delete(c.items, oldK)
	}
}

// size is the number of kept answers (for tests and /health).
func (c *cache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// panicError marks a recovered panic; it maps to "unavailable".
type panicError struct{ v any }

func (p panicError) Error() string { return "panic in fetch" }
