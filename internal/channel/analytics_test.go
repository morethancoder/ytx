package channel

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morethancoder/ytx/internal/apperr"
)

func TestMeChannel(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := meChannel(context.Background(), input("me.channel", false, map[string]any{"days": 28}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(map[string]any)
	if d["views"] != int64(181500) || d["subscribers_net"] != int64(1145) || d["average_view_duration_s"] != int64(178) {
		t.Fatalf("data %v", d)
	}
	start, end := period(28)
	for _, c := range f.seen() {
		if c.Path == "/v2/reports" {
			q, _ := url.ParseQuery(c.Query)
			if q.Get("ids") != "channel==MINE" || q.Get("startDate") != start || q.Get("endDate") != end {
				t.Errorf("query %v", q)
			}
		}
	}
	if res.Meta.QuotaUsed != 0 {
		t.Errorf("analytics spent %d Data API units", res.Meta.QuotaUsed)
	}
}

func TestMeVideos(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := meVideos(context.Background(), input("me.videos", false, map[string]any{"days": 90, "limit": 50}))
	if err != nil {
		t.Fatal(err)
	}
	vs := res.Data.([]videoStats)
	if len(vs) != 6 || vs[0].ID != "vid00000001" || vs[0].Title != "How I edit videos fast" || vs[0].DurationS != 250 || vs[0].AvgViewPct != 66.8 {
		t.Fatalf("videos %+v", vs)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "impressions") {
		t.Errorf("missing the impressions warning: %v", res.Warnings)
	}
	if res.Meta.QuotaUsed != 1 {
		t.Errorf("quota %d, want 1 (one videos.list)", res.Meta.QuotaUsed)
	}
}

func TestMeRetention(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := meRetention(context.Background(), input("me.retention", false, map[string]any{"video": "vid00000003"}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(map[string]any)
	pts := d["points"].([]retentionPoint)
	// The fixture curve is 1 - ratio/2; the video is 360 s, so 30 s is ratio 1/12.
	if len(pts) != 21 || pts[2].AtS != 36 {
		t.Fatalf("points %+v", pts[:3])
	}
	if at := d["at_30s"].(float64); at < 0.958 || at > 0.959 {
		t.Errorf("at_30s %v", at)
	}
	if d["at"].(map[string]any)["50%"] != 0.75 {
		t.Errorf("marks %v", d["at"])
	}
}

func TestMineVideos(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := mineVideos(context.Background(), input("mine.videos", false, map[string]any{"limit": 50}))
	if err != nil {
		t.Fatal(err)
	}
	vs := res.Data.([]myVideo)
	if len(vs) != 6 || vs[5].Privacy != "unlisted" || vs[0].Views != 90000 || vs[0].DurationS != 250 {
		t.Fatalf("videos %+v", vs)
	}
	if res.Meta.QuotaUsed != 3 {
		t.Errorf("quota %d, want 3", res.Meta.QuotaUsed)
	}
}

func TestMeLearnNeverOverwrites(t *testing.T) {
	f := setup(t)
	f.signIn()
	dir := t.TempDir()
	_, end := period(1)
	first := filepath.Join(dir, "channel-learnings-"+end+".md")
	if err := os.WriteFile(first, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 0; i < 2; i++ {
		res, err := meLearn(context.Background(), input("me.learn", false, map[string]any{"out": dir, "days": 365}))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, res.Data.(map[string]any)["path"].(string))
	}
	want := []string{filepath.Join(dir, "channel-learnings-"+end+"-2.md"), filepath.Join(dir, "channel-learnings-"+end+"-3.md")}
	if paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths %v, want %v", paths, want)
	}
	if b, _ := os.ReadFile(first); string(b) != "keep me" {
		t.Fatal("the existing note was overwritten")
	}
	b, _ := os.ReadFile(paths[0])
	note := string(b)
	for _, s := range []string{"---\ntitle: \"Channel learnings " + end + "\"", "videos: 6\n", "## Kept viewers best", "## Observations",
		"Quick tip: audio", "median length of", "(r = ", "At 30 seconds", "not in the Analytics API"} {
		if !strings.Contains(note, s) {
			t.Errorf("note lacks %q", s)
		}
	}
	if !strings.HasPrefix(note, "---\n") {
		t.Error("note has no front matter")
	}
}

func TestMeLearnUsesResearchDir(t *testing.T) {
	f := setup(t)
	f.signIn()
	dir := filepath.Join(t.TempDir(), "notes")
	if err := os.WriteFile(filepath.Join(os.Getenv("YTX_HOME"), "config.json"), []byte(`{"research_dir":"`+dir+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := meLearn(context.Background(), input("me.learn", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if p := res.Data.(map[string]any)["path"].(string); filepath.Dir(p) != dir {
		t.Fatalf("wrote %s, want inside %s", p, dir)
	}
}

// An Analytics quotaExceeded leaves the Data API pool alone, so reads keep
// using the official API.
func TestAnalyticsQuotaLeavesPool(t *testing.T) {
	f := setup(t)
	f.signIn()
	f.on("GET /v2/reports", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write(fixture(t, "error_quota.json"))
	})
	_, err := meChannel(context.Background(), input("me.channel", false, nil))
	if codeOf(err) != apperr.QuotaExceeded {
		t.Fatalf("got %v", err)
	}
	if f.pool.Left() != 10000 {
		t.Fatalf("Data API pool left %d, want untouched 10000", f.pool.Left())
	}
}
