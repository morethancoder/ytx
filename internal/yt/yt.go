// Package yt is the one way ytx reads YouTube. Reader has two implementations:
// keyless (InnerTube, the endpoints youtube.com itself calls) and api (the
// official Data API v3, used when a key is set). Get picks one for a call,
// falls back to keyless when the API can't answer (quota used up, an endpoint
// the API lacks), caches results, and records which source answered in a Trace
// so every result can say where its data came from.
package yt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/cache"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/quota"
)

// Source names, as they appear in meta.source.
const (
	SourceKeyless = "keyless"
	SourceAPI     = "api"
	SourceCache   = "cache"
)

// SearchOpts are the search filters. Zero values mean "YouTube's default".
type SearchOpts struct {
	Query     string
	Limit     int
	Type      string // "video" (long and short), "short", "any" (also channels' videos only; never channels or playlists)
	Order     string // "relevance", "views", "date"
	SinceDays int
	Lang      string
	Region    string
}

// ChannelOpts says how many recent uploads to list.
type ChannelOpts struct {
	Limit  int
	Shorts bool // list Shorts instead of long videos
}

// CommentOpts filters comments.
type CommentOpts struct {
	Limit int
	Order string // "top" or "new"
}

// Reader reads public YouTube data. Every method returns apperr codes:
// not_found, comments_disabled, no_transcript, quota_exceeded,
// not_implemented (this source can't do it), youtube_error, network_error.
type Reader interface {
	Search(ctx context.Context, o SearchOpts) ([]model.Video, error)
	// Video is one video's full details (description, tags, likes, exact date).
	Video(ctx context.Context, id string) (*model.Video, error)
	// Channel takes an id (UC...), @handle or URL and fills Recent (not MedianViews).
	Channel(ctx context.Context, ref string, o ChannelOpts) (*model.Channel, error)
	Comments(ctx context.Context, videoID string, o CommentOpts) ([]model.Comment, error)
	// Transcript returns lang's track, else the original-language track ("" = original).
	Transcript(ctx context.Context, videoID, lang string) (*model.Transcript, error)
	Suggest(ctx context.Context, query, lang, region string) ([]string, error)
	Trending(ctx context.Context, region, category string, limit int) ([]model.Video, error)
	Playlist(ctx context.Context, id string, limit int) (*model.Playlist, []model.Video, error)
}

// Quota costs of each Reader method on the official API.
var APICost = map[string]int{
	"Search": 1, "Video": 1, "Channel": 3, "Comments": 1, "Transcript": 0,
	"Suggest": 0, "Trending": 1, "Playlist": 3,
}

// ---- registration (implementations attach themselves in init)

var (
	newKeyless func() Reader
	newAPI     func(key string, q *quota.Tracker) Reader
)

// RegisterKeyless sets the keyless implementation.
func RegisterKeyless(f func() Reader) { newKeyless = f }

// RegisterAPI sets the official API implementation. It must spend units with
// q.Spend before each call and call q.Exhaust when Google says quotaExceeded.
func RegisterAPI(f func(key string, q *quota.Tracker) Reader) { newAPI = f }

// ---- trace: which source answered

// Trace collects what happened during one command.
// Its methods are safe for concurrent use: research reads in parallel under one trace.
// Read the fields directly only after the command's work has finished.
type Trace struct {
	mu        sync.Mutex
	Sources   map[string]int // source → calls
	Reason    string         // why the API was not used, when it wasn't
	QuotaUsed int
	Cached    int
}

type traceKey struct{}

// WithTrace returns a context that records sources.
func WithTrace(ctx context.Context) (context.Context, *Trace) {
	t := &Trace{Sources: map[string]int{}}
	return context.WithValue(ctx, traceKey{}, t), t
}

// TraceFrom returns the trace in ctx, or nil when there is none.
func TraceFrom(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	return t
}

func note(ctx context.Context, source string, units int) {
	if o, ok := ctx.Value(originKey{}).(*origin); ok && source != SourceCache {
		o.set(source)
	}
	if t, ok := ctx.Value(traceKey{}).(*Trace); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.Sources[source]++
		t.QuotaUsed += units
		if source == SourceCache {
			t.Cached++
		}
	}
}

func reason(ctx context.Context, r string) {
	if t, ok := ctx.Value(traceKey{}).(*Trace); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.Reason == "" {
			t.Reason = r
		}
	}
}

// Source is the main source of a trace: the one used most, ignoring cache unless everything was cached.
func (t *Trace) Source() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	best, n := "", 0
	for s, c := range t.Sources {
		if s != SourceCache && c > n {
			best, n = s, c
		}
	}
	if best == "" && t.Cached > 0 {
		return SourceCache
	}
	if len(t.Sources) > 2 || (len(t.Sources) == 2 && t.Sources[SourceCache] == 0) {
		return "mixed"
	}
	return best
}

