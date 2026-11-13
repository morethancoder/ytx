// Package research builds the research commands (outliers, gaps, research)
// on top of the Reader. They only read through yt.Reader, so the cache and the
// source trace work as for every other read.
package research

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morethancoder/ytx/internal/analyze"
	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/reads"
	"github.com/morethancoder/ytx/internal/yt"
)

func init() {
	ops.Handle("research.outliers", outliersOp)
	ops.Handle("research.gaps", gapsOp)
	ops.Handle("research.run", runOp)
}

// concurrency is how many channel or video lookups run at once; YouTube is
// polite to a few parallel readers and unfriendly to dozens.
const concurrency = 4

// OutlierRow is one search result scored against its channel.
type OutlierRow struct {
	model.Video
	ChannelMedian      int64 `json:"channel_median"`
	ChannelSubscribers int64 `json:"channel_subscribers"`
	// TooNew marks "no score": the channel's recent uploads are too new to judge.
	TooNew bool `json:"too_new,omitempty"`
}

// ChannelRow is one channel met in the results.
type ChannelRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Subscribers int64  `json:"subscribers"`
	Median      int64  `json:"median_views"`
	TooNew      bool   `json:"too_new,omitempty"`
}

// forEach runs f for 0..n-1, at most `concurrency` at a time.
func forEach(n int, f func(i int)) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			f(i)
		}(i)
	}
	wg.Wait()
}

// ScoreOutliers scores videos against their channels' medians. Zero-view and
// live videos are skipped (they have no honest score yet). Only uploads at least
// analyze.SettleDays old set the median; a channel with too few gets none, its
// videos are marked TooNew and one warning names it. A channel that
// can't be read keeps its videos with outlier 0 and adds a warning.
func ScoreOutliers(ctx context.Context, r yt.Reader, vs []model.Video, recent int) ([]OutlierRow, []ChannelRow, []string) {
	var ids []string
	seen := map[string]bool{}
	for _, v := range vs {
		if v.ChannelID != "" && !seen[v.ChannelID] {
			seen[v.ChannelID] = true
			ids = append(ids, v.ChannelID)
		}
	}
	chans := make([]*model.Channel, len(ids))
	errs := make([]error, len(ids))
	forEach(len(ids), func(i int) {
		chans[i], errs[i] = r.Channel(ctx, ids[i], yt.ChannelOpts{Limit: recent})
	})
	var warnings []string
	byID := map[string]*ChannelRow{}
	var channels []ChannelRow
	for i, id := range ids {
		if errs[i] != nil || chans[i] == nil {
			warnings = append(warnings, "channel "+id+": "+errString(errs[i]))
			continue
		}
		c := chans[i]
		med, ok := analyze.SettledMedian(c.Recent, time.Now())
		if !ok {
			warnings = append(warnings, "channel "+nameOr(c.Name, id)+": recent uploads are too new to judge, so its videos have no outlier score")
		}
		channels = append(channels, ChannelRow{ID: id, Name: c.Name, URL: c.URL, Subscribers: c.Subscribers, Median: med, TooNew: !ok})
	}
	// Point into the slice only once it has stopped growing.
	for i := range channels {
		byID[channels[i].ID] = &channels[i]
	}
	var rows []OutlierRow
	for _, v := range vs {
		if v.Views <= 0 || v.IsLive {
			continue
		}
		row := OutlierRow{Video: v}
		if c := byID[v.ChannelID]; c != nil {
			row.ChannelMedian, row.ChannelSubscribers = c.Median, c.Subscribers
			row.TooNew = c.TooNew
			row.Outlier = analyze.Outlier(v.Views, c.Median)
		} else {
			row.Outlier = 0
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Outlier != rows[j].Outlier {
			return rows[i].Outlier > rows[j].Outlier
		}
		return rows[i].Views > rows[j].Views
	})
	return rows, channels, warnings
}

func nameOr(name, id string) string {
	if name == "" {
		return id
	}
	return name
}

func errString(err error) string {
	if err == nil {
		return "no data"
	}
	return err.Error()
}

func outliersOp(ctx context.Context, in ops.Input) (*ops.Result, error) {
	o, err := reads.SearchOpts(in)
	if err != nil {
		return nil, err
	}
	var min float64
	if s := strings.TrimSpace(in.Str("min")); s != "" {
		if min, err = strconv.ParseFloat(s, 64); err != nil || min < 0 {
			return nil, apperr.New(apperr.InvalidInput, "--min must be a number like 2")
		}
	}
	recent := in.Int("recent", 30)
	if recent < 1 || recent > 200 {
		return nil, apperr.New(apperr.InvalidInput, "--recent must be 1 to 200")
	}
	r, err := reads.Reader(in)
	if err != nil {
		return nil, err
	}
	vs, err := r.Search(ctx, o)
	if err != nil {
		return nil, err
	}
	if len(vs) > o.Limit {
		vs = vs[:o.Limit]
	}
	reads.FillAge(vs)
	rows, channels, warnings := ScoreOutliers(ctx, r, vs, recent)
	if min > 0 {
		kept := rows[:0]
		for _, row := range rows {
			if row.Outlier >= min {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	return &ops.Result{Data: map[string]any{"query": o.Query, "videos": rows, "channels": channels}, Warnings: warnings}, nil
}
