package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/keyless"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

const fixDir = "../../testdata/keyless"

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tokensIn finds every continuation token in a saved answer, so the fake
// YouTube can hand out the next saved page for it.
func tokensIn(b []byte) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if cc, ok := x["continuationCommand"].(map[string]any); ok {
				if tok, _ := cc["token"].(string); tok != "" {
					out = append(out, tok)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	var v any
	_ = json.Unmarshal(b, &v)
	walk(v)
	return out
}

// youtubeFake plays YouTube from the saved real answers in testdata/keyless.
// hits counts requests so a test can prove a cached read made none.
func youtubeFake(t *testing.T) (*keyless.Client, *atomic.Int64) {
	t.Helper()
	next := map[string]string{}
	for _, l := range []struct{ holder, answer string }{
		{"search_page1.json", "search_page2.json"},
		{"next_watch.json", "comments_page1.json"},
		{"comments_page1.json", "comments_page2.json"},
		{"channel_videos.json", "channel_videos_page2.json"},
	} {
		for _, tok := range tokensIn(fixture(t, l.holder)) {
			next[tok] = l.answer
		}
	}
	hits := &atomic.Int64{}
	send := func(w http.ResponseWriter, name string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, name))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/youtubei/v1/", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		endpoint := strings.TrimPrefix(r.URL.Path, "/youtubei/v1/")
		vid, _ := b["videoId"].(string)
		if tok, ok := b["continuation"].(string); ok {
			if name, found := next[tok]; found {
				send(w, name)
			} else {
				_, _ = w.Write([]byte(`{}`))
			}
			return
		}
		switch endpoint {
		case "search":
			if b["query"] == "boom" {
				http.Error(w, "YouTube says: secret internal words https://www.youtube.com/x", 500)
				return
			}
			send(w, "search_page1.json")
		case "next":
			switch vid {
			case "aaaaaaaaaaa":
				send(w, "next_unavailable.json")
			case "disabled000":
				send(w, "next_disabled.json")
			default:
				send(w, "next_watch.json")
			}
		case "navigation/resolve_url":
			if u, _ := b["url"].(string); strings.Contains(u, "nobody") {
				http.Error(w, `{"error":{"message":"not found"}}`, 404)
				return
			}
			send(w, "resolve_url.json")
		case "browse":
			id, _ := b["browseId"].(string)
			if strings.HasPrefix(id, "VL") {
				if strings.HasPrefix(id, "VLPLmissing") {
					http.Error(w, `{"error":{"message":"invalid"}}`, 400)
					return
				}
				send(w, "playlist.json")
				return
			}
			send(w, "channel_videos.json")
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/complete/search", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(fixture(t, "suggest.txt"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := keyless.New()
	c.Base, c.SuggestBase, c.ChartsBase = srv.URL, srv.URL, srv.URL
	c.YTDLP = "off"
	c.Backoff = time.Millisecond
	c.Now = func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
	return c, hits
}

func testServer(t *testing.T, r Reader, mod func(*Config)) *Server {
	t.Helper()
	cfg := Defaults()
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, r, "test")
	s.logf = func(string, ...any) {}
	return s
}

// get runs a request through the handler and decodes the JSON answer.
func get(t *testing.T, s http.Handler, target string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: answer is not JSON: %q", target, rec.Body.String())
	}
	return rec.Code, out, rec.Body.String()
}

func wantError(t *testing.T, s http.Handler, target, code string, status int) {
	t.Helper()
	got, body, raw := get(t, s, target)
	e, _ := body["error"].(map[string]any)
	if got != status || body["ok"] != false || e["code"] != code || len(e) != 1 {
		t.Errorf("%s: want %d %s only, got %d %s", target, status, code, got, raw)
	}
}

func list(t *testing.T, body map[string]any, key string) []any {
	t.Helper()
	l, ok := body[key].([]any)
	if !ok {
		t.Fatalf("no list %q in %v", key, body)
	}
	return l
}

