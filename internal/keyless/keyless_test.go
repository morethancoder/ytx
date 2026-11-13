package keyless

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
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

// links maps the fixture that holds a continuation token to the answer for it.
var links = []struct{ holder, answer string }{
	{"search_page1.json", "search_page2.json"},
	{"next_watch.json", "comments_page1.json"},
	{"comments_page1.json", "comments_page2.json"},
	{"channel_videos.json", "channel_videos_page2.json"},
}

// newServer plays YouTube from the recorded fixtures.
func newServer(t *testing.T) *Client {
	t.Helper()
	tokenOf := map[string]string{}
	for _, l := range links {
		for _, tok := range tokensIn(fixture(t, l.holder)) {
			tokenOf[tok] = l.answer
		}
	}
	send := func(w http.ResponseWriter, name string) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture(t, name))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/youtubei/v1/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b obj
		_ = json.Unmarshal(raw, &b)
		endpoint := strings.TrimPrefix(r.URL.Path, "/youtubei/v1/")
		client := str(get(b, "context", "client", "clientName"))
		vid, _ := b["videoId"].(string)
		if tok, ok := b["continuation"].(string); ok {
			name, found := tokenOf[tok]
			if !found {
				w.Write([]byte(`{}`)) // the end of the list: nothing more to load
				return
			}
			send(w, name)
			return
		}
		switch endpoint {
		case "search":
			send(w, "search_page1.json")
		case "player":
			switch {
			case vid == "aaaaaaaaaaa" && client == "WEB":
				send(w, "player_error.json")
			case vid == "aaaaaaaaaaa":
				send(w, "player_error.json")
			case vid == "dubbed00000" && client != "WEB":
				send(w, "player_dubbed.json")
			case client == "WEB":
				send(w, "player_web.json")
			default:
				send(w, "player_android.json")
			}
		case "next":
			switch vid {
			case "aaaaaaaaaaa":
				send(w, "next_unavailable.json")
			case "disabled0000":
				send(w, "next_disabled.json")
			default:
				send(w, "next_watch.json")
			}
		case "navigation/resolve_url":
			if strings.Contains(str(b["url"]), "nobody") {
				http.Error(w, `{"error":{"message":"not found"}}`, 404)
				return
			}
			send(w, "resolve_url.json")
		case "browse":
			id, _ := b["browseId"].(string)
			switch {
			case client == "WEB_MUSIC_ANALYTICS":
				send(w, "charts.json")
			case strings.HasPrefix(id, "VL"):
				if id == "VLPLmissing" {
					http.Error(w, `{"error":{"message":"invalid"}}`, 400)
					return
				}
				send(w, "playlist.json")
			case b["params"] == tabShorts:
				send(w, "channel_shorts.json")
			default:
				send(w, "channel_videos.json")
			}
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/timedtext", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("kind") == "asr" {
			send(w, "timedtext_asr.json")
			return
		}
		send(w, "timedtext_en.json")
	})
	mux.HandleFunc("/complete/search", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "suggest.txt"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New()
	c.Base, c.SuggestBase, c.ChartsBase = srv.URL, srv.URL, srv.URL
	c.YTDLP = "off"
	c.Backoff = time.Millisecond
	c.Now = func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
	return c
}

