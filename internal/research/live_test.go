//go:build live

package research

import (
	"context"
	"os"
	"strings"
	"testing"

	// The keyless reader registers itself in init(); the binary imports it in main.go.
	_ "github.com/morethancoder/ytx/internal/keyless"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/yt"
)

func liveInput(op string, vals map[string]any) ops.Input {
	return ops.Input{Op: ops.Find(op), Values: vals, Given: map[string]bool{}, Source: "keyless"}
}

func TestLiveOutliers(t *testing.T) {
	ctx, _ := yt.WithTrace(context.Background())
	res, err := outliersOp(ctx, liveInput("research.outliers", map[string]any{"query": "claude code tutorial", "limit": 8, "recent": 10}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Data.(map[string]any)
	if len(m["videos"].([]OutlierRow)) == 0 {
		t.Fatal("no videos")
	}
}

func TestLiveResearch(t *testing.T) {
	dir := t.TempDir()
	ctx, _ := yt.WithTrace(context.Background())
	res, err := runOp(ctx, liveInput("research.run", map[string]any{"query": "claude code tutorial", "out": dir, "top": 2}))
	if err != nil {
		t.Fatal(err)
	}
	path := res.Data.(map[string]any)["path"].(string)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"## Demand", "## Top videos", "## Sources"} {
		if !strings.Contains(string(b), h) {
			t.Fatalf("missing %s", h)
		}
	}
}
