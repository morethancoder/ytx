// Package reads attaches the public read commands (search, video, channel,
// comments, transcript, suggest, trending, playlist) to the ops table. They
// all go through yt.Get, so they run keyless or on the API with the same output.
package reads

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/analyze"
	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/yt"
)

func init() {
	ops.Handle("search", search)
	ops.Handle("video.get", videoGet)
	ops.Handle("channel.get", channelGet)
	ops.Handle("comments.list", commentsList)
	ops.Handle("transcript.get", transcriptGet)
	ops.Handle("suggest", suggest)
	ops.Handle("trending", trending)
	ops.Handle("playlist.get", playlistGet)
}

// Reader returns the reader for a call (exported for research and other areas).
func Reader(in ops.Input) (yt.Reader, error) {
	return yt.Get(yt.Options{Mode: yt.Mode(in.Source), NoCache: in.NoCache})
}

// Defaults fills lang and region from the config when the caller left them out.
func Defaults(in ops.Input) (lang, region string) {
	lang, region = in.Str("lang"), in.Str("region")
	cfg, _ := config.Load()
	if cfg != nil {
		if lang == "" {
			lang = cfg.Lang
		}
		if !in.Given["region"] && cfg.Region != "" {
			region = cfg.Region
		}
	}
	return lang, region
}

// SearchOpts builds yt.SearchOpts from an input.
func SearchOpts(in ops.Input) (yt.SearchOpts, error) {
	lang, region := Defaults(in)
	o := yt.SearchOpts{Query: strings.TrimSpace(in.Str("query")), Limit: in.Int("limit", 20), Type: in.Str("type"),
		Order: in.Str("order"), SinceDays: in.Int("since", 0), Lang: lang, Region: region}
	if o.Query == "" {
		return o, apperr.New(apperr.InvalidInput, "empty query")
	}
	if o.Type == "" {
		o.Type = "video"
	}
	if o.Order == "" {
		o.Order = "relevance"
	}
	switch o.Type {
	case "video", "short", "any":
	default:
		return o, apperr.New(apperr.InvalidInput, "--type must be video, short or any")
	}
	switch o.Order {
	case "relevance", "views", "date":
	default:
		return o, apperr.New(apperr.InvalidInput, "--order must be relevance, views or date")
	}
	if o.Limit < 1 || o.Limit > 500 {
		return o, apperr.New(apperr.InvalidInput, "--limit must be 1 to 500")
	}
	return o, nil
}

// FillAge sets AgeDays from Published.
func FillAge(vs []model.Video) {
	now := time.Now()
	for i := range vs {
		if !vs[i].Published.IsZero() {
			vs[i].AgeDays = int(now.Sub(vs[i].Published).Hours() / 24)
		}
	}
}

func search(ctx context.Context, in ops.Input) (*ops.Result, error) {
	o, err := SearchOpts(in)
	if err != nil {
		return nil, err
	}
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	vs, err := r.Search(ctx, o)
	if err != nil {
		return nil, err
	}
	FillAge(vs)
	if len(vs) > o.Limit {
		vs = vs[:o.Limit]
	}
	return &ops.Result{Data: vs}, nil
}

func videoGet(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	v, err := r.Video(ctx, id)
	if err != nil {
		return nil, err
	}
	vs := []model.Video{*v}
	FillAge(vs)
	return &ops.Result{Data: vs[0]}, nil
}

func channelGet(ctx context.Context, in ops.Input) (*ops.Result, error) {
	ref, err := yt.ChannelRef(in.Str("channel"))
	if err != nil {
		return nil, err
	}
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	c, err := r.Channel(ctx, ref, yt.ChannelOpts{Limit: in.Int("limit", 30), Shorts: in.Bool("shorts")})
	if err != nil {
		return nil, err
	}
	FillAge(c.Recent)
	c.MedianViews = analyze.Median(c.Recent)
	for i := range c.Recent {
		c.Recent[i].Outlier = analyze.Outlier(c.Recent[i].Views, c.MedianViews)
	}
	return &ops.Result{Data: c}, nil
}

func commentsList(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	order := in.Str("order")
	if order != "top" && order != "new" {
		return nil, apperr.New(apperr.InvalidInput, "--order must be top or new")
	}
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	cs, err := r.Comments(ctx, id, yt.CommentOpts{Limit: in.Int("limit", 100), Order: order})
	if err != nil {
		return nil, err
	}
	out := cs[:0]
	for _, c := range cs {
		c.IsQuestion = analyze.IsQuestion(c.Text)
		if in.Bool("questions") && !c.IsQuestion {
			continue
		}
		out = append(out, c)
	}
	if order == "top" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Likes > out[j].Likes })
	}
	return &ops.Result{Data: out}, nil
}

func transcriptGet(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	t, err := r.Transcript(ctx, id, in.Str("lang"))
	if err != nil {
		return nil, err
	}
	if t.Text == "" {
		parts := make([]string, len(t.Segments))
		for i, s := range t.Segments {
			parts[i] = s.Text
		}
		t.Text = strings.Join(parts, " ")
	}
	t.Words = len(strings.Fields(t.Text))
	if in.Bool("text") {
		t.Segments = nil
	}
	return &ops.Result{Data: t}, nil
}

// expansions are what --expand appends to the phrase (a..z plus question words).
func expansions(q string) []string {
	out := []string{q}
	for _, w := range []string{"how", "why", "what", "best", "vs", "for"} {
		out = append(out, w+" "+q)
	}
	for c := 'a'; c <= 'z'; c++ {
		out = append(out, q+" "+string(c))
	}
	return out
}

func suggest(ctx context.Context, in ops.Input) (*ops.Result, error) {
	q := strings.TrimSpace(in.Str("query"))
	if q == "" {
		return nil, apperr.New(apperr.InvalidInput, "empty query")
	}
	lang, region := Defaults(in)
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	phrases := []string{q}
	if in.Bool("expand") {
		phrases = expansions(q)
	}
	seen := map[string]bool{}
	var out []string
	var warnings []string
	for _, p := range phrases {
		ss, err := r.Suggest(ctx, p, lang, region)
		if err != nil {
			if p == q {
				return nil, err
			}
			warnings = append(warnings, p+": "+err.Error())
			continue
		}
		for _, s := range ss {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return &ops.Result{Data: out, Warnings: warnings}, nil
}

func trending(ctx context.Context, in ops.Input) (*ops.Result, error) {
	_, region := Defaults(in)
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	vs, err := r.Trending(ctx, region, in.Str("category"), in.Int("limit", 20))
	if err != nil {
		return nil, err
	}
	FillAge(vs)
	return &ops.Result{Data: vs}, nil
}

func playlistGet(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.PlaylistID(in.Str("playlist"))
	if err != nil {
		return nil, err
	}
	r, err := Reader(in)
	if err != nil {
		return nil, err
	}
	p, vs, err := r.Playlist(ctx, id, in.Int("limit", 50))
	if err != nil {
		return nil, err
	}
	FillAge(vs)
	return &ops.Result{Data: map[string]any{"playlist": p, "videos": vs}}, nil
}