func TestSearch(t *testing.T) {
	c, _ := youtubeFake(t)
	s := testServer(t, c, nil)
	code, body, _ := get(t, s, "/v1/search?q=go+tutorial&limit=8&type=any")
	if code != 200 || body["ok"] != true || body["cached"] != false || body["truncated"] != false {
		t.Fatalf("%d %v", code, body)
	}
	vs := list(t, body, "videos")
	if len(vs) != 8 {
		t.Fatalf("want 8 videos across the saved pages, got %d", len(vs))
	}
	v := vs[0].(map[string]any)
	if v["id"] == "" || v["title"] == "" {
		t.Errorf("video %v", v)
	}
	// Shorts only, ordered by views, with every filter set: all must validate.
	code, _, raw := get(t, s, "/v1/search?q=go&type=short&order=views&since_days=30&lang=en&region=US")
	if code != 200 {
		t.Errorf("filters: %d %s", code, raw)
	}
}

func TestChannel(t *testing.T) {
	c, _ := youtubeFake(t)
	s := testServer(t, c, nil)
	code, body, raw := get(t, s, "/v1/channel?ref=@RickAstleyYT&limit=8")
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	ch := body["channel"].(map[string]any)
	if ch["id"] != "UCuAXFkgsw1L7xaCfnd5JJOw" || ch["name"] != "Rick Astley" {
		t.Errorf("channel %v", ch)
	}
	if rec := ch["recent"].([]any); len(rec) != 8 {
		t.Errorf("want 8 recent, got %d", len(rec))
	}
	wantError(t, s, "/v1/channel?ref=@nobody", CodeNotFound, 404)
	wantError(t, s, "/v1/channel?ref=not+a+channel!", CodeBadRequest, 400)
}

func TestPlaylist(t *testing.T) {
	c, _ := youtubeFake(t)
	s := testServer(t, c, nil)
	code, body, raw := get(t, s, "/v1/playlist?id=PLRAV69dS1uWQGDQoBYMZWKjzuhCaOnBpa&limit=3")
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	pl := body["playlist"].(map[string]any)
	if pl["id"] != "PLRAV69dS1uWQGDQoBYMZWKjzuhCaOnBpa" || pl["title"] == "" {
		t.Errorf("playlist %v", pl)
	}
	if len(list(t, body, "videos")) != 3 {
		t.Errorf("want 3 videos")
	}
	wantError(t, s, "/v1/playlist?id=PLmissing000000", CodeNotFound, 404)
}

func TestComments(t *testing.T) {
	c, _ := youtubeFake(t)
	s := testServer(t, c, nil)
	code, body, raw := get(t, s, "/v1/comments?video=dQw4w9WgXcQ&limit=5")
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	cs := list(t, body, "comments")
	if len(cs) != 5 || cs[0].(map[string]any)["text"] == "" {
		t.Errorf("comments %v", cs)
	}
	// A watch URL is accepted for the video.
	if code, _, _ := get(t, s, "/v1/comments?video=https://www.youtube.com/watch?v%3DdQw4w9WgXcQ&order=new&limit=2"); code != 200 {
		t.Errorf("url form: %d", code)
	}
	wantError(t, s, "/v1/comments?video=disabled000", CodeCommentsOff, 422)
	wantError(t, s, "/v1/comments?video=aaaaaaaaaaa", CodeNotFound, 404)
}

func TestSuggest(t *testing.T) {
	c, _ := youtubeFake(t)
	s := testServer(t, c, nil)
	code, body, raw := get(t, s, "/v1/suggest?q=go+tut&lang=en&region=US&limit=3")
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if n := len(list(t, body, "suggestions")); n < 1 || n > 3 {
		t.Errorf("want 1 to 3 suggestions, got %d", n)
	}
}

