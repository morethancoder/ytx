package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/morethancoder/ytx/internal/analyze"
	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/reads"
	"github.com/morethancoder/ytx/internal/yt"
)

// now is swappable so the golden note has a fixed date.
var now = time.Now

// Row caps keep the note readable; the counts in the result say how many exist.
const (
	maxDemand   = 40
	maxGroup    = 8
	maxTop      = 15
	maxOutliers = 10
	maxChannels = 10
	maxAsk      = 10
	searchSize  = 20
	outlierMin  = 2.0
	openSeconds = 30
	openWords   = 80
)

// DemandGroup is the suggestions one prefix or question word produced.
type DemandGroup struct {
	Name  string
	Items []string
}

// Note is everything the Markdown is made from.
type Note struct {
	Topic    string
	Date     time.Time
	Params   map[string]any
	Demand   []DemandGroup
	Videos   []OutlierRow
	Outliers []OutlierRow
	Channels []ChannelRow
	Gaps     GapResult
	Material []Material
	Sources  []string
	// Source is where the reads came from (keyless, api, mixed, cache); "" before reading.
	Source string
}

// Slug keeps letters and digits of any script (Arabic included) and joins words with "-".
func Slug(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if r := []rune(out); len(r) > 60 {
		out = strings.Trim(string(r[:60]), "-")
	}
	if out == "" {
		out = "research"
	}
	return out
}

// WriteNew writes the note to dir/base.md, or base-2.md, base-3.md ... and never
// replaces a file. The body is written fully to a temp file first, then hard
// linked to the final name: Link fails when the name exists, so two runs on
// the same day can't overwrite each other.
func WriteNew(dir, base string, body []byte) (string, error) {
	base = strings.TrimSuffix(base, ".md")
	tmp, err := os.CreateTemp(dir, ".note-*.tmp")
	if err != nil {
		return "", apperr.New(apperr.InvalidInput, "can't write in %s: %v", dir, err)
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		return "", apperr.New(apperr.Internal, "couldn't finish writing the note in %s", dir)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", apperr.Wrap(apperr.Internal, err, "writing the note")
	}
	for i := 1; i < 1000; i++ {
		name := base + ".md"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.md", base, i)
		}
		p := filepath.Join(dir, name)
		err := os.Link(tmp.Name(), p)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", apperr.New(apperr.InvalidInput, "can't write %q: %v", p, err)
		}
		return p, nil
	}
	return "", apperr.New(apperr.Internal, "too many notes named %s in %s", base, dir)
}

func jstr(s string) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimSpace(b.String())
}

func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer("|", "\\|").Replace(s)
}

