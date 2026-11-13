package research

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fake is an offline yt.Reader.
type fake struct {
	videos      []model.Video
	channels    map[string]*model.Channel // missing id = channel error
	comments    map[string][]model.Comment
	transcripts map[string]*model.Transcript // missing id = no transcript
	suggest     map[string][]string
	inflight    int32
	maxInflight int32
}

func (f *fake) Search(context.Context, yt.SearchOpts) ([]model.Video, error) { return f.videos, nil }
func (f *fake) Video(context.Context, string) (*model.Video, error)          { return nil, errors.New("na") }
func (f *fake) Channel(_ context.Context, ref string, _ yt.ChannelOpts) (*model.Channel, error) {
	n := atomic.AddInt32(&f.inflight, 1)
	defer atomic.AddInt32(&f.inflight, -1)
	for {
		m := atomic.LoadInt32(&f.maxInflight)
		if n <= m || atomic.CompareAndSwapInt32(&f.maxInflight, m, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	if c, ok := f.channels[ref]; ok {
		return c, nil
	}
	return nil, errors.New("channel gone")
}
func (f *fake) Comments(_ context.Context, id string, _ yt.CommentOpts) ([]model.Comment, error) {
	return f.comments[id], nil
}
func (f *fake) Transcript(_ context.Context, id, _ string) (*model.Transcript, error) {
	if t, ok := f.transcripts[id]; ok {
		return t, nil
	}
	return nil, errors.New("no transcript")
}
func (f *fake) Suggest(_ context.Context, q, _, _ string) ([]string, error) { return f.suggest[q], nil }
func (f *fake) Trending(context.Context, string, string, int) ([]model.Video, error) {
	return nil, nil
}
func (f *fake) Playlist(context.Context, string, int) (*model.Playlist, []model.Video, error) {
	return nil, nil, nil
}

func vid(id, ch string, views int64) model.Video {
	return model.Video{ID: id, URL: "https://www.youtube.com/watch?v=" + id, Title: "Video " + id, ChannelID: ch, ChannelName: "Chan " + ch, Views: views, DurationS: 605, AgeDays: 40}
}

func recent(views ...int64) []model.Video {
	var vs []model.Video
	for _, v := range views {
		vs = append(vs, model.Video{Views: v})
	}
	return vs
}

func TestScoreOutliers(t *testing.T) {
	live := vid("live", "A", 5000)
	live.IsLive = true
	f := &fake{channels: map[string]*model.Channel{
		"A": {ID: "A", Name: "Chan A", URL: "https://www.youtube.com/channel/A", Subscribers: 1000, Recent: recent(100, 150, 200, 250, 300)},  // odd: 200
		"B": {ID: "B", Name: "Chan B", URL: "https://www.youtube.com/channel/B", Subscribers: 50, Recent: recent(100, 200, 250, 300, 400, 0)}, // even, zero skipped: 250
	}}
	vs := []model.Video{vid("a1", "A", 600), vid("b1", "B", 500), vid("zero", "A", 0), live, vid("c1", "C", 900)}
	rows, chans, warns := ScoreOutliers(context.Background(), f, vs, 30)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (zero and live skipped), got %d", len(rows))
	}
	got := map[string]OutlierRow{}
	for _, r := range rows {
		got[r.ID] = r
	}
	if got["a1"].Outlier != 3 || got["a1"].ChannelMedian != 200 || got["a1"].ChannelSubscribers != 1000 {
		t.Fatalf("a1 = %+v", got["a1"])
	}
	if got["b1"].Outlier != 2 || got["b1"].ChannelMedian != 250 {
		t.Fatalf("b1 = %+v", got["b1"])
	}
	if c, ok := got["c1"]; !ok || c.Outlier != 0 {
		t.Fatalf("failed channel must keep its video with outlier 0, got %+v", c)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "C") {
		t.Fatalf("warnings = %v", warns)
	}
	if rows[0].ID != "a1" || rows[2].ID != "c1" {
		t.Fatalf("not sorted by outlier: %v %v %v", rows[0].ID, rows[1].ID, rows[2].ID)
	}
	if len(chans) != 2 {
		t.Fatalf("channels = %d", len(chans))
	}
}

func TestScoreOutliersConcurrencyCap(t *testing.T) {
	f := &fake{channels: map[string]*model.Channel{}}
	var vs []model.Video
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"} {
		f.channels[id] = &model.Channel{ID: id, Recent: recent(10, 10, 10, 10, 10)}
		vs = append(vs, vid("v"+id, id, 10))
	}
	ScoreOutliers(context.Background(), f, vs, 5)
	if f.maxInflight > concurrency || f.maxInflight < 2 {
		t.Fatalf("max in flight = %d, want 2..%d", f.maxInflight, concurrency)
	}
}

