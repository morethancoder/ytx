package keyless

import (
	"context"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// Trending: YouTube removed its Trending page and the browse endpoints behind it
// (FEtrending, FEexplore now answer "invalid argument"). There is no feed with
// the same meaning, so this returns the closest real equivalents and never
// invents data:
//
//   - music: the YouTube Charts weekly "Top music videos" for the region (the
//     same feed charts.youtube.com shows, keyless). Views is that week's views.
//   - now, gaming, movies: a search with no or a fixed topic query ("", "gaming",
//     "official trailer"), upload date today (widened to this week when too few),
//     sorted by view count. These are the most-viewed new videos, not YouTube's
//     own editorial trending list.
func (c *Client) Trending(ctx context.Context, region, category string, limit int) ([]model.Video, error) {
	limit = clampLimit(limit, 25)
	query := ""
	switch strings.ToLower(category) {
	case "", "now":
	case "music":
		vs, err := c.chartsMusic(ctx, region, limit)
		if err == nil && len(vs) > 0 {
			return vs, nil
		}
		query = "music"
	case "gaming":
		query = "gaming"
	case "movies":
		query = "official trailer"
	default:
		return nil, apperr.New(apperr.InvalidInput, "trending category must be now, music, gaming or movies")
	}
	o := yt.SearchOpts{Query: query, Limit: limit, Type: "video", Order: "views", SinceDays: 1, Region: region}
	vs, err := c.Search(ctx, o)
	if err != nil {
		return nil, err
	}
	if len(vs) < limit {
		o.SinceDays = 7
		if wider, werr := c.Search(ctx, o); werr == nil && len(wider) > len(vs) {
			vs = wider
		}
	}
	return vs, nil
}

// chartsMusic reads the weekly top music videos for a region from YouTube Charts.
func (c *Client) chartsMusic(ctx context.Context, region string, limit int) ([]model.Video, error) {
	cc := strings.ToLower(region)
	if cc == "" {
		cc = "us"
	}
	base := c.ChartsBase
	if base == "" {
		base = DefaultChartsBase
	}
	resp, err := c.post(ctx, request{
		base: base, endpoint: "browse", client: obj{"clientName": "WEB_MUSIC_ANALYTICS", "clientVersion": "2.0", "hl": "en", "gl": strings.ToUpper(cc)},
		body:    obj{"browseId": "FEmusic_analytics_charts_home", "query": "perspective=CHART&chart_params_country_code=" + cc + "&chart_params_chart_type=VIDEOS&chart_params_period_type=WEEKLY"},
		headers: map[string]string{"Referer": base + "/", "Origin": base},
	})
	if err != nil {
		return nil, err
	}
	return parseChartsVideos(resp, limit), nil
}

// parseChartsVideos reads the TOP_VIEWS_CHART list of a Charts answer.
func parseChartsVideos(resp obj, limit int) []model.Video {
	out := []model.Video{}
	for _, list := range asList(findFirst(resp, "videos")) {
		if str(get(list, "listType")) != "TOP_VIEWS_CHART" {
			continue
		}
		for _, e := range asList(get(list, "videoViews")) {
			id := str(get(e, "id"))
			if id == "" {
				continue
			}
			v := model.Video{ID: id, URL: yt.VideoURL(id, false), Title: str(get(e, "title")),
				ChannelID: str(get(e, "externalChannelId")), ChannelName: str(get(e, "channelName")),
				Views: parseCount(str(get(e, "viewCount"))), Thumbnail: bestThumb(asList(get(e, "thumbnail", "thumbnails")))}
			if d, ok := get(e, "videoDuration").(float64); ok {
				v.DurationS = int(d)
			}
			rd := asMap(get(e, "releaseDate"))
			if y, ok := rd["year"].(float64); ok && y > 0 {
				m, _ := rd["month"].(float64)
				d, _ := rd["day"].(float64)
				if m < 1 {
					m = 1
				}
				if d < 1 {
					d = 1
				}
				v.Published = time.Date(int(y), time.Month(int(m)), int(d), 0, 0, 0, 0, time.UTC)
			}
			out = append(out, v)
		}
		break
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
