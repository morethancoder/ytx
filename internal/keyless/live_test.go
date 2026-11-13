//go:build live

package keyless

import (
	"context"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/yt"
)

// Well-known targets that have existed for years: Rick Astley's video and channel.
const (
	liveVideo    = "dQw4w9WgXcQ"
	liveChannel  = "@RickAstleyYT"
	livePlaylist = "PLRAV69dS1uWQGDQoBYMZWKjzuhCaOnBpa"
	liveNoComm   = "e_04ZrNroTo" // comments are turned off
)

func liveCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLiveSearch(t *testing.T) {
	c := New()
	vs, err := c.Search(liveCtx(t), yt.SearchOpts{Query: "go tutorial", Limit: 30, Type: "video", Order: "relevance", Region: "US"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) < 25 {
		t.Fatalf("want at least 25 results across pages, got %d", len(vs))
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if seen[v.ID] {
			t.Errorf("duplicate id %s", v.ID)
		}
		seen[v.ID] = true
		if v.ID == "" || v.Title == "" || v.URL == "" || v.IsShort {
			t.Errorf("bad video %+v", v)
		}
	}
	first := vs[0]
	if first.Views <= 0 || first.DurationS <= 0 || first.ChannelName == "" || first.Thumbnail == "" || first.PublishedText == "" || first.Published.IsZero() {
		t.Errorf("first result is missing fields: %+v", first)
	}
}

func TestLiveSearchOrderAndFilters(t *testing.T) {
	c := New()
	vs, err := c.Search(liveCtx(t), yt.SearchOpts{Query: "minecraft", Limit: 10, Type: "video", Order: "views", SinceDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) == 0 {
		t.Fatal("no results for the last 7 days")
	}
	for _, v := range vs {
		if age := time.Since(v.Published); age > 9*24*time.Hour {
			t.Errorf("%s is %v old, outside the 7 day window (%q)", v.ID, age, v.PublishedText)
		}
	}
	shorts, err := c.Search(liveCtx(t), yt.SearchOpts{Query: "go tutorial", Limit: 10, Type: "short"})
	if err != nil {
		t.Fatal(err)
	}
	if len(shorts) == 0 {
		t.Fatal("no shorts")
	}
	for _, v := range shorts {
		if !v.IsShort || v.URL != yt.VideoURL(v.ID, true) {
			t.Errorf("not a short: %+v", v)
		}
	}
	dated, err := c.Search(liveCtx(t), yt.SearchOpts{Query: "golang", Limit: 15, Type: "video", Order: "date"})
	if err != nil || len(dated) < 2 {
		t.Fatalf("date search: %v %d", err, len(dated))
	}
	for i := 1; i < len(dated); i++ {
		if dated[i].Published.After(dated[i-1].Published) {
			t.Errorf("date order broken at %d", i)
		}
	}
}

func TestLiveVideo(t *testing.T) {
	v, err := New().Video(liveCtx(t), liveVideo)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != liveVideo || v.Title == "" || v.Views < 1_000_000_000 || v.Likes <= 0 || v.DurationS <= 0 ||
		v.ChannelID == "" || v.ChannelName == "" || v.Description == "" || len(v.Tags) == 0 || v.Thumbnail == "" {
		t.Errorf("video is missing fields: %+v", v)
	}
	if v.Published.Year() != 2009 {
		t.Errorf("published = %v, want 2009", v.Published)
	}
	if v.Comments <= 0 {
		t.Errorf("comment count missing: %d", v.Comments)
	}
}

func TestLiveVideoNotFound(t *testing.T) {
	_, err := New().Video(liveCtx(t), "aaaaaaaaaaa")
	if !isAppErr(err, apperr.NotFound) {
		t.Fatalf("want not_found, got %v", err)
	}
}

func TestLiveChannel(t *testing.T) {
	c := New()
	ch, err := c.Channel(liveCtx(t), liveChannel, yt.ChannelOpts{Limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "UCuAXFkgsw1L7xaCfnd5JJOw" || ch.Name != "Rick Astley" || ch.Handle != "@RickAstleyYT" ||
		ch.Subscribers < 1_000_000 || ch.VideoCount <= 0 || ch.Thumbnail == "" || ch.Description == "" {
		t.Errorf("channel: %+v", ch)
	}
	if len(ch.Recent) < 30 {
		t.Fatalf("want 30+ recent videos across a continuation, got %d", len(ch.Recent))
	}
	if r := ch.Recent[0]; r.ID == "" || r.Title == "" || r.DurationS <= 0 || r.ChannelName == "" {
		t.Errorf("recent[0]: %+v", r)
	}
	shorts, err := c.Channel(liveCtx(t), ch.ID, yt.ChannelOpts{Limit: 10, Shorts: true})
	if err != nil || len(shorts.Recent) == 0 || !shorts.Recent[0].IsShort {
		t.Fatalf("shorts: %v %+v", err, shorts)
	}
	if _, err := c.Channel(liveCtx(t), "@zzzzzzqqqqqxxxxx1234", yt.ChannelOpts{}); !isAppErr(err, apperr.NotFound) {
		t.Errorf("unknown handle: want not_found, got %v", err)
	}
}

func TestLiveComments(t *testing.T) {
	c := New()
	for _, order := range []string{"top", "new"} {
		cs, err := c.Comments(liveCtx(t), liveVideo, yt.CommentOpts{Limit: 25, Order: order})
		if err != nil {
			t.Fatalf("%s: %v", order, err)
		}
		if len(cs) != 25 {
			t.Fatalf("%s: want 25 comments across pages, got %d", order, len(cs))
		}
		empty := 0
		for _, cm := range cs {
			if cm.ID == "" || cm.Author == "" || cm.When == "" {
				t.Errorf("%s: bad comment %+v", order, cm)
			}
			if cm.Text == "" {
				empty++ // a sticker or image-only comment has no text
			}
		}
		if empty > 5 {
			t.Errorf("%s: %d of 25 comments have no text", order, empty)
		}
		if order == "top" && cs[0].Likes <= 0 {
			t.Errorf("top comment has no likes: %+v", cs[0])
		}
	}
	if _, err := c.Comments(liveCtx(t), liveNoComm, yt.CommentOpts{Limit: 5}); !isAppErr(err, apperr.CommentsDisabled) {
		t.Errorf("want comments_disabled, got %v", err)
	}
}

func TestLiveTranscript(t *testing.T) {
	c := New()
	c.YTDLP = "off" // prove the keyless path works on its own
	tr, err := c.Transcript(liveCtx(t), liveVideo, "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language == "" || len(tr.Segments) < 20 || tr.Words < 100 || tr.Text == "" || tr.VideoID != liveVideo {
		t.Errorf("transcript: lang=%q segs=%d words=%d", tr.Language, len(tr.Segments), tr.Words)
	}
	// A video with auto-dubbed audio must still default to its original language.
	dub, err := c.Transcript(liveCtx(t), "WdZjoMi6W7Y", "")
	if err != nil || dub.Language != "en" {
		t.Errorf("dubbed video default language: %v %+v", err, dub)
	}
	de, err := c.Transcript(liveCtx(t), liveVideo, "de")
	if err != nil || de.Language != "de-DE" {
		t.Errorf("german: %v %+v", err, de)
	}
	if _, err := c.Transcript(liveCtx(t), "aaaaaaaaaaa", ""); !isAppErr(err, apperr.NotFound) {
		t.Errorf("missing video: want not_found, got %v", err)
	}
}

func TestLiveSuggest(t *testing.T) {
	ss, err := New().Suggest(liveCtx(t), "go tut", "en", "US")
	if err != nil || len(ss) < 3 {
		t.Fatalf("suggest: %v %v", err, ss)
	}
	fr, err := New().Suggest(liveCtx(t), "café", "fr", "FR")
	if err != nil || len(fr) == 0 {
		t.Fatalf("suggest fr: %v %v", err, fr)
	}
}

func TestLiveTrending(t *testing.T) {
	c := New()
	for _, cat := range []string{"now", "music", "gaming", "movies"} {
		vs, err := c.Trending(liveCtx(t), "US", cat, 10)
		if err != nil {
			t.Fatalf("%s: %v", cat, err)
		}
		if len(vs) < 5 {
			t.Fatalf("%s: want 5+ videos, got %d", cat, len(vs))
		}
		for _, v := range vs {
			if v.ID == "" || v.Title == "" || v.Views <= 0 {
				t.Errorf("%s: bad video %+v", cat, v)
			}
		}
	}
	if _, err := c.Trending(liveCtx(t), "US", "nonsense", 5); !isAppErr(err, apperr.InvalidInput) {
		t.Errorf("want invalid_input, got %v", err)
	}
}

func TestLivePlaylist(t *testing.T) {
	pl, vs, err := New().Playlist(liveCtx(t), livePlaylist, 20)
	if err != nil {
		t.Fatal(err)
	}
	if pl.ID != livePlaylist || pl.Title == "" || pl.ItemCount < 20 || pl.URL == "" {
		t.Errorf("playlist: %+v", pl)
	}
	if len(vs) != 20 || vs[0].ID == "" || vs[0].Title == "" || vs[0].DurationS <= 0 {
		t.Errorf("videos: %d %+v", len(vs), vs[0])
	}
	if _, _, err := New().Playlist(liveCtx(t), "PLaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 5); !isAppErr(err, apperr.NotFound) {
		t.Errorf("unknown playlist: want not_found, got %v", err)
	}
}
