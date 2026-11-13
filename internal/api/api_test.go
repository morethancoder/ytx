package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/quota"
	"github.com/morethancoder/ytx/internal/yt"
)

const testKey = "SECRET-TEST-KEY"

var clock = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

type call struct {
	path  string
	query url.Values
	key   string
}

type fake struct {
	mu    sync.Mutex
	calls []call
	// route maps an endpoint to a function returning status and fixture file.
	route map[string]func(q url.Values) (int, string)
}

func (f *fake) paths() []string {
	var p []string
	for _, c := range f.calls {
		p = append(p, c.path)
	}
	return p
}

func newClient(t *testing.T, limit int, route map[string]func(url.Values) (int, string)) (*Client, *fake, *quota.Tracker) {
	t.Helper()
	f := &fake{route: route}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, call{strings.TrimPrefix(r.URL.Path, "/"), r.URL.Query(), r.Header.Get("X-Goog-Api-Key")})
		f.mu.Unlock()
		fn := f.route[strings.TrimPrefix(r.URL.Path, "/")]
		if fn == nil {
			http.Error(w, "no route", 500)
			return
		}
		status, file := fn(r.URL.Query())
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "api", file))
		if err != nil {
			t.Errorf("fixture: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	q := quota.NewAt(filepath.Join(t.TempDir(), "quota.json"), limit, func() time.Time { return clock })
	c := New(testKey, q)
	c.base = srv.URL
	c.now = func() time.Time { return clock }
	return c, f, q
}

func ok(file string) func(url.Values) (int, string) {
	return func(url.Values) (int, string) { return 200, file }
}

func code(t *testing.T, err error) string {
	t.Helper()
	var e *apperr.Error
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errorsAs(err, &e) {
		t.Fatalf("not an apperr: %v", err)
	}
	return e.Code
}

func errorsAs(err error, e **apperr.Error) bool {
	x, ok := err.(*apperr.Error)
	if ok {
		*e = x
	}
	return ok
}

func TestSearchMappingPagingAndQuota(t *testing.T) {
	c, f, q := newClient(t, 10000, map[string]func(url.Values) (int, string){
		"search": func(v url.Values) (int, string) {
			if v.Get("pageToken") == "PAGE2" {
				return 200, "search_page2.json"
			}
			return 200, "search_page1.json"
		},
		"videos": ok("videos.json"),
	})
	vs, err := c.Search(context.Background(), yt.SearchOpts{Query: "go", Limit: 3, Order: "views", SinceDays: 7, Lang: "en", Region: "us"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 || vs[0].ID != "vidLong00001" || vs[1].ID != "vidShort0002" || vs[2].ID != "vidTagged003" {
		t.Fatalf("order/len wrong: %+v", vs)
	}
	l := vs[0]
	if l.Views != 123456 || l.Likes != 700 || l.Comments != 40 || l.DurationS != 3723 || l.Thumbnail != "https://i/m.jpg" ||
		l.IsShort || l.ChannelName != "Chan" || l.AgeDays != 10 || l.URL != "https://www.youtube.com/watch?v=vidLong00001" {
		t.Errorf("long mapping wrong: %+v", l)
	}
	if !vs[1].IsShort || !vs[2].IsShort {
		t.Errorf("short detection wrong: 45s=%v tagged 150s=%v", vs[1].IsShort, vs[2].IsShort)
	}
	first := f.calls[0].query
	for k, want := range map[string]string{"order": "viewCount", "relevanceLanguage": "en", "regionCode": "US", "type": "video", "q": "go", "publishedAfter": "2026-11-05T12:00:00Z"} {
		if first.Get(k) != want {
			t.Errorf("search param %s = %q, want %q", k, first.Get(k), want)
		}
	}
	if got := strings.Join(f.paths(), ","); got != "search,videos,search,videos" {
		t.Errorf("call order %s", got)
	}
	if f.calls[2].query.Get("pageToken") != "PAGE2" {
		t.Error("second page didn't send the page token")
	}
	for _, cl := range f.calls {
		if cl.key != testKey || cl.query.Get("key") != "" {
			t.Errorf("key must travel only in the header: %+v", cl)
		}
	}
	if q.Used() != 2 {
		t.Errorf("pool spent %d, want 2 (videos.list x2)", q.Used())
	}
	if b := q.Bucket(quota.BucketSearch, quota.BucketLimit); b.Used() != 2 {
		t.Errorf("search bucket spent %d, want 2", b.Used())
	}
}

func TestSearchOrdersAndShortType(t *testing.T) {
	c, f, _ := newClient(t, 10000, map[string]func(url.Values) (int, string){
		"search": ok("search_page1.json"), "videos": ok("videos.json"),
	})
	vs, err := c.Search(context.Background(), yt.SearchOpts{Query: "x", Limit: 2, Type: "short", Order: "date"})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].query.Get("videoDuration") != "short" || f.calls[0].query.Get("order") != "date" {
		t.Errorf("params: %v", f.calls[0].query)
	}
	// Page one holds one long video and one Short; the long one is filtered out.
	if len(vs) != 1 || vs[0].ID != "vidShort0002" {
		t.Errorf("short filter: %+v", vs)
	}
	if _, err := c.Search(context.Background(), yt.SearchOpts{Query: "x", Order: "bogus"}); code(t, err) != apperr.InvalidInput {
		t.Error("bad order should be invalid_input")
	}
}

func TestVideoAndNotFound(t *testing.T) {
	c, f, q := newClient(t, 100, map[string]func(url.Values) (int, string){"videos": func(v url.Values) (int, string) {
		if v.Get("id") == "missing00000" {
			return 200, "empty.json"
		}
		return 200, "videos.json"
	}})
	v, err := c.Video(context.Background(), "vidShort0002")
	if err != nil || v.ID != "vidShort0002" || v.Views != 9000 {
		t.Fatalf("video: %+v %v", v, err)
	}
	if !strings.Contains(f.calls[0].query.Get("part"), "statistics") {
		t.Error("video needs statistics part")
	}
	if _, err := c.Video(context.Background(), "missing00000"); code(t, err) != apperr.NotFound {
		t.Error("empty items should be not_found")
	}
	if q.Used() != 2 {
		t.Errorf("spent %d, want 2", q.Used())
	}
}

func TestChannelByHandleDropsShorts(t *testing.T) {
	c, f, q := newClient(t, 100, map[string]func(url.Values) (int, string){
		"channels": ok("channels.json"), "playlistItems": ok("playlistitems.json"), "videos": ok("videos.json"),
	})
	ch, err := c.Channel(context.Background(), "@chan", yt.ChannelOpts{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].query.Get("forHandle") != "@chan" || f.calls[0].query.Get("id") != "" {
		t.Errorf("handle lookup: %v", f.calls[0].query)
	}
	if f.calls[1].query.Get("playlistId") != "UUaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("uploads playlist: %v", f.calls[1].query)
	}
	if ch.Name != "Chan" || ch.Handle != "@chan" || ch.Subscribers != 5000 || ch.VideoCount != 88 || ch.ViewCount != 1000000 {
		t.Errorf("channel: %+v", ch)
	}
	if len(ch.Recent) != 1 || ch.Recent[0].ID != "vidLong00001" {
		t.Errorf("recent should hold only the long video: %+v", ch.Recent)
	}
	if q.Used() != 3 {
		t.Errorf("spent %d, want 3", q.Used())
	}
	if _, err := c.Channel(context.Background(), "UCaaaaaaaaaaaaaaaaaaaaaa", yt.ChannelOpts{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if f.calls[3].query.Get("id") != "UCaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("id lookup: %v", f.calls[3].query)
	}
}

func TestChannelShortsAndUnsupportedAreNotImplemented(t *testing.T) {
	c, f, q := newClient(t, 100, nil)
	ctx := context.Background()
	if _, err := c.Channel(ctx, "@chan", yt.ChannelOpts{Shorts: true}); code(t, err) != apperr.NotImplemented {
		t.Error("channel shorts")
	}
	if _, err := c.Transcript(ctx, "x", ""); code(t, err) != apperr.NotImplemented {
		t.Error("transcript")
	}
	if _, err := c.Suggest(ctx, "x", "", ""); code(t, err) != apperr.NotImplemented {
		t.Error("suggest")
	}
	if len(f.calls) != 0 || q.Used() != 0 {
		t.Error("unsupported methods must not call or spend")
	}
}

func TestChannelNotFound(t *testing.T) {
	c, _, _ := newClient(t, 100, map[string]func(url.Values) (int, string){"channels": ok("empty.json")})
	if _, err := c.Channel(context.Background(), "@nobody", yt.ChannelOpts{}); code(t, err) != apperr.NotFound {
		t.Error("want not_found")
	}
}

func TestComments(t *testing.T) {
	c, f, q := newClient(t, 100, map[string]func(url.Values) (int, string){"commentThreads": ok("commentthreads.json")})
	cs, err := c.Comments(context.Background(), "vidLong00001", yt.CommentOpts{Limit: 10, Order: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].query.Get("order") != "time" || f.calls[0].query.Get("videoId") != "vidLong00001" {
		t.Errorf("params: %v", f.calls[0].query)
	}
	if len(cs) != 2 || cs[0].Author != "Ann" || cs[0].Likes != 5 || cs[0].Replies != 2 || cs[0].ID != "thr1" || cs[0].Published.IsZero() {
		t.Errorf("comments: %+v", cs)
	}
	if _, err := c.Comments(context.Background(), "v", yt.CommentOpts{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if f.calls[1].query.Get("order") != "relevance" || len(f.calls) != 2 {
		t.Errorf("top order: %v", f.calls[1].query)
	}
	if q.Used() != 2 {
		t.Errorf("spent %d", q.Used())
	}
}

func TestCommentsDisabled(t *testing.T) {
	c, _, _ := newClient(t, 100, map[string]func(url.Values) (int, string){"commentThreads": func(url.Values) (int, string) { return 403, "err_comments.json" }})
	_, err := c.Comments(context.Background(), "v", yt.CommentOpts{})
	if code(t, err) != apperr.CommentsDisabled {
		t.Errorf("got %v", err)
	}
}

func TestTrending(t *testing.T) {
	c, f, q := newClient(t, 100, map[string]func(url.Values) (int, string){"videos": ok("videos.json")})
	vs, err := c.Trending(context.Background(), "de", "music", 2)
	if err != nil {
		t.Fatal(err)
	}
	p := f.calls[0].query
	if p.Get("chart") != "mostPopular" || p.Get("regionCode") != "DE" || p.Get("videoCategoryId") != "10" || p.Get("maxResults") != "2" {
		t.Errorf("params: %v", p)
	}
	if len(vs) != 2 || q.Used() != 1 {
		t.Errorf("len %d spent %d", len(vs), q.Used())
	}
	for name, id := range map[string]string{"gaming": "20", "movies": "30", "film": "1", "42": "42"} {
		if _, err := c.Trending(context.Background(), "", name, 1); err != nil {
			t.Fatal(err)
		}
		if got := f.calls[len(f.calls)-1].query.Get("videoCategoryId"); got != id {
			t.Errorf("%s -> %s, want %s", name, got, id)
		}
	}
	if _, err := c.Trending(context.Background(), "", "nonsense", 1); code(t, err) != apperr.InvalidInput {
		t.Error("unknown category")
	}
}

func TestPlaylist(t *testing.T) {
	c, f, q := newClient(t, 100, map[string]func(url.Values) (int, string){
		"playlists": ok("playlists.json"), "playlistItems": ok("playlistitems.json"), "videos": ok("videos.json"),
	})
	pl, vs, err := c.Playlist(context.Background(), "PLaaaaaaaaaaaaaaaa", 10)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Title != "My list" || pl.Privacy != "public" || pl.ItemCount != 2 || len(vs) != 2 {
		t.Errorf("playlist: %+v %d", pl, len(vs))
	}
	if got := strings.Join(f.paths(), ","); got != "playlists,playlistItems,videos" || q.Used() != 3 {
		t.Errorf("calls %s spent %d", got, q.Used())
	}
}

func TestPlaylistNotFound(t *testing.T) {
	c, _, _ := newClient(t, 100, map[string]func(url.Values) (int, string){
		"playlists": ok("playlists.json"), "playlistItems": func(url.Values) (int, string) { return 404, "err_notfound.json" },
	})
	if _, _, err := c.Playlist(context.Background(), "PLx", 5); code(t, err) != apperr.NotFound {
		t.Errorf("got %v", err)
	}
}

func TestQuotaExceededFromGoogleExhaustsTracker(t *testing.T) {
	c, f, q := newClient(t, 10000, map[string]func(url.Values) (int, string){"videos": func(url.Values) (int, string) { return 403, "err_quota.json" }})
	_, err := c.Video(context.Background(), "vidLong00001")
	if code(t, err) != apperr.QuotaExceeded {
		t.Fatalf("got %v", err)
	}
	if q.Left() != 0 {
		t.Errorf("tracker should be exhausted, %d left", q.Left())
	}
	// The next call is refused locally, without touching the network.
	n := len(f.calls)
	if _, err := c.Video(context.Background(), "vidLong00001"); code(t, err) != apperr.QuotaExceeded || len(f.calls) != n {
		t.Error("exhausted tracker should refuse before the HTTP call")
	}
}

func TestSearchBucketFullStillAllowsVideo(t *testing.T) {
	c, f, q := newClient(t, 10000, map[string]func(url.Values) (int, string){"search": ok("search_page1.json"), "videos": ok("videos.json")})
	b := q.Bucket(quota.BucketSearch, quota.BucketLimit)
	if err := b.Spend(quota.BucketLimit); err != nil {
		t.Fatal(err)
	}
	_, err := c.Search(context.Background(), yt.SearchOpts{Query: "x"})
	if code(t, err) != apperr.QuotaExceeded || len(f.calls) != 0 || q.Used() != 0 {
		t.Errorf("err %v calls %d pool %d", err, len(f.calls), q.Used())
	}
	if _, err := c.Video(context.Background(), "vidLong00001"); err != nil {
		t.Errorf("video should still work: %v", err)
	}
}

func TestSearchQuotaExceededExhaustsOnlyBucket(t *testing.T) {
	c, _, q := newClient(t, 10000, map[string]func(url.Values) (int, string){"search": func(url.Values) (int, string) { return 403, "err_quota.json" }})
	_, err := c.Search(context.Background(), yt.SearchOpts{Query: "x"})
	if code(t, err) != apperr.QuotaExceeded || !strings.Contains(err.Error(), "search") {
		t.Fatalf("got %v", err)
	}
	if q.Bucket(quota.BucketSearch, quota.BucketLimit).Left() != 0 || q.Left() != 10000 {
		t.Error("only the search bucket should be exhausted")
	}
}

func TestLocalQuotaRefusalSpendsNothing(t *testing.T) {
	c, f, q := newClient(t, 0, map[string]func(url.Values) (int, string){"search": ok("search_page1.json"), "videos": ok("videos.json")})
	_, err := c.Video(context.Background(), "vidLong00001")
	if code(t, err) != apperr.QuotaExceeded || len(f.calls) != 0 || q.Used() != 0 {
		t.Errorf("err %v calls %d used %d", err, len(f.calls), q.Used())
	}
}

func TestBadKeyIsForbiddenAndNeverLeaksKey(t *testing.T) {
	c, _, _ := newClient(t, 100, map[string]func(url.Values) (int, string){"videos": func(url.Values) (int, string) { return 400, "err_key.json" }})
	_, err := c.Video(context.Background(), "vidLong00001")
	if code(t, err) != apperr.Forbidden || !strings.Contains(err.Error(), "YOUTUBE_API_KEY") {
		t.Errorf("got %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("error leaks the key")
	}
}

func TestNetworkErrorDoesNotLeakKey(t *testing.T) {
	c, _, _ := newClient(t, 100, nil)
	c.base = "http://127.0.0.1:1" // nothing listens here
	_, err := c.Video(context.Background(), "vidLong00001")
	if code(t, err) != apperr.NetworkError {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error leaks the key: %v", err)
	}
}

func TestRetriesOnceOnServerError(t *testing.T) {
	n := 0
	c, f, q := newClient(t, 100, map[string]func(url.Values) (int, string){"videos": func(url.Values) (int, string) {
		n++
		if n == 1 {
			return 503, "err_quota.json"
		}
		return 200, "videos.json"
	}})
	if _, err := c.Video(context.Background(), "vidLong00001"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || q.Used() != 1 {
		t.Errorf("calls %d spent %d (a retry must not be charged twice)", len(f.calls), q.Used())
	}
}

func TestParseDuration(t *testing.T) {
	for s, want := range map[string]int{"PT45S": 45, "PT1H2M3S": 3723, "PT10M": 600, "P1DT1H": 90000, "P0D": 0, "": 0, "bad": 0} {
		if got := parseDuration(s); got != want {
			t.Errorf("%q = %d, want %d", s, got, want)
		}
	}
}

// APICost is what the status and doctor numbers say a default call costs; it must match what is spent.
func TestAPICostMatchesSpending(t *testing.T) {
	routes := map[string]func(url.Values) (int, string){
		"search": ok("search_page1.json"), "videos": ok("videos.json"), "channels": ok("channels.json"),
		"playlistItems": ok("playlistitems.json"), "playlists": ok("playlists.json"), "commentThreads": ok("commentthreads.json"),
	}
	ctx := context.Background()
	cases := map[string]func(c *Client) error{
		"Search":     func(c *Client) error { _, err := c.Search(ctx, yt.SearchOpts{Query: "x", Limit: 2}); return err },
		"Video":      func(c *Client) error { _, err := c.Video(ctx, "vidLong00001"); return err },
		"Channel":    func(c *Client) error { _, err := c.Channel(ctx, "@chan", yt.ChannelOpts{}); return err },
		"Comments":   func(c *Client) error { _, err := c.Comments(ctx, "v", yt.CommentOpts{}); return err },
		"Transcript": func(c *Client) error { _, err := c.Transcript(ctx, "v", ""); return nilIfNotImpl(err) },
		"Suggest":    func(c *Client) error { _, err := c.Suggest(ctx, "v", "", ""); return nilIfNotImpl(err) },
		"Trending":   func(c *Client) error { _, err := c.Trending(ctx, "us", "", 0); return err },
		"Playlist":   func(c *Client) error { _, _, err := c.Playlist(ctx, "PLaaaaaaaaaaaaaaaa", 0); return err },
	}
	for name, run := range cases {
		c, _, q := newClient(t, 10000, routes)
		if err := run(c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if q.Used() != yt.APICost[name] {
			t.Errorf("%s spends %d but yt.APICost says %d", name, q.Used(), yt.APICost[name])
		}
	}
}

// nilIfNotImpl accepts the not_implemented error the two keyless-only methods return.
func nilIfNotImpl(err error) error {
	if e, ok := err.(*apperr.Error); ok && e.Code == apperr.NotImplemented {
		return nil
	}
	return err
}