func TestHealthAndRouting(t *testing.T) {
	c, hits := youtubeFake(t)
	s := testServer(t, c, nil)
	code, body, _ := get(t, s, "/health")
	if code != 200 || body["ok"] != true || body["version"] != "test" {
		t.Errorf("health: %d %v", code, body)
	}
	// Serve mode never offers the player-based reads.
	for _, p := range []string{"/v1/video?id=dQw4w9WgXcQ", "/v1/transcript?video=dQw4w9WgXcQ", "/v1/player", "/", "/v1/trending"} {
		wantError(t, s, p, CodeNotFound, 404)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/search?q=x", nil))
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), CodeBadRequest) {
		t.Errorf("POST: %d %s", rec.Code, rec.Body.String())
	}
	if hits.Load() != 0 {
		t.Errorf("health and routing must not call YouTube, got %d calls", hits.Load())
	}
}

func TestBadRequests(t *testing.T) {
	c, hits := youtubeFake(t)
	s := testServer(t, c, func(c *Config) { c.MaxQueryLen = 10 })
	for _, p := range []string{
		"/v1/search", "/v1/search?q=", "/v1/search?q=%20%20",
		"/v1/search?q=this+is+far+too+long", "/v1/search?q=a%00b",
		"/v1/search?q=x&limit=0", "/v1/search?q=x&limit=abc", "/v1/search?q=x&limit=-3",
		"/v1/search?q=x&order=popular", "/v1/search?q=x&type=channel", "/v1/search?q=x&since_days=99999",
		"/v1/search?q=x&lang=english-us-very-long", "/v1/search?q=x&region=USA",
		"/v1/channel", "/v1/channel?ref=x&shorts=maybe", "/v1/playlist?id=nope", "/v1/comments?video=short",
		"/v1/comments?video=dQw4w9WgXcQ&order=hot", "/v1/suggest",
	} {
		wantError(t, s, p, CodeBadRequest, 400)
	}
	if hits.Load() != 0 {
		t.Errorf("a bad request must not reach YouTube, got %d calls", hits.Load())
	}
}

func TestErrorsShowNothingFromYouTube(t *testing.T) {
	c, _ := youtubeFake(t)
	s := testServer(t, c, nil)
	_, _, raw := get(t, s, "/v1/search?q=boom")
	if strings.Contains(raw, "secret") || strings.Contains(raw, "youtube") || strings.Contains(raw, "http") || strings.Contains(raw, "HTTP") {
		t.Errorf("answer leaks upstream text: %s", raw)
	}
	wantError(t, s, "/v1/search?q=boom", CodeUnavailable, 503)
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{apperr.New(apperr.NotFound, "x"), CodeNotFound, 404},
		{apperr.New(apperr.Forbidden, "private"), CodeNotFound, 404},
		{apperr.New(apperr.CommentsDisabled, "x"), CodeCommentsOff, 422},
		{apperr.New(apperr.InvalidInput, "x"), CodeBadRequest, 400},
		{apperr.New(apperr.YouTubeError, "layout changed"), CodeUnavailable, 503},
		{apperr.New(apperr.NetworkError, "x"), CodeUnavailable, 503},
		{apperr.New(apperr.QuotaExceeded, "x"), CodeUnavailable, 503},
		{apperr.New(apperr.Internal, "x"), CodeUnavailable, 503},
		{context.DeadlineExceeded, CodeUnavailable, 504},
		{panicError{"x"}, CodeUnavailable, 503},
		{io.EOF, CodeUnavailable, 503},
	} {
		if code, status := classify(tc.err); code != tc.code || status != tc.status {
			t.Errorf("%v: got %s %d, want %s %d", tc.err, code, status, tc.code, tc.status)
		}
	}
}

// fakeReader is a Reader that counts calls, can block and can be told to fail.
type fakeReader struct {
	calls   atomic.Int64
	gate    chan struct{} // when set, Search waits for it to close
	err     error
	panics  bool
	videos  int
	lastOpt yt.SearchOpts
	mu      sync.Mutex
}

func (f *fakeReader) Search(ctx context.Context, o yt.SearchOpts) ([]model.Video, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.lastOpt = o
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	if f.panics {
		panic("parser blew up")
	}
	if f.err != nil {
		return nil, f.err
	}
	n := f.videos
	if n == 0 {
		n = 3
	}
	n = min(n, o.Limit) // the real reader never returns more than the limit
	out := make([]model.Video, n)
	for i := range out {
		out[i] = model.Video{ID: "id", Title: strings.Repeat("t", 100)}
	}
	return out, nil
}