func num(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func age(v model.Video) string {
	d := v.AgeDays
	switch {
	case v.Published.IsZero() && v.PublishedText != "":
		return v.PublishedText
	case d >= 365:
		return fmt.Sprintf("%dy", d/365)
	case d >= 60:
		return fmt.Sprintf("%dmo", d/30)
	case d > 0:
		return fmt.Sprintf("%dd", d)
	}
	return "new"
}

func length(s int) string {
	if s <= 0 {
		return ""
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func link(title, u string) string {
	title = strings.NewReplacer("[", "(", "]", ")").Replace(cell(title))
	return "[" + title + "](" + u + ")"
}

func videoURL(v model.Video) string {
	if v.URL != "" {
		return v.URL
	}
	return "https://www.youtube.com/watch?v=" + v.ID
}

func oscore(o float64) string {
	if o <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1fx", o)
}

// Opening is the first ~30 seconds of a transcript, or its first words when it has no timing.
func Opening(t *model.Transcript) string {
	var parts []string
	for _, s := range t.Segments {
		if s.StartS >= openSeconds {
			break
		}
		parts = append(parts, s.Text)
	}
	text := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if text == "" {
		w := strings.Fields(t.Text)
		if len(w) > openWords {
			w = w[:openWords]
		}
		text = strings.Join(w, " ")
	}
	return text
}

// Counts is how many items each section holds in the note.
func (n *Note) Counts() map[string]int {
	openings := 0
	for _, m := range n.Material {
		if m.Transcript != nil {
			openings++
		}
	}
	demand := 0
	for _, g := range n.Demand {
		demand += len(g.Items)
	}
	return map[string]int{
		"demand": min(demand, maxDemand), "top_videos": min(len(n.Videos), maxTop), "outliers": min(len(n.Outliers), maxOutliers),
		"channels": min(len(n.Channels), maxChannels), "viewer_questions": min(len(n.Gaps.Gaps), maxAsk),
		"openings": openings, "sources": len(n.Sources),
	}
}

// Markdown renders the note: plain Markdown with YAML front matter, readable in any vault.
func (n *Note) Markdown() string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("---\ntopic: %s\ndate: %s\nsource: %s\nquery:\n", jstr(n.Topic), n.Date.Format("2006-01-02"), n.sourceLine())
	keys := make([]string, 0, len(n.Params))
	for k := range n.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := n.Params[k].(type) {
		case string:
			w("  %s: %s\n", k, jstr(v))
		default:
			w("  %s: %v\n", k, v)
		}
	}
	w("tags: [research, youtube]\n---\n\n# %s\n\n", n.Topic)

	w("## Demand\n\n")
	if len(n.Demand) == 0 {
		w("_No autocomplete data._\n\n")
	}
	left := maxDemand
	for _, g := range n.Demand {
		items := g.Items
		if left <= 0 {
			break
		}
		if len(items) > left {
			items = items[:left]
		}
		left -= len(items)
		w("- **%s**: %s\n", g.Name, strings.Join(items, "; "))
	}
	w("\n## Top videos\n\n")
	if len(n.Videos) == 0 {
		w("_No videos found._\n\n")
	} else {
		w("| Title | Channel | Views | Age | Length | Outlier |\n|---|---|---|---|---|---|\n")
		for _, v := range n.Videos[:min(len(n.Videos), maxTop)] {
			w("| %s | %s | %s | %s | %s | %s |\n", link(v.Title, videoURL(v.Video)), cell(v.ChannelName), num(v.Views), age(v.Video), length(v.DurationS), oscore(v.Outlier))
		}
		w("\n")
	}
	w("## Outliers\n\nVideos at %.0fx or more their channel's median views.\n\n", outlierMin)
	if len(n.Outliers) == 0 {
		w("_None at that level._\n\n")
	} else {
		w("| Title | Channel | Views | Channel median | Outlier |\n|---|---|---|---|---|\n")
		for _, v := range n.Outliers[:min(len(n.Outliers), maxOutliers)] {
			w("| %s | %s | %s | %s | %s |\n", link(v.Title, videoURL(v.Video)), cell(v.ChannelName), num(v.Views), num(v.ChannelMedian), oscore(v.Outlier))
		}
		w("\n")
	}
	w("## Channels in this space\n\n")
	if len(n.Channels) == 0 {
		w("_No channel data._\n\n")
	} else {
		w("| Channel | Subscribers | Median views |\n|---|---|---|\n")
		for _, c := range n.Channels[:min(len(n.Channels), maxChannels)] {
			w("| %s | %s | %s |\n", link(c.Name, c.URL), num(c.Subscribers), num(c.Median))
		}
		w("\n")
	}
	w("## What viewers ask\n\n")
	if len(n.Gaps.Gaps) == 0 {
		w("_No unanswered questions found._\n\n")
	}
	for _, q := range n.Gaps.Gaps[:min(len(n.Gaps.Gaps), maxAsk)] {
		w("- %s (%d likes; not covered: %s; video `%s`)\n", clip(q.Text), q.Likes, strings.Join(q.Missing, ", "), q.VideoID)
	}
	if len(n.Gaps.Covered) > 0 {
		w("\nAlready answered by the top videos:\n\n")
		for _, q := range n.Gaps.Covered {
			w("- %s (%d likes)\n", clip(q.Text), q.Likes)
		}
	}
	w("\n## How the top videos open\n\nThe first %d seconds of each transcript.\n\n", openSeconds)
	opened := 0
	for _, m := range n.Material {
		if m.Transcript == nil {
			continue
		}
		opened++
		w("### %s\n\n> %s\n\n", link(m.Video.Title, videoURL(m.Video)), Opening(m.Transcript))
	}
	if opened == 0 {
		w("_No transcripts available._\n\n")
	}
	w("## Sources\n\n")
	for _, s := range n.Sources {
		w("- %s\n", s)
	}
	return b.String()
}

func runOp(ctx context.Context, in ops.Input) (*ops.Result, error) {
	o, err := reads.SearchOpts(in)
	if err != nil {
		return nil, err
	}
	top := in.Int("top", 3)
	if top < 1 || top > 10 {
		return nil, apperr.New(apperr.InvalidInput, "--top must be 1 to 10")
	}
	o.Limit = searchSize
	r, err := reads.Reader(in)
	if err != nil {
		return nil, err
	}
	dir := in.Str("out")
	if dir == "" {
		if cfg, _ := config.Load(); cfg != nil {
			dir = cfg.ResearchDir
		}
	}
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "can't use the folder %s: %v", dir, err)
	}
	n, warnings, err := Gather(ctx, r, o, top)
	if err != nil {
		return nil, err
	}
	if t := yt.TraceFrom(ctx); t != nil {
		n.Source = t.Source()
	}
	name := strings.TrimSpace(in.Str("name"))
	if name == "" {
		name = Slug(n.Topic) + "-" + n.Date.Format("2006-01-02")
	} else {
		name = Slug(name)
	}
	path, err := WriteNew(dir, name, []byte(n.Markdown()))
	if err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return &ops.Result{Data: map[string]any{"path": path, "counts": n.Counts(), "warnings": warnings}, Warnings: warnings}, nil
}

