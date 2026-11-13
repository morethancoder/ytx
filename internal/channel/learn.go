package channel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/ops"
)

// meLearn writes a Markdown note comparing the best and worst videos of a
// period. Every observation is computed from the numbers; nothing is guessed.
func meLearn(ctx context.Context, in ops.Input) (*ops.Result, error) {
	days, err := checkDays(in, 365)
	if err != nil {
		return nil, err
	}
	dir, err := noteDir(in.Str("out"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	vs, start, end, err := s.videoStatsFor(ctx, days, 200)
	if err != nil {
		return nil, err
	}
	if len(vs) < 2 {
		return nil, apperr.New(apperr.NotFound, "need at least 2 videos with views in the last %d days to compare (found %d); try a larger --days", days, len(vs))
	}
	n := max(1, min(5, len(vs)/2))
	byPct := append([]videoStats(nil), vs...)
	sort.SliceStable(byPct, func(i, j int) bool { return byPct[i].AvgViewPct > byPct[j].AvgViewPct })
	topPct, lowPct := byPct[:n], byPct[len(byPct)-n:]
	topViews, lowViews := vs[:n], vs[len(vs)-n:] // vs is sorted by views already

	// Retention at 30 s costs one Analytics call per video (no Data API
	// units), so it is read only for the two retention groups.
	var warns []string
	failed := 0
	at30 := map[string]*float64{}
	for _, v := range append(append([]videoStats(nil), topPct...), lowPct...) {
		if _, done := at30[v.ID]; done || v.DurationS <= 30 {
			continue
		}
		pts, err := s.retention(ctx, v.ID, v.Published, v.DurationS)
		if err != nil {
			if ae := apperr.From(err); ae.Code == apperr.QuotaExceeded || ae.Code == apperr.LoginRequired || ctx.Err() != nil {
				return nil, err
			}
			failed++
			continue
		}
		if w, ok := watchAt(pts, 30/float64(v.DurationS)); ok {
			w := w
			at30[v.ID] = &w
		}
	}
	if failed > 0 {
		warns = append(warns, fmt.Sprintf("retention at 30 s couldn't be read for %d videos", failed))
	}
	fill := func(list []videoStats) []videoStats {
		out := append([]videoStats(nil), list...)
		for i := range out {
			out[i].RetentionAt30s = at30[out[i].ID]
		}
		return out
	}
	topPct, lowPct, topViews, lowViews = fill(topPct), fill(lowPct), fill(topViews), fill(lowViews)

	obs := observations(vs, topPct, lowPct, topViews, lowViews)
	note := renderNote(days, start, end, len(vs), topPct, lowPct, topViews, lowViews, obs)
	path, err := writeNew(dir, "channel-learnings-"+end, note)
	if err != nil {
		return nil, err
	}
	warns = append(warns, NoImpressions)
	data := map[string]any{"path": path, "videos": len(vs), "period": map[string]any{"start": start, "end": end, "days": days}, "observations": obs}
	r := s.result(data, warns...)
	r.Meta.Source = "oauth"
	return r, nil
}

// noteDir is --out, else research_dir from config, else the current folder.
func noteDir(out string) (string, error) {
	dir := out
	if dir == "" {
		if cfg, _ := config.Load(); cfg != nil {
			dir = cfg.ResearchDir
		}
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", apperr.Wrap(apperr.Internal, err, "no current folder")
		}
		dir = wd
	}
	dir = config.ExpandHome(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", apperr.New(apperr.InvalidInput, "can't create the folder %q: %v", dir, errText(err))
	}
	return dir, nil
}

// writeNew writes base.md, or base-2.md, base-3.md... and never replaces a
// file: O_EXCL makes the create fail if the name was taken meanwhile.
func writeNew(dir, base string, body []byte) (string, error) {
	for i := 1; i < 1000; i++ {
		name := base + ".md"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.md", base, i)
		}
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", apperr.New(apperr.InvalidInput, "can't write %q: %v", p, errText(err))
		}
		_, werr := f.Write(body)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(p)
			return "", apperr.New(apperr.Internal, "couldn't finish writing %q", p)
		}
		return p, nil
	}
	return "", apperr.New(apperr.Internal, "too many notes named %s in %s", base, dir)
}

// ---- numbers