func (f *fakeReader) Channel(context.Context, string, yt.ChannelOpts) (*model.Channel, error) {
	f.calls.Add(1)
	return nil, f.err
}
func (f *fakeReader) Comments(context.Context, string, yt.CommentOpts) ([]model.Comment, error) {
	f.calls.Add(1)
	return nil, f.err
}
func (f *fakeReader) Suggest(context.Context, string, string, string) ([]string, error) {
	f.calls.Add(1)
	return nil, f.err
}
func (f *fakeReader) Playlist(context.Context, string, int) (*model.Playlist, []model.Video, error) {
	f.calls.Add(1)
	return nil, nil, f.err
}

func TestCacheServesRepeatsWithoutYouTube(t *testing.T) {
	c, hits := youtubeFake(t)
	s := testServer(t, c, nil)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	s.cache.now = func() time.Time { return now }

	_, first, _ := get(t, s, "/v1/search?q=go+tutorial")
	after := hits.Load()
	if after == 0 || first["cached"] != false {
		t.Fatalf("first call must fetch: hits=%d %v", after, first["cached"])
	}
	_, second, _ := get(t, s, "/v1/search?q=%20go+tutorial%20")
	if hits.Load() != after || second["cached"] != true {
		t.Errorf("a repeat inside the TTL must not reach YouTube (hits %d -> %d, cached=%v)", after, hits.Load(), second["cached"])
	}
	// A different limit is a different question.
	get(t, s, "/v1/search?q=go+tutorial&limit=5")
	if hits.Load() == after {
		t.Errorf("a different limit must fetch")
	}
	// After the TTL it fetches again.
	n := hits.Load()
	now = now.Add(s.cfg.SearchTTL + time.Second)
	get(t, s, "/v1/search?q=go+tutorial")
	if hits.Load() == n {
		t.Errorf("an expired answer must fetch again")
	}
}

func TestCacheKeepsFailuresBriefly(t *testing.T) {
	f := &fakeReader{err: apperr.New(apperr.YouTubeError, "blocked")}
	s := testServer(t, f, nil)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	s.cache.now = func() time.Time { return now }
	wantError(t, s, "/v1/search?q=x", CodeUnavailable, 503)
	wantError(t, s, "/v1/search?q=x", CodeUnavailable, 503)
	if f.calls.Load() != 1 {
		t.Errorf("a failure must be remembered for ErrorTTL, calls=%d", f.calls.Load())
	}
	now = now.Add(s.cfg.ErrorTTL + time.Second)
	wantError(t, s, "/v1/search?q=x", CodeUnavailable, 503)
	if f.calls.Load() != 2 {
		t.Errorf("after ErrorTTL it asks again, calls=%d", f.calls.Load())
	}
}

func TestCoalescing(t *testing.T) {
	f := &fakeReader{gate: make(chan struct{})}
	s := testServer(t, f, nil)
	const n = 6
	var wg sync.WaitGroup
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, bodies[i] = get(t, s, "/v1/search?q=same")
		}()
	}
	// Wait until the one fetch is running and every caller has joined it.
	deadline := time.Now().Add(5 * time.Second)
	for f.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Fatalf("%d identical requests in flight must share one fetch, got %d", n, f.calls.Load())
	}
	for i, b := range bodies {
		if !strings.Contains(b, `"ok":true`) {
			t.Errorf("caller %d: %s", i, b)
		}
	}
	// A different question is not shared.
	f.gate = nil
	get(t, s, "/v1/search?q=other")
	if f.calls.Load() != 2 {
		t.Errorf("a different query must fetch, calls=%d", f.calls.Load())
	}
}