// ---- picking a reader

// Mode is what the caller asked for with --source.
type Mode string

const (
	Auto    Mode = "auto"
	Keyless Mode = "keyless"
	API     Mode = "api"
)

// Options for Get.
type Options struct {
	Mode    Mode
	NoCache bool
}

// Get returns the reader for one command.
func Get(o Options) (Reader, error) {
	if newKeyless == nil {
		return nil, apperr.New(apperr.Internal, "no keyless reader is built in")
	}
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	key, _ := config.APIKey()
	var r Reader
	switch o.Mode {
	case Keyless:
		r = &named{Reader: newKeyless(), source: SourceKeyless}
	case API:
		if key == "" {
			return nil, apperr.New(apperr.NeedsAPIKey, "--source api needs YOUTUBE_API_KEY or `ytx key set`")
		}
		if newAPI == nil {
			return nil, apperr.New(apperr.NotImplemented, "the official API reader is not built yet")
		}
		r = &named{Reader: newAPI(key, quota.New(cfg.Limit())), source: SourceAPI}
	case Auto, "":
		kl := &named{Reader: newKeyless(), source: SourceKeyless}
		switch {
		case key == "":
			kl.why = "no API key"
			r = kl
		case newAPI == nil:
			kl.why = "the official API reader is not built yet"
			r = kl
		default:
			r = &fallback{api: &named{Reader: newAPI(key, quota.New(cfg.Limit())), source: SourceAPI}, keyless: kl}
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "--source must be auto, keyless or api")
	}
	if !o.NoCache {
		r = &cached{r}
	}
	return r, nil
}

// fallbackable errors send the call to keyless.
func fallbackable(err error) (string, bool) {
	var e *apperr.Error
	if !errors.As(err, &e) {
		return "", false
	}
	switch e.Code {
	case apperr.QuotaExceeded:
		return "API quota used up", true
	case apperr.NotImplemented:
		return "the API has no endpoint for this", true
	}
	return "", false
}

// call runs f on the API and, when it can't answer, on keyless.
func call[T any](ctx context.Context, fb *fallback, f func(Reader) (T, error)) (T, error) {
	v, err := f(fb.api)
	if r, ok := fallbackable(err); ok {
		reason(ctx, r)
		return f(fb.keyless)
	}
	return v, err
}

// ---- cache wrapper

// TTL per Reader method.
var TTL = map[string]time.Duration{
	"Search": 24 * time.Hour, "Video": time.Hour, "Channel": time.Hour, "Comments": 6 * time.Hour,
	"Transcript": 30 * 24 * time.Hour, "Suggest": 24 * time.Hour, "Trending": time.Hour, "Playlist": time.Hour,
}

// cachedCall answers from the disk cache when it can. A miss runs f with a context
// that records which reader answered, and the cache keeps that source, so a later hit
// still reports "keyless" or "api" (with meta.cached) instead of a bare "cache".
func cachedCall[T any](ctx context.Context, method string, args []any, f func(context.Context) (T, error)) (T, error) {
	key := cache.Key(append([]string{method}, fmt.Sprint(args...))...)
	var v T
	if src, ok := cache.Get(key, TTL[method], &v); ok {
		noteCached(ctx, src)
		return v, nil
	}
	rec := &origin{}
	v, err := f(context.WithValue(ctx, originKey{}, rec))
	if err == nil {
		cache.Put(key, rec.get(), v)
	}
	return v, err
}

// origin records the reader that answered one cached call.
type origin struct {
	mu  sync.Mutex
	src string
}

type originKey struct{}

func (o *origin) set(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.src == "" || o.src == s {
		o.src = s
	} else {
		o.src = "mixed"
	}
}

func (o *origin) get() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.src
}

// noteCached records a cache hit. Entries written before the source was stored
// (or by an unknown reader) count as plain "cache".
func noteCached(ctx context.Context, src string) {
	switch src {
	case SourceKeyless, SourceAPI, "mixed":
	default:
		src = SourceCache
	}
	if t, ok := ctx.Value(traceKey{}).(*Trace); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.Sources[src]++
		t.Cached++
	}
}

// named records its source (and, for keyless, why the API wasn't used) in the trace.
type named struct {
	Reader
	source string
	why    string
}

func (n *named) done(ctx context.Context, method string, err error) {
	if err != nil {
		return
	}
	units := 0
	if n.source == SourceAPI {
		units = APICost[method]
	}
	if n.why != "" {
		reason(ctx, n.why)
	}
	note(ctx, n.source, units)
}

// fallback tries the official API first and keyless when the API can't answer.
type fallback struct {
	api, keyless Reader
}

// cached serves fresh-enough results from disk.
type cached struct{ Reader }
