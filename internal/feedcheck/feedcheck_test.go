package feedcheck

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/yt"
)

// fakeAgd stands in for agd: it logs its arguments and writes placeholder files.
// FAKE_AGD_FAIL=<command> makes that command exit 1; FAKE_AGD_INVALID=1 makes
// validate report an error.
const fakeAgd = `#!/bin/sh
echo "$*" >> "$FAKE_AGD_LOG"
cmd=$1; shift
[ "$FAKE_AGD_FAIL" = "$cmd" ] && { echo boom >&2; exit 1; }
[ -n "$FAKE_AGD_SLEEP" ] && sleep "$FAKE_AGD_SLEEP"
out=""; input=""
while [ $# -gt 0 ]; do
  case "$1" in -o) out=$2; shift;; -i) input=$2; shift;; esac
  shift
done
case "$cmd" in
  new) cp "$input" "$out"; echo '{"ok":true}';;
  theme) echo '{"ok":true}';;
  render) mkdir -p "$out"; echo png > "$out/01-s1.png"; echo "{\"ok\":true,\"result\":{\"slides\":[{\"id\":\"s1\",\"file\":\"$out/01-s1.png\"}]}}";;
  validate)
    if [ -n "$FAKE_AGD_INVALID" ]; then
      echo '{"ok":false,"issues":[{"code":"overflow","severity":"error","layer":"htitle-1","message":"too long"},{"code":"contrast","severity":"warning","layer":"x","message":"meh"}]}'; exit 3
    fi
    echo '{"ok":true,"issues":[]}';;
esac
`

type fakeReader struct {
	videos   []model.Video
	searches int32
}

func (f *fakeReader) Search(context.Context, yt.SearchOpts) ([]model.Video, error) {
	atomic.AddInt32(&f.searches, 1)
	return f.videos, nil
}
func (f *fakeReader) Video(context.Context, string) (*model.Video, error) { return nil, nil }
func (f *fakeReader) Channel(_ context.Context, id string, _ yt.ChannelOpts) (*model.Channel, error) {
	return &model.Channel{ID: id, Name: id, Thumbnail: baseURL + "/avatar/" + id + ".jpg"}, nil
}
func (f *fakeReader) Comments(context.Context, string, yt.CommentOpts) ([]model.Comment, error) {
	return nil, nil
}
func (f *fakeReader) Transcript(context.Context, string, string) (*model.Transcript, error) {
	return nil, nil
}
func (f *fakeReader) Suggest(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}
func (f *fakeReader) Trending(context.Context, string, string, int) ([]model.Video, error) {
	return nil, nil
}
func (f *fakeReader) Playlist(context.Context, string, int) (*model.Playlist, []model.Video, error) {
	return nil, nil, nil
}

var baseURL string

type env struct {
	dir    string // --out
	agdLog string
	reader *fakeReader
	thumbs []string
}

