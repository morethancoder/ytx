package channel

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/yt"
)

// NoImpressions explains why impressions and click-through rate are missing:
// the Analytics API's reports.query has no thumbnail-impression metric (only
// card, annotation and ad impressions); YouTube Studio and the Reporting API's
// bulk reports have them.
const NoImpressions = "impressions and impression click-through rate are not available from the YouTube Analytics API (only YouTube Studio or the Reporting API's bulk reports have them), so they are left out"

// myVideo is one of the user's uploads, with what only the owner can see.
type myVideo struct {
	model.Video
	Privacy      string `json:"privacy,omitempty"`
	PublishAt    string `json:"publish_at,omitempty"`
	UploadStatus string `json:"upload_status,omitempty"`
}

func toVideo(v *videoItem) model.Video {
	m := model.Video{ID: v.ID, URL: yt.VideoURL(v.ID, false), Title: v.title(),
		ChannelID: str(v.Snippet, "channelId"), ChannelName: str(v.Snippet, "channelTitle"),
		Description: str(v.Snippet, "description"), DurationS: isoSeconds(v.ContentDetails.Duration)}
	if t, err := time.Parse(time.RFC3339, str(v.Snippet, "publishedAt")); err == nil {
		m.Published = t
		m.AgeDays = int(now().Sub(t).Hours() / 24)
	}
	if tags, ok := v.Snippet["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				m.Tags = append(m.Tags, s)
			}
		}
	}
	m.Views = int64(num(str(v.Statistics, "viewCount")))
	m.Likes = int64(num(str(v.Statistics, "likeCount")))
	m.Comments = int64(num(str(v.Statistics, "commentCount")))
	return m
}