// tokensIn finds every continuation token in a fixture.
func tokensIn(b []byte) []string {
	var out []string
	var v any
	_ = json.Unmarshal(b, &v)
	for _, n := range findAll(v, "continuationCommand") {
		if tok := str(get(n, "token")); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

func ctx() context.Context { return context.Background() }

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !isAppErr(err, code) {
		t.Fatalf("want error code %s, got %v", code, err)
	}
}

func TestSearch(t *testing.T) {
	c := newServer(t)
	vs, err := c.Search(ctx(), yt.SearchOpts{Query: "go tutorial", Limit: 8, Type: "video", Region: "US"})
	if err != nil {
		t.Fatal(err)
	}
	// Three long videos on page 1 and two new ones on page 2; Shorts are filtered out.
	if len(vs) != 5 {
		t.Fatalf("want 5 long videos across the continuation, got %d", len(vs))
	}
	all, err := c.Search(ctx(), yt.SearchOpts{Query: "go tutorial", Limit: 20, Type: "any"})
	if err != nil || len(all) != 8 {
		t.Fatalf("type any: want 8 unique videos, got %d (%v)", len(all), err)
	}
	shorts, err := c.Search(ctx(), yt.SearchOpts{Query: "go tutorial", Limit: 20, Type: "short"})
	if err != nil || len(shorts) != 3 || !shorts[0].IsShort {
		t.Fatalf("type short: %v %+v", err, shorts)
	}
	ids := map[string]bool{}
	for _, v := range vs {
		if ids[v.ID] {
			t.Errorf("duplicate %s", v.ID)
		}
		ids[v.ID] = true
		if v.ID == "" || v.Title == "" || !strings.Contains(v.URL, v.ID) {
			t.Errorf("bad video %+v", v)
		}
	}
	v := vs[0]
	if v.Views <= 0 || v.DurationS <= 0 || v.ChannelName == "" || v.PublishedText == "" || v.Published.IsZero() {
		t.Errorf("first video is missing fields: %+v", v)
	}
	small, err := c.Search(ctx(), yt.SearchOpts{Query: "go tutorial", Limit: 2})
	if err != nil || len(small) != 2 {
		t.Fatalf("limit 2: %v %d", err, len(small))
	}
}

func TestSearchOrderViews(t *testing.T) {
	c := newServer(t)
	vs, err := c.Search(ctx(), yt.SearchOpts{Query: "x", Limit: 5, Order: "date"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(vs); i++ {
		if vs[i].Published.After(vs[i-1].Published) {
			t.Errorf("not newest first at %d", i)
		}
	}
	none, err := c.Search(ctx(), yt.SearchOpts{Query: "x", Limit: 5, SinceDays: 1})
	if err != nil || none == nil {
		t.Fatalf("a search with no hits must be an empty list, got %v %v", none, err)
	}
}

func TestSearchParams(t *testing.T) {
	if a, b := searchParams(2, 2, 1), searchParams(0, 0, 9); a == "" || b == "" || a == b {
		t.Errorf("params: %q %q", a, b)
	}
	if searchParams(0, 0, 0) != "" {
		t.Error("no options means no params")
	}
	if uploadDateFilter(1) != 2 || uploadDateFilter(7) != 3 || uploadDateFilter(0) != 0 {
		t.Error("upload date filter mapping")
	}
}

func TestVideo(t *testing.T) {
	c := newServer(t)
	v, err := c.Video(ctx(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "dQw4w9WgXcQ" || v.Title == "" || v.Views < 1_000_000_000 || v.Likes <= 0 || v.DurationS <= 0 ||
		v.ChannelID == "" || v.ChannelName == "" || v.Description == "" || len(v.Tags) == 0 || v.Thumbnail == "" {
		t.Errorf("video: %+v", v)
	}
	if v.Published.Year() != 2009 {
		t.Errorf("published %v", v.Published)
	}
	if v.Comments <= 0 {
		t.Errorf("comment count %d", v.Comments)
	}
}

func TestVideoNotFound(t *testing.T) {
	_, err := newServer(t).Video(ctx(), "aaaaaaaaaaa")
	wantCode(t, err, apperr.NotFound)
}

func TestChannel(t *testing.T) {
	c := newServer(t)
	ch, err := c.Channel(ctx(), "@RickAstleyYT", yt.ChannelOpts{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "UCuAXFkgsw1L7xaCfnd5JJOw" || ch.Name != "Rick Astley" || ch.Handle != "@RickAstleyYT" ||
		ch.Subscribers <= 0 || ch.Thumbnail == "" {
		t.Errorf("channel: %+v", ch)
	}
	if len(ch.Recent) != 8 {
		t.Fatalf("want 8 recent across the continuation, got %d", len(ch.Recent))
	}
	if r := ch.Recent[0]; r.ID == "" || r.Title == "" || r.DurationS <= 0 {
		t.Errorf("recent[0]: %+v", r)
	}
	shorts, err := c.Channel(ctx(), "UCuAXFkgsw1L7xaCfnd5JJOw", yt.ChannelOpts{Limit: 3, Shorts: true})
	if err != nil || len(shorts.Recent) == 0 || !shorts.Recent[0].IsShort {
		t.Fatalf("shorts: %v %+v", err, shorts)
	}
	_, err = c.Channel(ctx(), "@nobody", yt.ChannelOpts{})
	wantCode(t, err, apperr.NotFound)
}

func TestComments(t *testing.T) {
	c := newServer(t)
	cs, err := c.Comments(ctx(), "dQw4w9WgXcQ", yt.CommentOpts{Limit: 5, Order: "top"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 5 {
		t.Fatalf("want 5 across two pages, got %d", len(cs))
	}
	for _, cm := range cs {
		if cm.ID == "" || cm.Author == "" || cm.Text == "" || cm.When == "" {
			t.Errorf("bad comment %+v", cm)
		}
	}
	if _, err := c.Comments(ctx(), "dQw4w9WgXcQ", yt.CommentOpts{Limit: 2, Order: "new"}); err != nil {
		t.Errorf("newest: %v", err)
	}
}

func TestCommentsDisabledAndMissing(t *testing.T) {
	c := newServer(t)
	_, err := c.Comments(ctx(), "disabled0000", yt.CommentOpts{Limit: 5})
	wantCode(t, err, apperr.CommentsDisabled)
	_, err = c.Comments(ctx(), "aaaaaaaaaaa", yt.CommentOpts{Limit: 5})
	wantCode(t, err, apperr.NotFound)
}

func TestTranscript(t *testing.T) {
	c := newServer(t)
	tr, err := c.Transcript(ctx(), "dQw4w9WgXcQ", "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.VideoID != "dQw4w9WgXcQ" || tr.Language == "" || len(tr.Segments) == 0 || tr.Words == 0 || tr.Text == "" {
		t.Errorf("transcript: %+v", tr)
	}
	if s := tr.Segments[0]; s.Text == "" || s.DurS <= 0 {
		t.Errorf("segment: %+v", s)
	}
	de, err := c.Transcript(ctx(), "dQw4w9WgXcQ", "de")
	if err != nil || de.Language != "de-DE" {
		t.Errorf("de: %v %+v", err, de)
	}
}

func TestTranscriptNotFound(t *testing.T) {
	_, err := newServer(t).Transcript(ctx(), "aaaaaaaaaaa", "")
	wantCode(t, err, apperr.NotFound)
}

func TestParseTracksAndJSON3(t *testing.T) {
	var player obj
	_ = json.Unmarshal(fixture(t, "player_android.json"), &player)
	tracks, original := parseTracks(player)
	if len(tracks) != 4 || original == "" {
		t.Fatalf("want 4 tracks and an original, got %d %q", len(tracks), original)
	}
	if tr := chooseTrack(tracks, "ja", original); tr == nil || tr.Lang != "ja" {
		t.Errorf("ja: %+v", tr)
	}
	if tr := chooseTrack(tracks, "pt", original); tr == nil || tr.Lang != original {
		t.Errorf("unknown language must fall back to the original: %+v", tr)
	}
	tr, err := parseJSON3(fixture(t, "timedtext_asr.json"))
	if err != nil || len(tr.Segments) == 0 {
		t.Fatalf("asr: %v %+v", err, tr)
	}
	for _, s := range tr.Segments {
		if strings.TrimSpace(s.Text) == "" || strings.Contains(s.Text, "\n") {
			t.Errorf("segment text not clean: %q", s.Text)
		}
	}
	if _, err := parseJSON3([]byte("nope")); err == nil {
		t.Error("garbage must be an error")
	}
	if _, err := parseJSON3([]byte(`{"events":[]}`)); !isAppErr(err, apperr.NoTranscript) {
		t.Errorf("empty track: %v", err)
	}
}

func TestSuggest(t *testing.T) {
	ss, err := newServer(t).Suggest(ctx(), "go tut", "en", "US")
	if err != nil || len(ss) < 3 {
		t.Fatalf("%v %v", err, ss)
	}
	for _, s := range ss {
		if s == "" {
			t.Error("empty suggestion")
		}
	}
	got, err := parseSuggest([]byte(`window.google.ac.h(["q",[["caf\u00e9 au lait",0],["x",0]],{}])`))
	if err != nil || len(got) != 2 || got[0] != "café au lait" {
		t.Errorf("jsonp: %v %v", got, err)
	}
	if _, err := parseSuggest([]byte("<html>")); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestTrending(t *testing.T) {
	c := newServer(t)
	vs, err := c.Trending(ctx(), "US", "music", 3)
	if err != nil || len(vs) != 3 {
		t.Fatalf("music: %v %d", err, len(vs))
	}
	for _, v := range vs {
		if v.ID == "" || v.Title == "" || v.Views <= 0 || v.ChannelName == "" {
			t.Errorf("chart entry: %+v", v)
		}
	}
	for _, cat := range []string{"", "now", "gaming", "movies"} {
		// The recorded results are old, so the one-day and one-week filters drop
		// them all: the call must still succeed with an empty list.
		vs, err := c.Trending(ctx(), "", cat, 3)
		if err != nil || vs == nil {
			t.Errorf("%q: %v %v", cat, err, vs)
		}
	}
	_, err = c.Trending(ctx(), "US", "bogus", 3)
	wantCode(t, err, apperr.InvalidInput)
}

func TestPlaylist(t *testing.T) {
	c := newServer(t)
	pl, vs, err := c.Playlist(ctx(), "PLRAV69dS1uWQGDQoBYMZWKjzuhCaOnBpa", 3)
	if err != nil {
		t.Fatal(err)
	}
	if pl.ID != "PLRAV69dS1uWQGDQoBYMZWKjzuhCaOnBpa" || pl.Title == "" || pl.ItemCount <= 0 || pl.URL == "" {
		t.Errorf("playlist: %+v", pl)
	}
	if len(vs) != 3 || vs[0].ID == "" || vs[0].Title == "" || vs[0].DurationS <= 0 {
		t.Errorf("videos: %d %+v", len(vs), vs)
	}
	_, _, err = c.Playlist(ctx(), "PLmissing", 3)
	wantCode(t, err, apperr.NotFound)
}

func TestHTTPErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("x") == "" && calls == 1 {
			http.Error(w, "busy", 503)
			return
		}
		w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	c := New()
	c.Base, c.Backoff = srv.URL, time.Millisecond
	_, err := c.Search(ctx(), yt.SearchOpts{Query: "x"})
	wantCode(t, err, apperr.YouTubeError)
	if calls < 2 {
		t.Errorf("a 503 should be retried once, calls=%d", calls)
	}
}

func TestParsers(t *testing.T) {
	counts := map[string]int64{"1.2M views": 1_200_000, "3,456 views": 3456, "No views": 0, "12K": 12000, "1.5B subscribers": 1_500_000_000, "": 0, "1 view": 1}
	for in, want := range counts {
		if got := parseCount(in); got != want {
			t.Errorf("parseCount(%q) = %d, want %d", in, got, want)
		}
	}
	durs := map[string]int{"3:32": 212, "1:02:03": 3723, "0:45": 45, "": 0, "x": 0}
	for in, want := range durs {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %d, want %d", in, got, want)
		}
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	tm, minAge, ok := parseAgo("3 weeks ago", now)
	if !ok || tm.After(now.AddDate(0, 0, -14)) || minAge < 14*24*time.Hour {
		t.Errorf("parseAgo: %v %v %v", tm, minAge, ok)
	}
	if _, _, ok := parseAgo("Streamed 2 hours ago", now); !ok {
		t.Error("streamed prefix")
	}
	if _, _, ok := parseAgo("whenever", now); ok {
		t.Error("garbage date must not parse")
	}
}

func TestDefensiveParsing(t *testing.T) {
	// Empty and odd shapes give zero values, never a panic.
	for _, v := range []any{nil, "x", 1.0, []any{}, obj{}, obj{"a": []any{nil, "x"}}} {
		_ = collectVideos(v, time.Now())
		_ = text(v)
		_ = nextToken(v)
		_ = parseChartsVideos(obj{"videos": v}, 5)
	}
	if _, err := parseChannelHeader(obj{}, "UC1", "UC1"); err == nil {
		t.Error("a channel page with no header is an unreadable layout")
	}
}

// A video with auto-dubbed audio lists an Arabic caption track first; with no
// language asked, the transcript must still be in the original (English).
func TestTranscriptOriginalLanguageWithDubs(t *testing.T) {
	var player obj
	_ = json.Unmarshal(fixture(t, "player_dubbed.json"), &player)
	tracks, original := parseTracks(player)
	if len(tracks) != 3 || tracks[0].Lang != "ar" || original != "en" {
		t.Fatalf("tracks=%d first=%s original=%q", len(tracks), tracks[0].Lang, original)
	}
	if tr := chooseTrack(tracks, "", original); tr == nil || tr.Lang != "en" || !tr.Auto {
		t.Errorf("default pick: %+v", tr)
	}
	if tr := chooseTrack(tracks, "fr", original); tr == nil || tr.Lang != "fr" {
		t.Errorf("explicit language must win: %+v", tr)
	}
	tr, err := newServer(t).Transcript(ctx(), "dubbed00000", "")
	if err != nil || tr.Language != "en" || !tr.Generated {
		t.Fatalf("transcript: %v %+v", err, tr)
	}
}
