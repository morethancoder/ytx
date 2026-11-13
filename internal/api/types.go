package api

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/model"
)

type thumb struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type snippet struct {
	PublishedAt          string           `json:"publishedAt"`
	ChannelID            string           `json:"channelId"`
	ChannelTitle         string           `json:"channelTitle"`
	Title                string           `json:"title"`
	Description          string           `json:"description"`
	Thumbnails           map[string]thumb `json:"thumbnails"`
	Tags                 []string         `json:"tags"`
	LiveBroadcastContent string           `json:"liveBroadcastContent"`
	CustomURL            string           `json:"customUrl"`
}

type videoItem struct {
	ID      string  `json:"id"`
	Snippet snippet `json:"snippet"`
	Content struct {
		Duration string `json:"duration"`
	} `json:"contentDetails"`
	Stats struct {
		Views    string `json:"viewCount"`
		Likes    string `json:"likeCount"`
		Comments string `json:"commentCount"`
	} `json:"statistics"`
}

type videoList struct {
	NextPageToken string      `json:"nextPageToken"`
	Items         []videoItem `json:"items"`
}

var durRe = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// parseDuration reads an ISO 8601 duration such as PT1H2M3S into seconds.
func parseDuration(s string) int {
	m := durRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return n(1)*86400 + n(2)*3600 + n(3)*60 + n(4)
}

func num(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }

// bestThumb picks the largest thumbnail.
func bestThumb(t map[string]thumb) string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := t[keys[i]], t[keys[j]]
		if a.Width != b.Width {
			return a.Width > b.Width
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 0 {
		return ""
	}
	return t[keys[0]].URL
}

// looksShort is the vertical hint: the API gives no frame size, so a video of
// up to three minutes counts as a Short only when it says so in its text.
func looksShort(v videoItem) bool {
	hay := strings.ToLower(v.Snippet.Title + " " + v.Snippet.Description + " " + strings.Join(v.Snippet.Tags, " "))
	return strings.Contains(hay, "#shorts") || strings.Contains(hay, "#short ")
}

func (c *Client) toVideo(v videoItem) model.Video {
	out := model.Video{
		ID:          v.ID,
		URL:         "https://www.youtube.com/watch?v=" + v.ID,
		Title:       v.Snippet.Title,
		ChannelID:   v.Snippet.ChannelID,
		ChannelName: v.Snippet.ChannelTitle,
		Views:       num(v.Stats.Views),
		Likes:       num(v.Stats.Likes),
		Comments:    num(v.Stats.Comments),
		DurationS:   parseDuration(v.Content.Duration),
		Thumbnail:   bestThumb(v.Snippet.Thumbnails),
		Description: v.Snippet.Description,
		Tags:        v.Snippet.Tags,
		IsLive:      v.Snippet.LiveBroadcastContent == "live",
	}
	if t, err := time.Parse(time.RFC3339, v.Snippet.PublishedAt); err == nil {
		out.Published = t
		if d := int(c.now().Sub(t).Hours() / 24); d > 0 {
			out.AgeDays = d
		}
	}
	out.IsShort = out.DurationS > 0 && (out.DurationS <= 60 || out.DurationS <= 180 && looksShort(v))
	return out
}