func mineVideos(ctx context.Context, in ops.Input) (*ops.Result, error) {
	limit := in.Int("limit", 50)
	if limit < 1 || limit > 5000 {
		return nil, apperr.New(apperr.InvalidInput, "--limit must be between 1 and 5000")
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := s.myChannel(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	page := ""
	for len(ids) < limit {
		var r struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ContentDetails struct {
					VideoID string `json:"videoId"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		q := url.Values{"part": {"contentDetails"}, "playlistId": {ch.Uploads}, "maxResults": {strconv.Itoa(min(50, limit-len(ids)))}}
		if page != "" {
			q.Set("pageToken", page)
		}
		if err := s.getJSON(ctx, endpointURL(dataBase, "/playlistItems", q), costList, &r); err != nil {
			return nil, err
		}
		for _, it := range r.Items {
			ids = append(ids, it.ContentDetails.VideoID)
		}
		if page = r.NextPageToken; page == "" || len(r.Items) == 0 {
			break
		}
	}
	got, err := s.videos(ctx, ids, "snippet,status,contentDetails,statistics")
	if err != nil {
		return nil, err
	}
	out := make([]myVideo, 0, len(ids))
	for _, id := range ids {
		v := got[id]
		if v == nil {
			continue
		}
		mv := myVideo{Video: toVideo(v), Privacy: v.privacy(), PublishAt: str(v.Status, "publishAt"), UploadStatus: str(v.Status, "uploadStatus")}
		mv.ChannelName = ch.Title
		out = append(out, mv)
	}
	return s.result(out), nil
}

// ---- Analytics API

// report is a reports.query answer: column names, then rows in that order.
type report struct {
	ColumnHeaders []struct {
		Name string `json:"name"`
	} `json:"columnHeaders"`
	Rows [][]any `json:"rows"`
}

func (r *report) records() []map[string]any {
	out := make([]map[string]any, 0, len(r.Rows))
	for _, row := range r.Rows {
		m := map[string]any{}
		for i, h := range r.ColumnHeaders {
			if i < len(row) {
				m[h.Name] = row[i]
			}
		}
		out = append(out, m)
	}
	return out
}

// analytics runs one reports.query for the signed-in channel. It spends no
// Data API units: the Analytics API has its own quota.
func (s *session) analytics(ctx context.Context, q url.Values) (*report, error) {
	q.Set("ids", "channel==MINE")
	r := &report{}
	_, b, err := s.send(ctx, request{method: http.MethodGet, url: endpointURL(analyticsBase, "/reports", q), analytics: true})
	if err != nil {
		return nil, err
	}
	if err := decode(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

// pacific is the Analytics API's day boundary.
var pacific = func() *time.Location {
	if l, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return l
	}
	return time.FixedZone("PT", -8*3600)
}()

// period is the last n days, today included, as Analytics API dates.
func period(days int) (start, end string) {
	e := now().In(pacific)
	return e.AddDate(0, 0, -(days - 1)).Format("2006-01-02"), e.Format("2006-01-02")
}

func checkDays(in ops.Input, def int) (int, error) {
	d := in.Int("days", def)
	if d < 1 || d > 3650 {
		return 0, apperr.New(apperr.InvalidInput, "--days must be between 1 and 3650")
	}
	return d, nil
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func round(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}

func meChannel(ctx context.Context, in ops.Input) (*ops.Result, error) {
	days, err := checkDays(in, 28)
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	start, end := period(days)
	r, err := s.analytics(ctx, url.Values{"startDate": {start}, "endDate": {end},
		"metrics": {"views,estimatedMinutesWatched,averageViewDuration,subscribersGained,subscribersLost"}})
	if err != nil {
		return nil, err
	}
	data := map[string]any{"period": map[string]any{"start": start, "end": end, "days": days}}
	var warns []string
	recs := r.records()
	if len(recs) == 0 {
		warns = append(warns, "YouTube has no analytics for this period yet")
		recs = []map[string]any{{}}
	}
	m := recs[0]
	gained, lost := int64(num(m["subscribersGained"])), int64(num(m["subscribersLost"]))
	data["views"] = int64(num(m["views"]))
	data["estimated_minutes_watched"] = int64(num(m["estimatedMinutesWatched"]))
	data["average_view_duration_s"] = int64(num(m["averageViewDuration"]))
	data["subscribers_gained"], data["subscribers_lost"], data["subscribers_net"] = gained, lost, gained-lost
	return s.result(data, warns...), nil
}

// videoStats is one video's numbers over a period.
type videoStats struct {
	ID             string    `json:"id"`
	URL            string    `json:"url"`
	Title          string    `json:"title"`
	Published      time.Time `json:"published,omitempty"`
	DurationS      int       `json:"duration_s,omitempty"`
	Views          int64     `json:"views"`
	MinutesWatched float64   `json:"estimated_minutes_watched"`
	AvgViewS       float64   `json:"average_view_duration_s"`
	AvgViewPct     float64   `json:"average_view_percentage"`
	// RetentionAt30s is audienceWatchRatio 30 s in (me learn fills it).
	RetentionAt30s *float64 `json:"retention_at_30s,omitempty"`
}

// videoStatsFor is the top-videos report (sorted by views, at most 200 rows)
// joined with each video's title, date and length from the Data API.
func (s *session) videoStatsFor(ctx context.Context, days, limit int) ([]videoStats, string, string, error) {
	start, end := period(days)
	r, err := s.analytics(ctx, url.Values{"startDate": {start}, "endDate": {end}, "dimensions": {"video"},
		"metrics": {"views,estimatedMinutesWatched,averageViewDuration,averageViewPercentage"},
		"sort":    {"-views"}, "maxResults": {strconv.Itoa(limit)}})
	if err != nil {
		return nil, "", "", err
	}
	var out []videoStats
	var ids []string
	for _, m := range r.records() {
		id, _ := m["video"].(string)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		out = append(out, videoStats{ID: id, URL: yt.VideoURL(id, false), Views: int64(num(m["views"])),
			MinutesWatched: num(m["estimatedMinutesWatched"]), AvgViewS: num(m["averageViewDuration"]),
			AvgViewPct: round(num(m["averageViewPercentage"]), 2)})
	}
	if len(ids) > 0 {
		got, err := s.videos(ctx, ids, "snippet,contentDetails")
		if err != nil {
			return nil, "", "", err
		}
		for i := range out {
			if v := got[out[i].ID]; v != nil {
				out[i].Title = v.title()
				out[i].DurationS = isoSeconds(v.ContentDetails.Duration)
				if t, err := time.Parse(time.RFC3339, str(v.Snippet, "publishedAt")); err == nil {
					out[i].Published = t
				}
			}
		}
	}
	return out, start, end, nil
}

func meVideos(ctx context.Context, in ops.Input) (*ops.Result, error) {
	days, err := checkDays(in, 90)
	if err != nil {
		return nil, err
	}
	limit := in.Int("limit", 50)
	if limit < 1 {
		return nil, apperr.New(apperr.InvalidInput, "--limit must be 1 or more")
	}
	warns := []string{NoImpressions}
	if limit > 200 {
		limit = 200
		warns = append(warns, "the Analytics API returns at most 200 videos per report; showing the top 200 by views")
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	vs, _, _, err := s.videoStatsFor(ctx, days, limit)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		warns = append(warns, "no video had views in this period")
		vs = []videoStats{}
	}
	return s.result(vs, warns...), nil
}

// retentionPoint is one point of the retention curve.
type retentionPoint struct {
	Ratio   float64 `json:"ratio"`
	AtS     float64 `json:"at_s,omitempty"`
	Watch   float64 `json:"audience_watch_ratio"`
	RelPerf float64 `json:"relative_retention_performance"`
}

// retention reads one video's curve since it was published (the report takes
// a single video, so this is one call per video).
func (s *session) retention(ctx context.Context, id string, published time.Time, durS int) ([]retentionPoint, error) {
	start := "2005-02-14"
	if !published.IsZero() {
		start = published.In(pacific).Format("2006-01-02")
	}
	_, end := period(1)
	r, err := s.analytics(ctx, url.Values{"startDate": {start}, "endDate": {end}, "filters": {"video==" + id},
		"dimensions": {"elapsedVideoTimeRatio"}, "metrics": {"audienceWatchRatio,relativeRetentionPerformance"},
		"sort": {"elapsedVideoTimeRatio"}})
	if err != nil {
		return nil, err
	}
	var pts []retentionPoint
	for _, m := range r.records() {
		p := retentionPoint{Ratio: num(m["elapsedVideoTimeRatio"]), Watch: round(num(m["audienceWatchRatio"]), 4),
			RelPerf: round(num(m["relativeRetentionPerformance"]), 4)}
		if durS > 0 {
			p.AtS = round(p.Ratio*float64(durS), 1)
		}
		pts = append(pts, p)
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Ratio < pts[j].Ratio })
	return pts, nil
}

// watchAt interpolates audienceWatchRatio at a point of the video (0..1).
func watchAt(pts []retentionPoint, ratio float64) (float64, bool) {
	if len(pts) == 0 || ratio < 0 || ratio > 1 {
		return 0, false
	}
	if ratio <= pts[0].Ratio {
		return pts[0].Watch, true
	}
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		if ratio <= b.Ratio {
			if b.Ratio == a.Ratio {
				return b.Watch, true
			}
			f := (ratio - a.Ratio) / (b.Ratio - a.Ratio)
			return round(a.Watch+f*(b.Watch-a.Watch), 4), true
		}
	}
	return pts[len(pts)-1].Watch, true
}

func meRetention(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.video(ctx, id, "snippet,contentDetails")
	if err != nil {
		return nil, err
	}
	mv := toVideo(v)
	pts, err := s.retention(ctx, id, mv.Published, mv.DurationS)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"video": id, "url": mv.URL, "title": mv.Title, "duration_s": mv.DurationS, "points": pts}
	var warns []string
	if len(pts) == 0 {
		warns = append(warns, "YouTube has no retention data for this video yet (it needs enough views)")
		data["points"] = []retentionPoint{}
	} else {
		marks := map[string]any{}
		for _, q := range []float64{0.25, 0.5, 0.75} {
			if w, ok := watchAt(pts, q); ok {
				marks[strconv.Itoa(int(q*100))+"%"] = w
			}
		}
		if mv.DurationS > 30 {
			if w, ok := watchAt(pts, 30/float64(mv.DurationS)); ok {
				data["at_30s"] = w
			}
		}
		data["at"] = marks
	}
	return s.result(data, warns...), nil
}