func TestPanicsAreRecovered(t *testing.T) {
	// A panic inside the fetch.
	f := &fakeReader{panics: true}
	s := testServer(t, f, nil)
	wantError(t, s, "/v1/search?q=x", CodeUnavailable, 503)
	// The server still answers afterwards.
	f.panics = false
	wantError(t, s, "/v1/search?q=y&limit=0", CodeBadRequest, 400)
	if code, _, _ := get(t, s, "/v1/search?q=z"); code != 200 {
		t.Errorf("server must keep working after a panic, got %d", code)
	}
	// A panic in the handler itself.
	s.mux.HandleFunc("/v1/boom", func(http.ResponseWriter, *http.Request) { panic("handler") })
	wantError(t, s, "/v1/boom", CodeUnavailable, 503)
}

func TestLimitsAndTruncation(t *testing.T) {
	f := &fakeReader{videos: 40}
	s := testServer(t, f, func(c *Config) { c.MaxResults = 10; c.DefaultResults = 4; c.MaxBytes = 1024 })
	get(t, s, "/v1/search?q=a&limit=500")
	if f.lastOpt.Limit != 10 {
		t.Errorf("limit above the cap must be cut to it, got %d", f.lastOpt.Limit)
	}
	get(t, s, "/v1/search?q=b")
	if f.lastOpt.Limit != 4 {
		t.Errorf("no limit means the default, got %d", f.lastOpt.Limit)
	}
	// 10 videos of ~150 bytes are over 1 KB: the list is cut and flagged.
	code, body, raw := get(t, s, "/v1/search?q=c&limit=10")
	if code != 200 {
		t.Fatal(code, raw)
	}
	n := len(list(t, body, "videos"))
	if body["truncated"] != true || n == 0 || n >= 10 || len(raw) > 1024 {
		t.Errorf("want a cut answer under the cap, got truncated=%v n=%d bytes=%d", body["truncated"], n, len(raw))
	}
	// An answer that fits is not flagged.
	_, small, _ := get(t, testServer(t, &fakeReader{videos: 2}, nil), "/v1/search?q=d")
	if small["truncated"] != false {
		t.Errorf("small answer flagged truncated")
	}
}

func TestInflightLimit(t *testing.T) {
	f := &fakeReader{gate: make(chan struct{})}
	s := testServer(t, f, func(c *Config) { c.MaxInflight = 1; c.Timeout = 200 * time.Millisecond })
	done := make(chan struct{})
	go func() { get(t, s, "/v1/search?q=first"); close(done) }()
	for f.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	// The only slot is taken, so a different query waits, then times out as unavailable.
	wantError(t, s, "/v1/search?q=second", CodeUnavailable, 504)
	if f.calls.Load() != 1 {
		t.Errorf("the second query must not have reached YouTube")
	}
	close(f.gate)
	<-done
}

func TestCacheCap(t *testing.T) {
	f := &fakeReader{}
	s := testServer(t, f, func(c *Config) { c.CacheEntries = 3 })
	for _, q := range []string{"a", "b", "c", "d", "e"} {
		get(t, s, "/v1/search?q="+q)
	}
	if n := s.cache.size(); n > 3 {
		t.Errorf("cache holds %d, cap is 3", n)
	}
}

func TestConfig(t *testing.T) {
	c := Defaults()
	env := map[string]string{EnvAddr: ":9000", EnvMaxResults: "7", EnvDefaultResults: "3", EnvSearchTTL: "2h", EnvMaxBytes: "5000"}
	if err := c.ApplyEnv(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9000" || c.MaxResults != 7 || c.SearchTTL != 2*time.Hour || c.MaxBytes != 5000 || c.Validate() != nil {
		t.Errorf("env not applied: %+v", c)
	}
	for k, v := range map[string]string{EnvMaxResults: "many", EnvTimeout: "soon"} {
		d := Defaults()
		if err := d.ApplyEnv(func(n string) string {
			if n == k {
				return v
			}
			return ""
		}); err == nil {
			t.Errorf("%s=%s must be refused", k, v)
		}
	}
	bad := Defaults()
	bad.DefaultResults = bad.MaxResults + 1
	if bad.Validate() == nil {
		t.Error("default above max must be refused")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	cfg := Defaults()
	cfg.Addr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, &fakeReader{}, "test") }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("clean stop returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