func gapFixture() *fake {
	return &fake{
		videos: []model.Video{vid("v1", "A", 1000), vid("v2", "B", 2000), vid("v3", "B", 3000)},
		comments: map[string][]model.Comment{
			"v1": {
				{ID: "1", Author: "x", Text: "How do I deploy this to kubernetes cluster?", Likes: 50},
				{ID: "2", Author: "y", Text: "great video thanks", Likes: 900},
				{ID: "3", Author: "z", Text: "How do I install the hooks?", Likes: 10},
			},
			"v2": {
				{ID: "4", Author: "w", Text: "how do i deploy this to kubernetes cluster?", Likes: 5}, // duplicate
				{ID: "5", Author: "u", Text: "Can it run terraform pipelines offline?", Likes: 70},
			},
		},
		transcripts: map[string]*model.Transcript{
			"v1": {VideoID: "v1", Text: "today we install the hooks and write the config for claude"},
			"v2": {VideoID: "v2", Segments: []model.Segment{{StartS: 0, Text: "welcome back"}, {StartS: 12, Text: "we configure hooks"}, {StartS: 45, Text: "later part"}}},
		},
	}
}

func TestFindGaps(t *testing.T) {
	f := gapFixture()
	ms, warns := ReadTop(context.Background(), f, f.videos, "")
	if len(warns) != 1 || !strings.Contains(warns[0], "transcript of v3") {
		t.Fatalf("a missing transcript must be one warning, got %v", warns)
	}
	res := FindGaps(ms)
	if len(res.Gaps) != 2 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	if res.Gaps[0].Likes != 70 || res.Gaps[1].Likes != 50 {
		t.Fatalf("gaps not ranked by likes: %+v", res.Gaps)
	}
	if !strings.Contains(strings.Join(res.Gaps[1].Missing, " "), "kubernetes") {
		t.Fatalf("missing terms = %v", res.Gaps[1].Missing)
	}
	if len(res.Covered) != 1 || !strings.Contains(res.Covered[0].Text, "hooks") {
		t.Fatalf("covered = %+v", res.Covered)
	}
}

func TestFindGapsNoTranscripts(t *testing.T) {
	ms := []Material{{Questions: []model.Comment{{Text: "How does this work with kubernetes?"}}}}
	if res := FindGaps(ms); len(res.Gaps) != 0 {
		t.Fatalf("nothing can be judged without a transcript, got %+v", res.Gaps)
	}
}

func noteFixture() *fake {
	f := gapFixture()
	f.videos[0].Published = time.Now().AddDate(0, 0, -40)
	f.channels = map[string]*model.Channel{
		"A": {ID: "A", Name: "Chan A", URL: "https://www.youtube.com/channel/A", Subscribers: 12500, Recent: recent(100, 150, 200, 250, 300)},
		"B": {ID: "B", Name: "Chan | B", URL: "https://www.youtube.com/channel/B", Subscribers: 1_200_000, Recent: recent(500, 750, 1000, 1250, 1500)},
	}
	f.suggest = map[string][]string{
		"claude code":     {"claude code tutorial", "claude code install"},
		"how claude code": {"how claude code works", "claude code install"},
	}
	f.transcripts["v1"].Segments = []model.Segment{{StartS: 0, Text: "Hey everyone,"}, {StartS: 5, Text: "today we install the hooks"}, {StartS: 40, Text: "not in the intro"}}
	return f
}