func clock(s float64) string {
	t := int(math.Round(s))
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t%3600/60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func pluck(vs []videoStats, f func(videoStats) (float64, bool)) []float64 {
	var out []float64
	for _, v := range vs {
		if x, ok := f(v); ok {
			out = append(out, x)
		}
	}
	return out
}

func durOf(v videoStats) (float64, bool) { return float64(v.DurationS), v.DurationS > 0 }
func titleLen(v videoStats) (float64, bool) {
	return float64(utf8.RuneCountInString(v.Title)), v.Title != ""
}
func r30(v videoStats) (float64, bool) {
	if v.RetentionAt30s == nil {
		return 0, false
	}
	return *v.RetentionAt30s, true
}

func pearson(xs, ys []float64) (float64, bool) {
	if len(xs) != len(ys) || len(xs) < 5 {
		return 0, false
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(ys))
	var sxy, sxx, syy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

func weekdays(vs []videoStats) (string, int) {
	count := map[time.Weekday]int{}
	for _, v := range vs {
		if !v.Published.IsZero() {
			count[v.Published.In(pacific).Weekday()]++
		}
	}
	best, n := time.Sunday, 0
	for d := time.Sunday; d <= time.Saturday; d++ {
		if count[d] > n {
			best, n = d, count[d]
		}
	}
	if n == 0 {
		return "", 0
	}
	return best.String(), n
}

// observations are plain sentences computed from the numbers.
func observations(all, topPct, lowPct, topViews, lowViews []videoStats) []string {
	var out []string
	n := len(topPct)
	if a, b := pluck(topPct, durOf), pluck(lowPct, durOf); len(a) > 0 && len(b) > 0 {
		out = append(out, fmt.Sprintf("The %d videos that kept the largest share of each view have a median length of %s; the %d that kept the least, %s.",
			n, clock(median(a)), n, clock(median(b))))
	}
	var ds, ps []float64
	for _, v := range all {
		if v.DurationS > 0 {
			ds = append(ds, float64(v.DurationS))
			ps = append(ps, v.AvgViewPct)
		}
	}
	if r, ok := pearson(ds, ps); ok {
		how := "show little relation"
		switch {
		case r <= -0.3:
			how = "move in opposite directions: longer videos keep a smaller share"
		case r >= 0.3:
			how = "move together: longer videos keep a larger share"
		}
		out = append(out, fmt.Sprintf("Across %d videos, length and average view percentage %s (r = %.2f).", len(ds), how, r))
	}
	var total, top float64
	k := int(math.Ceil(float64(len(all)) * 0.2))
	for i, v := range all {
		total += float64(v.Views)
		if i < k {
			top += float64(v.Views)
		}
	}
	if total > 0 {
		out = append(out, fmt.Sprintf("The top %d of %d videos (20%%) brought %.0f%% of the period's views.", k, len(all), top*100/total))
	}
	if a, b := pluck(topPct, r30), pluck(lowPct, r30); len(a) > 0 && len(b) > 0 {
		out = append(out, fmt.Sprintf("At 30 seconds, the best-retaining videos still had a median audience watch ratio of %.2f, against %.2f for the least-retaining.", median(a), median(b)))
	}
	if d, c := weekdays(topViews); c > 1 {
		out = append(out, fmt.Sprintf("%d of the %d most-viewed videos were published on a %s.", c, len(topViews), d))
	}
	if a, b := pluck(topViews, titleLen), pluck(lowViews, titleLen); len(a) > 0 && len(b) > 0 {
		out = append(out, fmt.Sprintf("Median title length: %.0f characters for the most-viewed videos, %.0f for the least-viewed.", median(a), median(b)))
	}
	both := 0
	in := map[string]bool{}
	for _, v := range topViews {
		in[v.ID] = true
	}
	for _, v := range topPct {
		if in[v.ID] {
			both++
		}
	}
	out = append(out, fmt.Sprintf("%d of the %d most-viewed videos are also among the %d best at keeping viewers.", both, len(topViews), n))
	return out
}

// ---- note

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.Join(strings.Fields(s), " ")
}

func table(b *strings.Builder, vs []videoStats) {
	b.WriteString("| Video | Length | Published | Views | Avg viewed | Avg view | At 30 s |\n|---|---|---|---:|---:|---:|---:|\n")
	for _, v := range vs {
		title := v.Title
		if title == "" {
			title = v.ID
		}
		pub, length, at := "", "", ""
		if !v.Published.IsZero() {
			pub = v.Published.In(pacific).Format("Mon 2006-01-02")
		}
		if v.DurationS > 0 {
			length = clock(float64(v.DurationS))
		}
		if v.RetentionAt30s != nil {
			at = fmt.Sprintf("%.2f", *v.RetentionAt30s)
		}
		fmt.Fprintf(b, "| [%s](%s) | %s | %s | %d | %.1f%% | %s | %s |\n", cell(title), v.URL, length, pub, v.Views, v.AvgViewPct, clock(v.AvgViewS), at)
	}
	b.WriteString("\n")
}

func renderNote(days int, start, end string, count int, topPct, lowPct, topViews, lowViews []videoStats, obs []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: \"Channel learnings %s\"\ntype: channel-learnings\nsource: ytx me learn\ncreated: %s\nperiod_start: %s\nperiod_end: %s\ndays: %d\nvideos: %d\ntags: [youtube, analytics]\n---\n\n",
		end, now().UTC().Format(time.RFC3339), start, end, days, count)
	fmt.Fprintf(&b, "# Channel learnings, %s to %s\n\n", start, end)
	fmt.Fprintf(&b, "From the YouTube Analytics API: the %d videos with views in these %d days (at most the top 200 by views). "+
		"\"Avg viewed\" is the average share of the video watched per view; \"At 30 s\" is the audience watch ratio 30 seconds in. "+
		"Impressions and click-through rate are not in the Analytics API, so they are not here.\n\n", count, days)
	b.WriteString("## Kept viewers best (average view percentage)\n\n")
	table(&b, topPct)
	b.WriteString("## Kept viewers least\n\n")
	table(&b, lowPct)
	b.WriteString("## Most views\n\n")
	table(&b, topViews)
	b.WriteString("## Fewest views\n\n")
	table(&b, lowViews)
	b.WriteString("## Observations\n\n")
	for _, o := range obs {
		b.WriteString("- " + o + "\n")
	}
	return []byte(b.String())
}
