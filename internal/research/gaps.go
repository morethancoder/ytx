package research

import (
	"context"
	"sort"
	"strings"

	"github.com/morethancoder/ytx/internal/analyze"
	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/reads"
	"github.com/morethancoder/ytx/internal/yt"
)

// Material is what was read from one top video.
type Material struct {
	Video      model.Video
	Questions  []model.Comment
	Transcript *model.Transcript // nil when the video has none
}

// Question is a viewer question and what the transcripts say about it.
type Question struct {
	Text      string   `json:"text"`
	Likes     int64    `json:"likes"`
	VideoID   string   `json:"video_id"`
	Missing   []string `json:"missing_terms,omitempty"`
	Terms     []string `json:"-"`
	Author    string   `json:"author,omitempty"`
	VideoName string   `json:"-"`
}

// GapResult splits the questions into unanswered (gaps) and answered (covered).
type GapResult struct {
	Gaps    []Question `json:"gaps"`
	Covered []Question `json:"covered"`
}

// coveredKeep keeps the covered list short: it is context, not the point.
const coveredKeep = 5

// ReadTop reads the top comments (questions only) and the transcript of each video.
// A missing transcript or closed comments are warnings, never failures.
func ReadTop(ctx context.Context, r yt.Reader, vs []model.Video, lang string) ([]Material, []string) {
	ms := make([]Material, len(vs))
	warns := make([][]string, len(vs))
	forEach(len(vs), func(i int) {
		v := vs[i]
		ms[i].Video = v
		cs, err := r.Comments(ctx, v.ID, yt.CommentOpts{Limit: 100, Order: "top"})
		if err != nil {
			warns[i] = append(warns[i], "comments of "+v.ID+": "+err.Error())
		}
		for _, c := range cs {
			if analyze.IsQuestion(c.Text) {
				c.IsQuestion = true
				ms[i].Questions = append(ms[i].Questions, c)
			}
		}
		t, err := r.Transcript(ctx, v.ID, lang)
		if err != nil || t == nil {
			warns[i] = append(warns[i], "transcript of "+v.ID+": "+errString(err))
			return
		}
		if t.Text == "" {
			parts := make([]string, len(t.Segments))
			for k, s := range t.Segments {
				parts[k] = s.Text
			}
			t.Text = strings.Join(parts, " ")
		}
		ms[i].Transcript = t
	})
	var all []string
	for _, w := range warns {
		all = append(all, w...)
	}
	return ms, all
}

// FindGaps applies the rule: a question is a gap when most of its key terms
// appear in none of the transcripts. With no transcript at all nothing can be
// judged, so nothing is called a gap.
func FindGaps(ms []Material) GapResult {
	covered := map[string]bool{}
	haveTranscript := false
	for _, m := range ms {
		if m.Transcript != nil {
			haveTranscript = true
			analyze.Merge(covered, analyze.TermSet(m.Transcript.Text))
		}
	}
	var res GapResult
	if !haveTranscript {
		return res
	}
	seen := map[string]bool{}
	for _, m := range ms {
		for _, c := range m.Questions {
			key := strings.ToLower(strings.Join(strings.Fields(c.Text), " "))
			if seen[key] {
				continue
			}
			seen[key] = true
			terms := analyze.Terms(c.Text)
			if len(terms) == 0 {
				continue
			}
			miss := analyze.Missing(terms, covered)
			q := Question{Text: strings.Join(strings.Fields(c.Text), " "), Likes: c.Likes, VideoID: m.Video.ID, Missing: miss, Terms: terms, Author: c.Author}
			if analyze.MostlyMissing(terms, miss) {
				res.Gaps = append(res.Gaps, q)
			} else {
				q.Missing = nil
				res.Covered = append(res.Covered, q)
			}
		}
	}
	byLikes := func(qs []Question) {
		sort.SliceStable(qs, func(i, j int) bool { return qs[i].Likes > qs[j].Likes })
	}
	byLikes(res.Gaps)
	byLikes(res.Covered)
	if len(res.Covered) > coveredKeep {
		res.Covered = res.Covered[:coveredKeep]
	}
	return res
}

func gapsOp(ctx context.Context, in ops.Input) (*ops.Result, error) {
	o, err := reads.SearchOpts(in)
	if err != nil {
		return nil, err
	}
	top := in.Int("top", 5)
	if top < 1 || top > 20 {
		return nil, apperr.New(apperr.InvalidInput, "--top must be 1 to 20")
	}
	o.Limit = top
	r, err := reads.Reader(in)
	if err != nil {
		return nil, err
	}
	vs, err := r.Search(ctx, o)
	if err != nil {
		return nil, err
	}
	if len(vs) > top {
		vs = vs[:top]
	}
	ms, warnings := ReadTop(ctx, r, vs, o.Lang)
	res := FindGaps(ms)
	if res.Gaps == nil {
		res.Gaps = []Question{}
	}
	if res.Covered == nil {
		res.Covered = []Question{}
	}
	return &ops.Result{Data: map[string]any{"query": o.Query, "videos": len(vs), "gaps": res.Gaps, "covered": res.Covered}, Warnings: warnings}, nil
}
