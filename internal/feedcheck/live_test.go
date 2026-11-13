//go:build live

package feedcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/morethancoder/ytx/internal/keyless" // the real keyless reader
	"github.com/morethancoder/ytx/internal/ops"
)

// TestLiveFeedcheck runs without agd on a real topic: it must find five
// competitors and download their thumbnails.
func TestLiveFeedcheck(t *testing.T) {
	t.Setenv("YTX_HOME", t.TempDir())
	t.Setenv("YTX_NO_KEYCHAIN", "1")
	t.Setenv("YOUTUBE_API_KEY", "")
	t.Setenv("YTX_AGD", "")
	t.Setenv("PATH", t.TempDir()) // no agd on PATH
	thumb := filepath.Join(t.TempDir(), "mine.png")
	writePNG(t, thumb, 1280, 720)
	in := ops.Input{Op: ops.Find("feedcheck"), Source: "keyless", NoCache: true, Given: map[string]bool{},
		Values: map[string]any{"topic": "how to make espresso at home", "thumb": []string{thumb}, "title": []string{"Espresso at home"},
			"out": t.TempDir(), "position": 2, "theme": "light", "region": "US"}}
	res, err := run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Data.(*Output)
	if len(out.Competitors) != 5 {
		t.Fatalf("%d competitors", len(out.Competitors))
	}
	for _, c := range out.Competitors {
		st, err := os.Stat(c.Thumb)
		if err != nil || st.Size() < 1000 {
			t.Errorf("thumbnail of %q: %v", c.Title, err)
		}
		t.Logf("%s | %s | %s | %s | %s", c.Title, c.Channel, c.Views, c.Age, c.Duration)
	}
	t.Logf("warnings: %v", res.Warnings)
}
