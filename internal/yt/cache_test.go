package yt

import (
	"context"
	"testing"

	"github.com/morethancoder/ytx/internal/model"
)

// fakeReader answers Video only; the other methods are never called here.
type fakeReader struct {
	Reader
	calls int
}

func (f *fakeReader) Video(_ context.Context, id string) (*model.Video, error) {
	f.calls++
	return &model.Video{ID: id, Title: "t"}, nil
}

// A cache hit must still say which reader produced the data (finding: notes said
// "source: youtube cache"), and mark it as cached.
func TestCacheHitKeepsRealSource(t *testing.T) {
	t.Setenv("YTX_HOME", t.TempDir())
	for _, src := range []string{SourceKeyless, SourceAPI} {
		fake := &fakeReader{}
		r := &cached{&named{Reader: fake, source: src}}

		ctx, tr := WithTrace(context.Background())
		if _, err := r.Video(ctx, "id-"+src); err != nil {
			t.Fatal(err)
		}
		if got := tr.Source(); got != src || tr.Cached != 0 {
			t.Fatalf("miss: source %q cached %d, want %q 0", got, tr.Cached, src)
		}

		ctx, tr = WithTrace(context.Background())
		if _, err := r.Video(ctx, "id-"+src); err != nil {
			t.Fatal(err)
		}
		if fake.calls != 1 {
			t.Fatalf("reader called %d times, want 1 (second call should hit the cache)", fake.calls)
		}
		if got := tr.Source(); got != src || tr.Cached != 1 {
			t.Fatalf("hit: source %q cached %d, want %q 1", got, tr.Cached, src)
		}
		if tr.QuotaUsed != 0 {
			t.Fatalf("a cache hit spent %d units", tr.QuotaUsed)
		}
	}
}
