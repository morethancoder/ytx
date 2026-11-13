//go:build live

package download

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// "Me at the zoo" is 19 seconds long, so the download is small.
func TestLiveDownload(t *testing.T) {
	dir, err := os.MkdirTemp("", "ytx-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	tool := &Tool{}

	res, err := tool.Download(ctx, "jNQXAC9IVRw", Options{Height: 360}, dir)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(Data)
	t.Logf("video: %+v", d)
	if d.Bytes <= 0 || d.Height <= 0 || d.Height > 360 || d.DurationS < 15 || !strings.HasSuffix(d.Path, ".mp4") {
		t.Fatalf("video result %+v", d)
	}

	res, err = tool.Download(ctx, "jNQXAC9IVRw", Options{Audio: true}, dir)
	if err != nil {
		t.Fatal(err)
	}
	d = res.Data.(Data)
	t.Logf("audio: %+v", d)
	if d.Bytes <= 0 || d.AudioCodec == "" || d.AudioCodec == "none" {
		t.Fatalf("audio result %+v", d)
	}

	// A second run must keep the file and warn.
	res, err = tool.Download(ctx, "jNQXAC9IVRw", Options{Height: 360}, dir)
	if err != nil || len(res.Warnings) == 0 {
		t.Fatalf("repeat: %v %v", err, res)
	}
}
