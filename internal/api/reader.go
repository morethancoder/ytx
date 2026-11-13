package api

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

func limitOr(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

func pageSize(want, max int) string {
	if want > max {
		want = max
	}
	if want < 1 {
		want = 1
	}
	return strconv.Itoa(want)
}

// videosByIDs fills statistics, duration and snippet for ids, 50 per call,
// and returns them in the order of ids. Ids YouTube no longer knows are dropped.
func (c *Client) videosByIDs(ctx context.Context, ids []string) ([]model.Video, error) {
	byID := map[string]model.Video{}
	for i := 0; i < len(ids); i += 50 {
		end := min(i+50, len(ids))
		var l videoList
		err := c.get(ctx, "videos", costList, url.Values{
			"part":       {"snippet,contentDetails,statistics"},
			"id":         {strings.Join(ids[i:end], ",")},
			"maxResults": {"50"},
		}, &l)
		if err != nil {
			return nil, err
		}
		for _, it := range l.Items {
			byID[it.ID] = c.toVideo(it)
		}
	}
	out := make([]model.Video, 0, len(ids))
	for _, id := range ids {
		if v, ok := byID[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *Client) Search(ctx context.Context, o yt.SearchOpts) ([]model.Video, error) {
	limit := limitOr(o.Limit, 10)
	p := url.Values{"part": {"snippet"}, "q": {o.Query}, "type": {"video"}}
	switch o.Order {
	case "views":
		p.Set("order", "viewCount")
	case "date":
		p.Set("order", "date")
	case "relevance", "":
		p.Set("order", "relevance")
	default:
		return nil, apperr.New(apperr.InvalidInput, "order must be relevance, views or date")
	}
	short := o.Type == "short"
	if short {
		p.Set("videoDuration", "short")
	}
	if o.SinceDays > 0 {
		p.Set("publishedAfter", c.now().UTC().AddDate(0, 0, -o.SinceDays).Format(time.RFC3339))
	}
	if o.Region != "" {
		p.Set("regionCode", strings.ToUpper(o.Region))
	}
	if o.Lang != "" {
		p.Set("relevanceLanguage", o.Lang)
	}
	var out []model.Video
	seen := map[string]bool{}
	token := ""
	for page := 0; page < maxPages && len(out) < limit; page++ {
		p.Set("maxResults", pageSize(limit-len(out), 50))
		if token != "" {
			p.Set("pageToken", token)
		}
		var res struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ID struct {
					VideoID string `json:"videoId"`
				} `json:"id"`
			} `json:"items"`
		}
		if err := c.get(ctx, "search", costSearch, p, &res); err != nil {
			return nil, err
		}
		var ids []string
		for _, it := range res.Items {
			if id := it.ID.VideoID; id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		vids, err := c.videosByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, v := range vids {
			if short && !v.IsShort {
				continue
			}
			if len(out) < limit {
				out = append(out, v)
			}
		}
		if token = res.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (c *Client) Video(ctx context.Context, id string) (*model.Video, error) {
	vs, err := c.videosByIDs(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, apperr.New(apperr.NotFound, "no video with id %s", id)
	}
	return &vs[0], nil
}

// playlistVideos lists a playlist's video ids (50 per call) and then their details.
// keep filters the details; paging goes on until limit videos are kept.
func (c *Client) playlistVideos(ctx context.Context, playlistID string, limit int, keep func(model.Video) bool) ([]model.Video, error) {
	var out []model.Video
	token := ""
	for page := 0; page < maxPages && len(out) < limit; page++ {
		p := url.Values{"part": {"contentDetails"}, "playlistId": {playlistID}, "maxResults": {pageSize(limit-len(out), 50)}}
		if token != "" {
			p.Set("pageToken", token)
		}
		var res struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Content struct {
					VideoID string `json:"videoId"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if err := c.get(ctx, "playlistItems", costList, p, &res); err != nil {
			return nil, err
		}
		var ids []string
		for _, it := range res.Items {
			if it.Content.VideoID != "" {
				ids = append(ids, it.Content.VideoID)
			}
		}
		vids, err := c.videosByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, v := range vids {
			if (keep == nil || keep(v)) && len(out) < limit {
				out = append(out, v)
			}
		}
		if token = res.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (c *Client) Channel(ctx context.Context, ref string, o yt.ChannelOpts) (*model.Channel, error) {
	if o.Shorts {
		// The API has no Shorts shelf and duration alone misjudges them, so keyless answers this.
		return nil, apperr.New(apperr.NotImplemented, "the official API can't list a channel's Shorts reliably")
	}
	p := url.Values{"part": {"snippet,statistics,contentDetails"}}
	if strings.HasPrefix(ref, "@") {
		p.Set("forHandle", ref)
	} else {
		p.Set("id", ref)
	}
	var res struct {
		Items []struct {
			ID      string  `json:"id"`
			Snippet snippet `json:"snippet"`
			Stats   struct {
				Subs   string `json:"subscriberCount"`
				Views  string `json:"viewCount"`
				Videos string `json:"videoCount"`
			} `json:"statistics"`
			Content struct {
				Related struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := c.get(ctx, "channels", costList, p, &res); err != nil {
		return nil, err
	}
	if len(res.Items) == 0 {
		return nil, apperr.New(apperr.NotFound, "no channel found for %s", ref)
	}
	it := res.Items[0]
	ch := &model.Channel{
		ID:          it.ID,
		URL:         "https://www.youtube.com/channel/" + it.ID,
		Name:        it.Snippet.Title,
		Subscribers: num(it.Stats.Subs),
		VideoCount:  num(it.Stats.Videos),
		ViewCount:   num(it.Stats.Views),
		Description: it.Snippet.Description,
		Thumbnail:   bestThumb(it.Snippet.Thumbnails),
	}
	if strings.HasPrefix(it.Snippet.CustomURL, "@") {
		ch.Handle = it.Snippet.CustomURL
		ch.URL = "https://www.youtube.com/" + ch.Handle
	}
	if up := it.Content.Related.Uploads; up != "" {
		recent, err := c.playlistVideos(ctx, up, limitOr(o.Limit, 10), func(v model.Video) bool { return !v.IsShort })
		if err != nil {
			return nil, err
		}
		ch.Recent = recent
	}
	return ch, nil
}

func (c *Client) Comments(ctx context.Context, videoID string, o yt.CommentOpts) ([]model.Comment, error) {
	limit := limitOr(o.Limit, 20)
	order := "relevance"
	if o.Order == "new" {
		order = "time"
	}
	var out []model.Comment
	token := ""
	for page := 0; page < maxPages*5 && len(out) < limit; page++ {
		p := url.Values{
			"part": {"snippet"}, "videoId": {videoID}, "order": {order},
			"textFormat": {"plainText"}, "maxResults": {pageSize(limit-len(out), 100)},
		}
		if token != "" {
			p.Set("pageToken", token)
		}
		var res struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ID      string `json:"id"`
				Snippet struct {
					Top struct {
						ID      string `json:"id"`
						Snippet struct {
							Author    string `json:"authorDisplayName"`
							Text      string `json:"textDisplay"`
							Likes     int64  `json:"likeCount"`
							Published string `json:"publishedAt"`
						} `json:"snippet"`
					} `json:"topLevelComment"`
					Replies int64 `json:"totalReplyCount"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if err := c.get(ctx, "commentThreads", costList, p, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Items {
			s := it.Snippet.Top.Snippet
			cm := model.Comment{ID: it.Snippet.Top.ID, Author: s.Author, Text: s.Text, Likes: s.Likes, Replies: it.Snippet.Replies}
			if cm.ID == "" {
				cm.ID = it.ID
			}
			if t, err := time.Parse(time.RFC3339, s.Published); err == nil {
				cm.Published = t
			}
			if len(out) < limit {
				out = append(out, cm)
			}
		}
		if token = res.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (c *Client) Transcript(context.Context, string, string) (*model.Transcript, error) {
	return nil, apperr.New(apperr.NotImplemented, "the official API has no public transcript endpoint")
}

func (c *Client) Suggest(context.Context, string, string, string) ([]string, error) {
	return nil, apperr.New(apperr.NotImplemented, "the official API has no search suggestions endpoint")
}

// categoryIDs maps the names ytx accepts to YouTube's videoCategoryId.
var categoryIDs = map[string]string{
	"film": "1", "autos": "2", "music": "10", "pets": "15", "sports": "17", "travel": "19",
	"gaming": "20", "people": "22", "comedy": "23", "entertainment": "24", "news": "25",
	"howto": "26", "education": "27", "science": "28", "tech": "28", "movies": "30",
}

func (c *Client) Trending(ctx context.Context, region, category string, limit int) ([]model.Video, error) {
	limit = limitOr(limit, 20)
	p := url.Values{"part": {"snippet,contentDetails,statistics"}, "chart": {"mostPopular"}}
	if region != "" {
		p.Set("regionCode", strings.ToUpper(region))
	}
	if category != "" && category != "all" && category != "now" {
		id, ok := categoryIDs[strings.ToLower(category)]
		if !ok {
			if _, err := strconv.Atoi(category); err != nil {
				return nil, apperr.New(apperr.InvalidInput, "unknown trending category %q", category)
			}
			id = category
		}
		p.Set("videoCategoryId", id)
	}
	var out []model.Video
	token := ""
	for page := 0; page < maxPages && len(out) < limit; page++ {
		p.Set("maxResults", pageSize(limit-len(out), 50))
		if token != "" {
			p.Set("pageToken", token)
		}
		var l videoList
		if err := c.get(ctx, "videos", costList, p, &l); err != nil {
			return nil, err
		}
		for _, it := range l.Items {
			if len(out) < limit {
				out = append(out, c.toVideo(it))
			}
		}
		if token = l.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (c *Client) Playlist(ctx context.Context, id string, limit int) (*model.Playlist, []model.Video, error) {
	var res struct {
		Items []struct {
			ID      string  `json:"id"`
			Snippet snippet `json:"snippet"`
			Status  struct {
				Privacy string `json:"privacyStatus"`
			} `json:"status"`
			Content struct {
				ItemCount int64 `json:"itemCount"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	err := c.get(ctx, "playlists", costList, url.Values{"part": {"snippet,contentDetails,status"}, "id": {id}}, &res)
	if err != nil {
		return nil, nil, err
	}
	if len(res.Items) == 0 {
		return nil, nil, apperr.New(apperr.NotFound, "no playlist with id %s", id)
	}
	it := res.Items[0]
	pl := &model.Playlist{
		ID: it.ID, URL: "https://www.youtube.com/playlist?list=" + it.ID, Title: it.Snippet.Title,
		Description: it.Snippet.Description, Privacy: it.Status.Privacy, ItemCount: it.Content.ItemCount,
	}
	vids, err := c.playlistVideos(ctx, id, limitOr(limit, 50), nil)
	if err != nil {
		return nil, nil, err
	}
	return pl, vids, nil
}