// Gather does all the reading for a note. Only a failed search is an error;
// every other failure becomes a warning and its section is thinner.
func Gather(ctx context.Context, r yt.Reader, o yt.SearchOpts, top int) (*Note, []string, error) {
	vs, err := r.Search(ctx, o)
	if err != nil {
		return nil, nil, err
	}
	if len(vs) > o.Limit {
		vs = vs[:o.Limit]
	}
	reads.FillAge(vs)
	n := &Note{Topic: o.Query, Date: now(), Params: map[string]any{"top": top}}
	if o.Lang != "" {
		n.Params["lang"] = o.Lang
	}
	if o.Region != "" {
		n.Params["region"] = o.Region
	}
	var warnings []string

	// Demand: the seed's own completions first, then one group per question word or letter.
	// Expansions drift off topic, so a suggestion must keep the seed's key terms.
	names := []string{"completions"}
	phrases := []string{o.Query}
	for _, w := range []string{"how", "why", "what", "best", "vs", "for"} {
		names = append(names, w)
		phrases = append(phrases, w+" "+o.Query)
	}
	for c := 'a'; c <= 'z'; c++ {
		names = append(names, string(c))
		phrases = append(phrases, o.Query+" "+string(c))
	}
	sugg := make([][]string, len(phrases))
	serrs := make([]error, len(phrases))
	forEach(len(phrases), func(i int) { sugg[i], serrs[i] = r.Suggest(ctx, phrases[i], o.Lang, o.Region) })
	seedTerms := analyze.Terms(o.Query)
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(o.Query)): true}
	failed := 0
	for i, ss := range sugg {
		if serrs[i] != nil {
			failed++
			continue
		}
		var g []string
		for _, s := range ss {
			if k := strings.ToLower(strings.TrimSpace(s)); k != "" && !seen[k] && onTopic(seedTerms, s) {
				seen[k] = true
				g = append(g, s)
			}
		}
		if len(g) > maxGroup {
			g = g[:maxGroup]
		}
		if len(g) > 0 && !(len(g) == 1 && strings.HasPrefix(strings.ToLower(o.Query), strings.ToLower(g[0]))) {
			n.Demand = append(n.Demand, DemandGroup{Name: names[i], Items: g})
		}
	}
	if failed > 0 {
		warnings = append(warnings, fmt.Sprintf("suggest: %d of %d lookups failed", failed, len(phrases)))
	}

	rows, channels, w := ScoreOutliers(ctx, r, vs, 30)
	warnings = append(warnings, w...)
	n.Videos, n.Channels = append([]OutlierRow(nil), rows...), channels
	// The table follows search rank (what a viewer sees), not outlier order.
	rank := map[string]int{}
	for i, v := range vs {
		rank[v.ID] = i
	}
	sort.SliceStable(n.Videos, func(i, j int) bool { return rank[n.Videos[i].ID] < rank[n.Videos[j].ID] })
	for _, row := range rows {
		if row.Outlier >= outlierMin {
			n.Outliers = append(n.Outliers, row)
		}
	}
	sort.SliceStable(n.Channels, func(i, j int) bool { return n.Channels[i].Subscribers > n.Channels[j].Subscribers })

	topVideos := vs[:min(top, len(vs))]
	var rw []string
	n.Material, rw = ReadTop(ctx, r, topVideos, o.Lang)
	warnings = append(warnings, rw...)
	n.Gaps = FindGaps(n.Material)

	srcSeen := map[string]bool{}
	add := func(u string) {
		if u != "" && !srcSeen[u] {
			srcSeen[u] = true
			n.Sources = append(n.Sources, u)
		}
	}
	add("https://www.youtube.com/results?search_query=" + url.QueryEscape(o.Query))
	for _, v := range n.Videos[:min(len(n.Videos), maxTop)] {
		add(videoURL(v.Video))
	}
	for _, c := range n.Channels[:min(len(n.Channels), maxChannels)] {
		add(c.URL)
	}
	return n, warnings, nil
}

// sourceLine says which reader answered, so a note read later shows whether its numbers came keyless or from the API.
func (n *Note) sourceLine() string {
	if n.Source == "" {
		return "youtube (via ytx)"
	}
	return "youtube " + n.Source + " (via ytx)"
}

// onTopic keeps a suggestion that holds the seed's key terms; with 3 or more
// terms one may be missing. A term matches by stem or a shared 4-letter start,
// so "coding" still matches "code".
func onTopic(seed []string, suggestion string) bool {
	if len(seed) == 0 {
		return true
	}
	have := analyze.Terms(suggestion)
	missing := 0
	for _, t := range seed {
		found := false
		for _, h := range have {
			if h == t || sharedStart(h, t) {
				found = true
				break
			}
		}
		if !found {
			missing++
		}
	}
	allowed := 0
	if len(seed) >= 3 {
		allowed = 1
	}
	return missing <= allowed
}

func sharedStart(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	return len(ra) >= 4 && len(rb) >= 4 && string(ra[:4]) == string(rb[:4])
}

// clip shortens a quoted comment to about 280 characters at a word boundary.
func clip(s string) string {
	s = cell(s)
	r := []rune(s)
	if len(r) <= 280 {
		return s
	}
	cut := string(r[:280])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-") + "…"
}