func gather(t *testing.T, f *fake, topic string) *Note {
	t.Helper()
	now = func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	n, _, err := Gather(context.Background(), f, yt.SearchOpts{Query: topic, Limit: 20, Region: "US"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n.Videos {
		n.Videos[i].AgeDays = 40
		n.Videos[i].Published = time.Time{}
	}
	for i := range n.Outliers {
		n.Outliers[i].AgeDays = 40
	}
	return n
}

func TestNoteSectionsAndGolden(t *testing.T) {
	n := gather(t, noteFixture(), "claude code")
	md := n.Markdown()
	for _, h := range []string{"## Demand", "## Top videos", "## Outliers", "## Channels in this space", "## What viewers ask", "## How the top videos open", "## Sources"} {
		if !strings.Contains(md, h) {
			t.Fatalf("missing %q", h)
		}
	}
	if !strings.HasPrefix(md, "---\ntopic: \"claude code\"\ndate: 2026-11-12\n") || !strings.Contains(md, "tags: [research, youtube]\n---") {
		t.Fatalf("front matter wrong:\n%s", md[:200])
	}
	if strings.Contains(md, "not in the intro") {
		t.Fatal("opening must stop at 30 seconds")
	}
	if strings.Count(md, "claude code install") != 1 {
		t.Fatal("demand not deduplicated")
	}
	if !strings.Contains(md, `Chan \| B`) {
		t.Fatal("pipe in a table cell not escaped")
	}
	golden := filepath.Join("..", "..", "testdata", "research", "note.golden.md")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != md {
		t.Fatalf("note differs from golden (run go test -update):\n%s", md)
	}
	c := n.Counts()
	if c["top_videos"] != 3 || c["openings"] != 2 || c["sources"] == 0 {
		t.Fatalf("counts = %v", c)
	}
}

func TestSlugArabic(t *testing.T) {
	if got := Slug("كيف أتعلم البرمجة؟"); got != "كيف-أتعلم-البرمجة" {
		t.Fatalf("slug = %q", got)
	}
	if got := Slug("Claude Code: Tutorial!"); got != "claude-code-tutorial" {
		t.Fatalf("slug = %q", got)
	}
	if Slug("???") != "research" {
		t.Fatal("empty slug")
	}
}

func TestArabicNote(t *testing.T) {
	n := gather(t, noteFixture(), "كيف أتعلم البرمجة")
	if md := n.Markdown(); !strings.Contains(md, `topic: "كيف أتعلم البرمجة"`) || !strings.Contains(md, "# كيف أتعلم البرمجة") {
		t.Fatal("arabic topic mangled")
	}
}

func TestWriteNewNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"t.md", "t-2.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := WriteNew(dir, "t", []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "t-3.md" {
		t.Fatalf("landed at %s", p)
	}
	for n, want := range map[string]string{"t.md": "old t.md", "t-2.md": "old t-2.md", "t-3.md": "new"} {
		if b, _ := os.ReadFile(filepath.Join(dir, n)); string(b) != want {
			t.Fatalf("%s = %q", n, b)
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 3 {
		t.Fatalf("temp file left behind: %d entries", len(es))
	}
}

func TestOnTopic(t *testing.T) {
	seed := []string{"claude", "code", "tutorial"}
	for s, want := range map[string]bool{
		"claude code tutorial pdf": true, "claude coding tutorial": true, "claude code": true,
		"claude rotation": false, "how to code 3d": false,
	} {
		if onTopic(seed, s) != want {
			t.Errorf("onTopic(%q) != %v", s, want)
		}
	}
	if onTopic([]string{"claude", "code"}, "claude hero guide") {
		t.Error("two-term seed may miss nothing")
	}
}

func TestDemandGroups(t *testing.T) {
	md := gather(t, noteFixture(), "claude code").Markdown()
	if !strings.Contains(md, "- **completions**: claude code tutorial; claude code install\n- **how**: how claude code works\n") {
		t.Fatalf("demand wrong:\n%s", md)
	}
	if strings.Contains(md, "gta") || strings.Contains(md, "3d") {
		t.Fatal("off-topic suggestion kept")
	}
}

func TestClip(t *testing.T) {
	long := strings.Repeat("word ", 100) + "\n\n tail"
	got := clip(long)
	if r := []rune(got); len(r) > 282 || !strings.HasSuffix(got, "…") || strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Fatalf("clip = %q", got)
	}
	if clip("short  one\n") != "short one" {
		t.Fatal("short text changed")
	}
}

// Gather through the real yt.Get wrapper, so parallel reads hit the trace
// (run with -race).
func TestGatherParallelTrace(t *testing.T) {
	t.Setenv("YTX_HOME", t.TempDir())
	t.Setenv("YTX_NO_KEYCHAIN", "1")
	f := noteFixture()
	yt.RegisterKeyless(func() yt.Reader { return f })
	r, err := yt.Get(yt.Options{Mode: yt.Keyless, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, tr := yt.WithTrace(context.Background())
	if _, _, err := Gather(ctx, r, yt.SearchOpts{Query: "claude code", Limit: 20}, 3); err != nil {
		t.Fatal(err)
	}
	if tr.Source() != "keyless" {
		t.Fatalf("source = %q", tr.Source())
	}
}

// aged makes uploads published `days` ago with the given views.
func aged(days int, views ...int64) []model.Video {
	var vs []model.Video
	for _, v := range views {
		vs = append(vs, model.Video{Views: v, Published: time.Now().Add(-time.Duration(days) * 24 * time.Hour)})
	}
	return vs
}

func TestScoreOutliersAllNew(t *testing.T) {
	f := &fake{channels: map[string]*model.Channel{
		"N": {ID: "N", Name: "Fresh Talks", Subscribers: 9, URL: "https://www.youtube.com/channel/N", Recent: aged(0, 100, 150, 152, 160, 170, 180)},
	}}
	rows, chans, warns := ScoreOutliers(context.Background(), f, []model.Video{vid("n1", "N", 1_000_000)}, 30)
	if len(rows) != 1 || rows[0].Outlier != 0 || !rows[0].TooNew || rows[0].ChannelMedian != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if len(chans) != 1 || !chans[0].TooNew || chans[0].Median != 0 {
		t.Fatalf("chans = %+v", chans)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "Fresh Talks") || !strings.Contains(warns[0], "too new") {
		t.Fatalf("warnings = %v", warns)
	}
	n := Note{Videos: rows, Channels: chans}
	out := n.Markdown()
	if !strings.Contains(out, "| n/a |") || !strings.Contains(out, "too new") || strings.Contains(out, "x |") {
		t.Fatalf("note lacks n/a or too new:\n%s", out)
	}
}

func TestScoreOutliersMixedAges(t *testing.T) {
	recent := append(aged(0, 1, 2, 3, 4), aged(20, 100, 200, 300, 400, 500)...)
	f := &fake{channels: map[string]*model.Channel{"M": {ID: "M", Name: "Mixed", Recent: recent}}}
	rows, _, warns := ScoreOutliers(context.Background(), f, []model.Video{vid("m1", "M", 900)}, 30)
	if len(warns) != 0 || rows[0].TooNew || rows[0].ChannelMedian != 300 || rows[0].Outlier != 3 {
		t.Fatalf("rows = %+v warns = %v", rows, warns)
	}
}
