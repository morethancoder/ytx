package yt

import (
	"context"
	"sync"
	"testing"
)

// research reads in parallel under one trace; this crashed with "concurrent map writes" before Trace had a lock.
func TestTraceConcurrentNotes(t *testing.T) {
	ctx, tr := WithTrace(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := SourceKeyless
			if i%5 == 0 {
				src = SourceCache
			}
			note(ctx, src, 1)
			reason(ctx, "no API key")
			_ = tr.Source()
		}(i)
	}
	wg.Wait()
	if tr.QuotaUsed != 50 || tr.Sources[SourceKeyless] != 40 || tr.Cached != 10 {
		t.Fatalf("lost updates: %+v", tr.Sources)
	}
	if got := tr.Source(); got != SourceKeyless {
		t.Fatalf("source %q, want keyless", got)
	}
}
