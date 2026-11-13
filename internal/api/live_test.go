//go:build live

package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/quota"
	"github.com/morethancoder/ytx/internal/yt"
)

func liveClient(t *testing.T) *Client {
	key := os.Getenv("YOUTUBE_API_KEY")
	if key == "" {
		t.Skip("YOUTUBE_API_KEY is not set")
	}
	return New(key, quota.NewAt(filepath.Join(t.TempDir(), "q.json"), 10000, time.Now))
}

func TestLiveVideo(t *testing.T) {
	v, err := liveClient(t).Video(context.Background(), "dQw4w9WgXcQ")
	if err != nil || v.Views == 0 || v.DurationS == 0 {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestLiveSearch(t *testing.T) {
	vs, err := liveClient(t).Search(context.Background(), yt.SearchOpts{Query: "golang", Limit: 3})
	if err != nil || len(vs) == 0 {
		t.Fatalf("%d %v", len(vs), err)
	}
}

func TestLiveChannel(t *testing.T) {
	ch, err := liveClient(t).Channel(context.Background(), "@YouTube", yt.ChannelOpts{Limit: 3})
	if err != nil || ch.Subscribers == 0 || len(ch.Recent) == 0 {
		t.Fatalf("%+v %v", ch, err)
	}
}

func TestLiveTrending(t *testing.T) {
	vs, err := liveClient(t).Trending(context.Background(), "US", "music", 3)
	if err != nil || len(vs) == 0 {
		t.Fatalf("%d %v", len(vs), err)
	}
}

func TestLiveComments(t *testing.T) {
	cs, err := liveClient(t).Comments(context.Background(), "dQw4w9WgXcQ", yt.CommentOpts{Limit: 3})
	if err != nil || len(cs) == 0 {
		t.Fatalf("%d %v", len(cs), err)
	}
}