// setup wires everything offline: a fake reader, an image server, temp state and (when withAgd) a fake agd.
func setup(t *testing.T, withAgd bool) *env {
	t.Helper()
	t.Setenv("YTX_HOME", t.TempDir())
	t.Setenv("YTX_NO_KEYCHAIN", "1")
	t.Setenv("YOUTUBE_API_KEY", "")
	t.Setenv("FAKE_AGD_FAIL", "")
	t.Setenv("FAKE_AGD_INVALID", "")
	t.Setenv("FAKE_AGD_SLEEP", "")
	e := &env{dir: t.TempDir()}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "UCnoavatar"):
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpegbytes " + r.URL.Path))
		}
	}))
	t.Cleanup(srv.Close)
	baseURL = srv.URL

	raw, err := os.ReadFile("../../testdata/feedcheck/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []model.Video
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "{{BASE}}", srv.URL)), &vs); err != nil {
		t.Fatal(err)
	}
	e.reader = &fakeReader{videos: vs}
	yt.RegisterKeyless(func() yt.Reader { return e.reader })

	oldNow, oldPick, oldSigned := now, pick, signedIn
	now = func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
	pick = func(n int) int { return 3 } // blind position 4
	signedIn = func(context.Context) *mine { return nil }
	t.Cleanup(func() { now, pick, signedIn = oldNow, oldPick, oldSigned })

	if withAgd {
		bin := filepath.Join(t.TempDir(), "agd")
		if err := os.WriteFile(bin, []byte(fakeAgd), 0o755); err != nil {
			t.Fatal(err)
		}
		e.agdLog = filepath.Join(t.TempDir(), "agd.log")
		t.Setenv("YTX_AGD", bin)
		t.Setenv("FAKE_AGD_LOG", e.agdLog)
	} else {
		t.Setenv("YTX_AGD", "")
		t.Setenv("PATH", t.TempDir()) // no agd anywhere
	}

	for i, size := range [][2]int{{1280, 720}, {1920, 1080}} {
		p := filepath.Join(t.TempDir(), []string{"a.png", "b.png"}[i])
		writePNG(t, p, size[0], size[1])
		e.thumbs = append(e.thumbs, p)
	}
	return e
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x += 7 {
		img.Set(x, x%h, color.White)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func (e *env) call(t *testing.T, vals map[string]any) (*ops.Result, *Output, error) {
	t.Helper()
	in := ops.Input{Op: ops.Find("feedcheck"), Values: map[string]any{"out": e.dir, "position": 2, "theme": "light", "region": "US"},
		Given: map[string]bool{}, Source: "keyless", NoCache: true}
	for k, v := range vals {
		in.Values[k] = v
	}
	res, err := run(context.Background(), in)
	if err != nil {
		return nil, nil, err
	}
	return res, res.Data.(*Output), nil
}

func (e *env) basic(extra map[string]any) map[string]any {
	m := map[string]any{"topic": "Coffee at home", "thumb": []string{e.thumbs[0]}, "title": []string{"I made coffee, at home"}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

type videoItem struct {
	Thumb    string          `json:"thumb"`
	Title    json.RawMessage `json:"title"`
	Channel  json.RawMessage `json:"channel"`
	Avatar   string          `json:"avatar"`
	Views    string          `json:"views"`
	Age      string          `json:"age"`
	Duration string          `json:"duration"`
	Yours    bool            `json:"yours"`
}

type inputFile struct {
	Slides []struct {
		Heading string          `json:"heading"`
		Note    string          `json:"note"`
		Marker  json.RawMessage `json:"marker"`
		Videos  []videoItem     `json:"videos"`
		// pointer: nil when the key is absent
		PhoneFrom *int `json:"phoneFrom"`
	} `json:"slides"`
}

func readInput(t *testing.T, path string) inputFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f inputFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestGeneratedInput(t *testing.T) {
	e := setup(t, true)
	signedIn = func(context.Context) *mine { return &mine{ID: "UCown"} }
	res, out, err := e.call(t, e.basic(map[string]any{"title": []string{"I made coffee, at home"}, "channel": "Me Myself", "duration": "12:04"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Competitors) != 5 || out.YoursPosition != 2 {
		t.Fatalf("competitors %d, position %d", len(out.Competitors), out.YoursPosition)
	}
	if !strings.HasSuffix(out.Folder, "/feedcheck/coffee-at-home-2026-11-12") {
		t.Errorf("folder %s", out.Folder)
	}
	f := readInput(t, filepath.Join(out.Folder, "v1.input.json"))
	vids := f.Slides[0].Videos
	if len(vids) != 6 {
		t.Fatalf("%d videos", len(vids))
	}
	if !vids[1].Yours || vids[0].Yours || vids[2].Yours {
		t.Errorf("yours not at position 2: %+v", vids)
	}
	you := vids[1]
	if you.Views != "No views" || you.Age != "just now" || you.Duration != "12:04" || string(you.Channel) != `"Me Myself"` || string(you.Title) != `"I made coffee, at home"` {
		t.Errorf("your card: %+v", you)
	}
	if !strings.HasSuffix(you.Thumb, "thumb-1.png") {
		t.Errorf("your thumb %s", you.Thumb)
	}
	// Competitors in search order around you: lat1, you, ar1, c6, c7, c8 (own1, shorts, live, upcoming dropped).
	want := []struct {
		idx             int
		views, age, dur string
	}{
		{0, "1.2M views", "2 years ago", "14:07"}, {2, "999 views", "3 weeks ago", "1:02:03"},
		{3, "1 view", "1 day ago", "8:15"}, {4, "18K views", "3 days ago", "11:42"}, {5, "610K views", "5 months ago", "22:51"},
	}
	for _, w := range want {
		v := vids[w.idx]
		if v.Views != w.views || v.Age != w.age || v.Duration != w.dur {
			t.Errorf("video %d: %+v, want %+v", w.idx, v, w)
		}
	}
	ar := vids[2]
	var rt, rc map[string]string
	if json.Unmarshal(ar.Title, &rt) != nil || json.Unmarshal(ar.Channel, &rc) != nil ||
		rt["dir"] != "rtl" || rt["text"] != "كيكة الهيل والفستق بدون فرن" || rc["dir"] != "rtl" || rc["text"] != "مطبخ سلمى" {
		t.Errorf("rtl not marked: %s / %s", ar.Title, ar.Channel)
	}
	if string(vids[0].Title)[0] != '"' {
		t.Errorf("a latin title stays a plain string: %s", vids[0].Title)
	}
	if !strings.HasSuffix(vids[0].Avatar, "UCbean-avatar.jpg") || vids[4].Avatar == "" {
		t.Errorf("avatars: %q %q", vids[0].Avatar, vids[4].Avatar)
	}
	if vids[5].Avatar != "" {
		t.Errorf("a failed avatar lookup must be left out, got %q", vids[5].Avatar)
	}
	if f.Slides[0].Heading != "I made coffee, at home" || f.Slides[0].Note != "Thumbnail 1, title 1 · 2026-11-12" || len(f.Slides[0].Marker) != 0 {
		t.Errorf("slide: %+v", f.Slides[0])
	}
	// The thumbnails and avatars are really in the folder.
	for _, name := range []string{"lat1.jpg", "ar1.jpg", "UCbean-avatar.jpg", "thumb-1.png", "v1.png", "v1-blind.png", "v1.agd.json"} {
		if _, err := os.Stat(filepath.Join(out.Folder, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(out.Folder, "v1.out")); err == nil {
		t.Error("agd's output folder should be gone")
	}
	if len(res.Warnings) != 0 {
		// 1280x720 is exactly the minimum and 16:9, so no thumbnail warning is expected.
		t.Errorf("warnings: %v", res.Warnings)
	}
	for _, c := range out.Competitors {
		if c.Title == "" || c.Channel == "" || !strings.HasPrefix(c.URL, "https://www.youtube.com/watch?v=") || c.Views == "" {
			t.Errorf("competitor %+v", c)
		}
	}
}

func TestFilteringAndOwnChannel(t *testing.T) {
	e := setup(t, false)
	_, out, err := e.call(t, e.basic(nil))
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, c := range out.Competitors {
		titles = append(titles, c.Title)
	}
	joined := strings.Join(titles, "|")
	for _, banned := range []string{"#shorts", "live stream", "Premiere"} {
		if strings.Contains(joined, banned) {
			t.Errorf("%q was not filtered: %s", banned, joined)
		}
	}
	if !strings.Contains(joined, "My own coffee video") {
		t.Errorf("own video should stay while your channel is unknown: %s", joined)
	}
	signedIn = func(context.Context) *mine {
		return &mine{ID: "UCown", Name: "Real Me", Avatar: baseURL + "/avatar/UCown.jpg"}
	}
	_, out, err = e.call(t, e.basic(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Competitors {
		if c.Title == "My own coffee video" {
			t.Error("own channel not filtered")
		}
	}
	if len(out.Competitors) != 5 || out.Competitors[4].Title != "A slow Sunday morning routine" {
		t.Errorf("fifth competitor should move up: %+v", out.Competitors)
	}
	f := readInput(t, filepath.Join(out.Folder, "v1.input.json"))
	you := f.Slides[0].Videos[1]
	if string(you.Channel) != `"Real Me"` || !strings.HasSuffix(you.Avatar, "avatar-you.jpg") {
		t.Errorf("signed-in card: %+v", you)
	}
	// Without --duration there is no badge.
	if you.Duration != "" {
		t.Errorf("duration %q", you.Duration)
	}
}

func TestDefaultChannelName(t *testing.T) {
	e := setup(t, false)
	_, out, err := e.call(t, e.basic(nil))
	if err != nil {
		t.Fatal(err)
	}
	you := readInput(t, filepath.Join(out.Folder, "v1.input.json")).Slides[0].Videos[1]
	if string(you.Channel) != `"Your channel"` || you.Avatar != "" {
		t.Errorf("%+v", you)
	}
}

func TestBlindSheet(t *testing.T) {
	e := setup(t, true)
	_, out, err := e.call(t, e.basic(map[string]any{"position": 3}))
	if err != nil {
		t.Fatal(err)
	}
	s := out.Sheets[0]
	if s.BlindPosition != 4 || out.YoursPosition != 3 {
		t.Fatalf("blind %d, marked %d", s.BlindPosition, out.YoursPosition)
	}
	marked := readInput(t, filepath.Join(out.Folder, "v1.input.json")).Slides[0]
	blind := readInput(t, filepath.Join(out.Folder, "v1-blind.input.json")).Slides[0]
	if !marked.Videos[2].Yours || !strings.Contains(blind.Videos[3].Thumb, "thumb-1") {
		t.Error("yours at the wrong place")
	}
	// pick pins index 3 of the windows holding card 4 of 6 (starts 2, 3, 4), clamped to the last
	if blind.PhoneFrom == nil || *blind.PhoneFrom != 4 || marked.PhoneFrom != nil {
		t.Errorf("phoneFrom: marked %v blind %v, want absent and 4", marked.PhoneFrom, blind.PhoneFrom)
	}
	if len(marked.Marker) != 0 || string(blind.Marker) != "false" {
		t.Errorf("marker: marked %q blind %q", marked.Marker, blind.Marker)
	}
	// Nothing in a file name may reveal the blind position.
	entries, _ := os.ReadDir(out.Folder)
	for _, en := range entries {
		if strings.ContainsAny(en.Name(), "456") && !strings.HasPrefix(en.Name(), "UC") && !strings.HasPrefix(en.Name(), "c") {
			t.Errorf("file name %q may leak the position", en.Name())
		}
	}
	for _, p := range []string{s.MarkedPNG, s.BlindPNG, s.AgdFile, s.AgdBlindFile} {
		if strings.Contains(filepath.Base(p), "4") || strings.Contains(filepath.Base(p), "pos") {
			t.Errorf("name %s", p)
		}
	}
}

func TestCombinations(t *testing.T) {
	e := setup(t, true)
	_, out, err := e.call(t, e.basic(map[string]any{"thumb": e.thumbs, "title": []string{"First, title", "Second title"}, "theme": "dark"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sheets) != 4 {
		t.Fatalf("%d sheets", len(out.Sheets))
	}
	want := [][2]string{{e.thumbs[0], "First, title"}, {e.thumbs[0], "Second title"}, {e.thumbs[1], "First, title"}, {e.thumbs[1], "Second title"}}
	for i, s := range out.Sheets {
		if s.Thumb != want[i][0] || s.Title != want[i][1] {
			t.Errorf("sheet %d: %+v", i, s)
		}
	}
	// Every sheet shows its own title and thumbnail; every blind sheet is neutral.
	titles := []string{"First, title", "Second title", "First, title", "Second title"}
	for i := 0; i < 4; i++ {
		label := "v" + string(rune('1'+i))
		m := readInput(t, filepath.Join(out.Folder, label+".input.json")).Slides[0]
		wantNote := "Thumbnail " + string(rune('1'+i/2)) + ", title " + string(rune('1'+i%2)) + " · 2026-11-12"
		if m.Heading != titles[i] || m.Note != wantNote {
			t.Errorf("%s: heading %q note %q", label, m.Heading, m.Note)
		}
		if !strings.HasSuffix(m.Videos[1].Thumb, "thumb-"+string(rune('1'+i/2))+".png") || string(m.Videos[1].Title) != `"`+titles[i]+`"` {
			t.Errorf("%s: your card %+v", label, m.Videos[1])
		}
		b := readInput(t, filepath.Join(out.Folder, label+"-blind.input.json")).Slides[0]
		if b.Heading != "Coffee at home" || b.Note != "Blind check · 2026-11-12" {
			t.Errorf("%s blind: heading %q note %q", label, b.Heading, b.Note)
		}
		if strings.Contains(b.Heading+b.Note, "title") || strings.Contains(b.Heading+b.Note, "Thumbnail") {
			t.Errorf("%s blind leaks the version", label)
		}
	}
	// Same five on every sheet.
	a := readInput(t, filepath.Join(out.Folder, "v1.input.json")).Slides[0].Videos
	b := readInput(t, filepath.Join(out.Folder, "v4-blind.input.json")).Slides[0].Videos
	set := func(vs []videoItem) string {
		var s []string
		for _, v := range vs {
			if !strings.Contains(v.Thumb, "thumb-") {
				s = append(s, v.Thumb)
			}
		}
		return strings.Join(s, ",")
	}
	if set(a) != set(b) {
		t.Errorf("competitors differ between sheets")
	}
	// agd ran new, theme, render, validate for each of the 8 designs.
	log, _ := os.ReadFile(e.agdLog)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 32 {
		t.Fatalf("%d agd calls:\n%s", len(lines), log)
	}
	first := lines[:4]
	for i, prefix := range []string{"new y4-feed-preview -i v1.input.json -o v1.agd.json", "theme v1.agd.json dark", "render v1.agd.json", "validate v1.agd.json --json"} {
		if !strings.HasPrefix(first[i], prefix) {
			t.Errorf("call %d = %q, want prefix %q", i, first[i], prefix)
		}
	}
}

func TestLightThemeSkipsThemeCall(t *testing.T) {
	e := setup(t, true)
	if _, _, err := e.call(t, e.basic(nil)); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(e.agdLog)
	if strings.Contains(string(log), "theme") {
		t.Errorf("light needs no theme call:\n%s", log)
	}
}

func TestNoAgd(t *testing.T) {
	e := setup(t, false)
	res, out, err := e.call(t, e.basic(map[string]any{"theme": "ash"}))
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, w := range res.Warnings {
		if strings.Contains(w, "agd was not found") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("no agd warning: %v", res.Warnings)
	}
	for _, part := range []string{"cd " + out.Folder, "agd new y4-feed-preview -i v1.input.json -o v1.agd.json", "agd theme v1.agd.json ash", "agd render v1-blind.agd.json", "agd validate v1-blind.agd.json --json"} {
		if !strings.Contains(found, part) {
			t.Errorf("warning lacks %q:\n%s", part, found)
		}
	}
	for _, name := range []string{"v1.input.json", "v1-blind.input.json", "lat1.jpg"} {
		if _, err := os.Stat(filepath.Join(out.Folder, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if out.Sheets[0].MarkedPNG != "" || out.Sheets[0].AgdFile == "" {
		t.Errorf("sheet: %+v", out.Sheets[0])
	}
}

func TestValidateErrorsAreWarnings(t *testing.T) {
	e := setup(t, true)
	t.Setenv("FAKE_AGD_INVALID", "1")
	res, _, err := e.call(t, e.basic(nil))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range res.Warnings {
		if strings.Contains(w, "sheet v1: agd validate: overflow htitle-1") || strings.Contains(w, "sheet v1-blind: agd validate: overflow") {
			n++
		}
		if strings.Contains(w, "contrast") {
			t.Errorf("only errors become warnings: %s", w)
		}
	}
	if n != 2 {
		t.Errorf("warnings: %v", res.Warnings)
	}
}

func TestRenderFailureIsAnError(t *testing.T) {
	e := setup(t, true)
	t.Setenv("FAKE_AGD_FAIL", "render")
	_, _, err := e.call(t, e.basic(nil))
	if err == nil || !strings.Contains(err.Error(), "render failed for v1") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadInput(t *testing.T) {
	e := setup(t, false)
	text := filepath.Join(t.TempDir(), "notimage.png")
	_ = os.WriteFile(text, []byte("hello"), 0o644)
	cases := map[string]map[string]any{
		"not an image":    e.basic(map[string]any{"thumb": []string{text}}),
		"missing file":    e.basic(map[string]any{"thumb": []string{filepath.Join(t.TempDir(), "nope.png")}}),
		"title too long":  e.basic(map[string]any{"title": []string{strings.Repeat("x", 101)}}),
		"empty title":     e.basic(map[string]any{"title": []string{"  "}}),
		"no title":        e.basic(map[string]any{"title": []string(nil)}),
		"no thumb":        e.basic(map[string]any{"thumb": []string(nil)}),
		"position 0":      e.basic(map[string]any{"position": 0}),
		"position 7":      e.basic(map[string]any{"position": 7}),
		"bad duration":    e.basic(map[string]any{"duration": "soon"}),
		"bad theme":       e.basic(map[string]any{"theme": "../x"}),
		"empty topic":     e.basic(map[string]any{"topic": " "}),
		"too many sheets": e.basic(map[string]any{"thumb": []string{e.thumbs[0], e.thumbs[0], e.thumbs[0], e.thumbs[0]}, "title": []string{"a", "b", "c", "d"}}),
	}
	for name, vals := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := e.call(t, vals)
			var ae *apperr.Error
			if err == nil || !asErr(err, &ae) || ae.Code != apperr.InvalidInput {
				t.Fatalf("err = %v, want invalid_input", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(e.dir, "feedcheck")); err == nil {
		t.Error("a rejected input must not create the folder")
	}
	if n := atomic.LoadInt32(&e.reader.searches); n != 0 {
		t.Errorf("a rejected input must not search (%d searches)", n)
	}
	// A title of exactly 100 characters is fine.
	if _, _, err := e.call(t, e.basic(map[string]any{"title": []string{strings.Repeat("é", 100)}})); err != nil {
		t.Errorf("100 chars: %v", err)
	}
}

func asErr(err error, target **apperr.Error) bool {
	ae, ok := err.(*apperr.Error)
	*target = ae
	return ok
}

func TestThumbWarnings(t *testing.T) {
	e := setup(t, false)
	square := filepath.Join(t.TempDir(), "sq.png")
	writePNG(t, square, 600, 600)
	res, _, err := e.call(t, e.basic(map[string]any{"thumb": []string{square}}))
	if err != nil {
		t.Fatal(err)
	}
	var ratio, small bool
	for _, w := range res.Warnings {
		ratio = ratio || strings.Contains(w, "not 16:9")
		small = small || strings.Contains(w, "smaller than 1280x720")
	}
	if !ratio || !small {
		t.Errorf("warnings: %v", res.Warnings)
	}
	// 16:9 within 2% and big enough: no warning about either.
	ok := filepath.Join(t.TempDir(), "ok.png")
	writePNG(t, ok, 1280, 725)
	res, _, err = e.call(t, e.basic(map[string]any{"thumb": []string{ok}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "16:9") || strings.Contains(w, "smaller") {
			t.Errorf("unexpected warning %s", w)
		}
	}
	// Over 2 MB: noise that PNG can't compress.
	big := filepath.Join(t.TempDir(), "big.png")
	img := image.NewNRGBA(image.Rect(0, 0, 1280, 720))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = byte(seed >> 24)
	}
	f, _ := os.Create(big)
	_ = png.Encode(f, img)
	f.Close()
	res, _, err = e.call(t, e.basic(map[string]any{"thumb": []string{big}}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range res.Warnings {
		found = found || strings.Contains(w, "accepts up to 2 MB")
	}
	if !found {
		t.Errorf("no size warning: %v", res.Warnings)
	}
}

func TestFolderNeverOverwritten(t *testing.T) {
	e := setup(t, false)
	var folders []string
	for i := 0; i < 3; i++ {
		_, out, err := e.call(t, e.basic(nil))
		if err != nil {
			t.Fatal(err)
		}
		folders = append(folders, filepath.Base(out.Folder))
	}
	want := []string{"coffee-at-home-2026-11-12", "coffee-at-home-2026-11-12-2", "coffee-at-home-2026-11-12-3"}
	for i := range want {
		if folders[i] != want[i] {
			t.Errorf("folders %v, want %v", folders, want)
		}
	}
	// A marker file in the first folder survives later runs.
	first := filepath.Join(e.dir, "feedcheck", want[0])
	if _, err := os.Stat(filepath.Join(first, "v1.input.json")); err != nil {
		t.Error(err)
	}
}

func TestResearchDirIsUsed(t *testing.T) {
	e := setup(t, false)
	rd := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"research_dir": rd})
	if err := os.WriteFile(filepath.Join(os.Getenv("YTX_HOME"), "config.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, err := e.call(t, e.basic(map[string]any{"out": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(out.Folder) != filepath.Join(rd, "feedcheck") {
		t.Errorf("folder %s", out.Folder)
	}
}

func TestDownloadRules(t *testing.T) {
	big := make([]byte, maxDownload+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "image/png; charset=binary")
			_, _ = w.Write([]byte("x"))
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		case "/big":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(big)
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	if p, err := download(context.Background(), srv.URL+"/ok", dir, "a"); err != nil || filepath.Base(p) != "a.png" {
		t.Errorf("ok: %v %v", p, err)
	}
	for _, path := range []string{"/html", "/big", "/svg", "/404"} {
		if _, err := download(context.Background(), srv.URL+path, dir, "b"); err == nil {
			t.Errorf("%s should fail", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "b.jpg")); err == nil {
		t.Error("an oversize download must not leave a file")
	}
	if _, err := download(context.Background(), "file:///etc/passwd", dir, "c"); err == nil {
		t.Error("non-http URL accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := download(ctx, srv.URL+"/ok", dir, "d"); err == nil {
		t.Error("cancelled context should fail")
	}
}

func TestFailedThumbIsReplaced(t *testing.T) {
	e := setup(t, false)
	for i := range e.reader.videos {
		if e.reader.videos[i].ID == "ar1" {
			e.reader.videos[i].Thumbnail = baseURL + "/missing/UCnoavatar.jpg" // the server 404s this path
		}
	}
	res, out, err := e.call(t, e.basic(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Competitors) != 5 {
		t.Fatalf("%d competitors", len(out.Competitors))
	}
	for _, c := range out.Competitors {
		if strings.Contains(c.Title, "كيكة") {
			t.Error("the failed one should be replaced")
		}
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "thumbnail download failed") {
		t.Errorf("warnings: %v", res.Warnings)
	}
}

func TestStringsInput(t *testing.T) {
	in := ops.Input{Values: map[string]any{"a": []string{"x, y", "z"}, "b": "one"}}
	if got := in.Strings("a"); len(got) != 2 || got[0] != "x, y" {
		t.Errorf("%v", got)
	}
	if got := in.Strings("b"); len(got) != 1 || got[0] != "one" {
		t.Errorf("%v", got)
	}
	if in.Strings("none") != nil {
		t.Error("absent should be nil")
	}
}

func TestBlindCardLooksLikeACompetitor(t *testing.T) {
	e := setup(t, false)
	_, out, err := e.call(t, e.basic(map[string]any{"duration": "9:30"}))
	if err != nil {
		t.Fatal(err)
	}
	// The five without your own video have views 1.25M, 999, 1, 18K and 610K: the median is 18K.
	for _, tc := range []struct {
		file, views, age string
		yours            bool
	}{{"v1.input.json", "No views", "just now", true}, {"v1-blind.input.json", "999 views", "2 days ago", false}} {
		raw, _ := os.ReadFile(filepath.Join(out.Folder, tc.file))
		var doc struct {
			Slides []struct {
				Videos []map[string]any `json:"videos"`
			} `json:"slides"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, v := range doc.Slides[0].Videos {
			_, has := v["yours"]
			if has != tc.yours && !tc.yours {
				t.Errorf("%s: a blind video carries the yours key: %v", tc.file, v)
			}
			if strings.Contains(v["thumb"].(string), "thumb-") {
				n++
				if v["views"] != tc.views || v["age"] != tc.age || v["duration"] != "9:30" {
					t.Errorf("%s: your card %v", tc.file, v)
				}
				if has != tc.yours {
					t.Errorf("%s: yours key = %v, want %v", tc.file, has, tc.yours)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %d cards of yours", tc.file, n)
		}
	}
}

func TestFewCompetitorsPositions(t *testing.T) {
	e := setup(t, false)
	e.reader.videos = e.reader.videos[:6] // lat1, short1, live1, own1, ar1, c6: three usable besides own1
	pick = func(n int) int { return n - 1 }
	res, out, err := e.call(t, e.basic(map[string]any{"position": 6}))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Competitors) != 4 { // own1 stays while your channel is unknown
		t.Fatalf("%d competitors", len(out.Competitors))
	}
	if out.YoursPosition != 5 || out.Sheets[0].BlindPosition != 5 {
		t.Errorf("positions: yours %d blind %d, want 5 and 5", out.YoursPosition, out.Sheets[0].BlindPosition)
	}
	for _, f := range []string{"v1.input.json", "v1-blind.input.json"} {
		v := readInput(t, filepath.Join(out.Folder, f)).Slides[0].Videos
		if len(v) != 5 || !strings.Contains(v[4].Thumb, "thumb-1") {
			t.Errorf("%s: yours not last of %d", f, len(v))
		}
	}
	w := strings.Join(res.Warnings, "\n")
	if !strings.Contains(w, "only 4 competitors") || !strings.Contains(w, "past the end") {
		t.Errorf("warnings: %v", res.Warnings)
	}
}

func TestOwnChannelFilteredWithFlags(t *testing.T) {
	e := setup(t, false)
	signedIn = func(context.Context) *mine {
		return &mine{ID: "UCown", Name: "Stored Name", Avatar: baseURL + "/avatar/UCown.jpg"}
	}
	av := filepath.Join(t.TempDir(), "me.png")
	writePNG(t, av, 64, 64)
	_, out, err := e.call(t, e.basic(map[string]any{"channel": "Flag Name", "avatar": av}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Competitors {
		if c.Title == "My own coffee video" {
			t.Error("own channel not filtered when --channel and --avatar are given")
		}
	}
	you := readInput(t, filepath.Join(out.Folder, "v1.input.json")).Slides[0].Videos[1]
	if string(you.Channel) != `"Flag Name"` || !strings.HasSuffix(you.Avatar, "avatar-you.png") || filepath.Dir(you.Avatar) != out.Folder {
		t.Errorf("your card: %+v", you)
	}
}

func TestLocalAvatarChecked(t *testing.T) {
	e := setup(t, false)
	bad := filepath.Join(t.TempDir(), "a.png")
	_ = os.WriteFile(bad, []byte("nope"), 0o644)
	for _, p := range []string{bad, filepath.Join(t.TempDir(), "missing.png")} {
		_, _, err := e.call(t, e.basic(map[string]any{"avatar": p}))
		var ae *apperr.Error
		if err == nil || !asErr(err, &ae) || ae.Code != apperr.InvalidInput {
			t.Errorf("%s: err = %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "feedcheck")); err == nil {
		t.Error("a rejected avatar must not create the folder")
	}
}

func TestThumbExtensionFromContent(t *testing.T) {
	e := setup(t, false)
	dir := t.TempDir()
	noExt := filepath.Join(dir, "thumbnail")
	writePNG(t, noExt, 1280, 720)
	jpgAsPng := filepath.Join(dir, "really.png")
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	f, _ := os.Create(jpgAsPng)
	_ = jpeg.Encode(f, img, nil)
	f.Close()
	_, out, err := e.call(t, e.basic(map[string]any{"thumb": []string{noExt, jpgAsPng}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"thumb-1.png", "thumb-2.jpg"} {
		if _, err := os.Stat(filepath.Join(out.Folder, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
}

func TestTimeoutCodes(t *testing.T) {
	path := os.Getenv("PATH") // the no-agd setup empties PATH; the fake agd needs sleep and mkdir
	e := setup(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := ops.Input{Op: ops.Find("feedcheck"), Values: map[string]any{"out": e.dir, "position": 2, "theme": "light", "region": "US",
		"topic": "x", "thumb": []string{e.thumbs[0]}, "title": []string{"t"}}, Given: map[string]bool{}, Source: "keyless", NoCache: true}
	_, err := run(ctx, in)
	var ae *apperr.Error
	if err == nil || !asErr(err, &ae) || ae.Code != apperr.Timeout {
		t.Errorf("downloads on a cancelled context: %v", err)
	}

	e = setup(t, true)
	t.Setenv("PATH", path)
	t.Setenv("FAKE_AGD_SLEEP", "3")
	tctx, tcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer tcancel()
	in.Values["out"] = e.dir
	start := time.Now()
	_, err = run(tctx, in)
	if err == nil || !asErr(err, &ae) || ae.Code != apperr.Timeout {
		t.Errorf("agd past the deadline: %v", err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("took %v", time.Since(start))
	}
}

// The blind phone window always holds your video and stays inside the feed, for every random pick.
func TestPhoneStart(t *testing.T) {
	old := pick
	t.Cleanup(func() { pick = old })
	for _, n := range []int{4, 6} {
		for p := 1; p <= min(6, n); p++ {
			lo, hi := max(1, p-2), min(p, max(1, n-2))
			seen := map[int]bool{}
			for k := 0; k < 5; k++ {
				pick = func(size int) int { return k % size }
				from := phoneStart(p, n)
				seen[from] = true
				if from < lo || from > hi || from < 1 || from > 4 || from+2 > max(n, 3) || p < from || p > from+2 {
					t.Errorf("n=%d p=%d: phoneFrom %d does not hold p in range", n, p, from)
				}
			}
			if len(seen) != hi-lo+1 {
				t.Errorf("n=%d p=%d: saw starts %v, want every start from %d to %d", n, p, seen, lo, hi)
			}
		}
	}
}
