package keyless

import (
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// collectVideos finds every video-like item below root, in rank order. Search,
// channel tabs, playlists and charts all hold videos in different containers
// (videoRenderer, lockupViewModel, shortsLockupViewModel), so one walker reads
// them all.
func collectVideos(root any, now time.Time) []model.Video {
	var out []model.Video
	walk(root, func(k string, val any) bool {
		switch k {
		case "videoRenderer", "compactVideoRenderer", "gridVideoRenderer", "playlistVideoRenderer":
			if v, ok := parseVideoRenderer(asMap(val), now); ok {
				out = append(out, v)
			}
			return false
		case "lockupViewModel":
			if v, ok := parseLockup(asMap(val), now); ok {
				out = append(out, v)
			}
			return false
		case "shortsLockupViewModel":
			if v, ok := parseShortsLockup(asMap(val)); ok {
				out = append(out, v)
			}
			return false
		}
		return true
	})
	return out
}

// setPublished fills PublishedText and the approximate Published from a
// relative date such as "3 weeks ago".
func setPublished(v *model.Video, s string, now time.Time) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	v.PublishedText = s
	if t, _, ok := parseAgo(s, now); ok {
		v.Published = t
	}
}

func parseVideoRenderer(r map[string]any, now time.Time) (model.Video, bool) {
	id := str(r["videoId"])
	if id == "" {
		return model.Video{}, false
	}
	v := model.Video{ID: id, Title: text(r["title"])}
	for _, key := range []string{"ownerText", "longBylineText", "shortBylineText"} {
		if run := get(r, key, "runs", 0); run != nil {
			v.ChannelName = str(get(run, "text"))
			v.ChannelID = str(get(run, "navigationEndpoint", "browseEndpoint", "browseId"))
			if v.ChannelName != "" {
				break
			}
		}
	}
	views := text(r["viewCountText"])
	if views == "" {
		views = text(r["shortViewCountText"])
	}
	v.Views = parseCount(views)
	if n := parseCount(str(r["lengthSeconds"])); n > 0 {
		v.DurationS = int(n)
	} else {
		v.DurationS = parseDuration(text(r["lengthText"]))
	}
	setPublished(&v, text(r["publishedTimeText"]), now)
	v.Thumbnail = bestThumb(asList(get(r, "thumbnail", "thumbnails")))

	nav := asMap(r["navigationEndpoint"])
	url := str(get(nav, "commandMetadata", "webCommandMetadata", "url"))
	v.IsShort = nav["reelWatchEndpoint"] != nil || strings.HasPrefix(url, "/shorts/")
	for _, b := range asList(r["badges"]) {
		if strings.Contains(str(get(b, "metadataBadgeRenderer", "style")), "LIVE_NOW") {
			v.IsLive = true
		}
	}
	for _, o := range asList(r["thumbnailOverlays"]) {
		switch str(get(o, "thumbnailOverlayTimeStatusRenderer", "style")) {
		case "LIVE":
			v.IsLive = true
		case "SHORTS":
			v.IsShort = true
		}
	}
	v.URL = yt.VideoURL(id, v.IsShort)
	return v, true
}

// parseLockup reads the newer lockupViewModel layout. Only video lockups count;
// playlists, courses and channels share the container and are skipped.
func parseLockup(l map[string]any, now time.Time) (model.Video, bool) {
	if str(l["contentType"]) != "LOCKUP_CONTENT_TYPE_VIDEO" {
		return model.Video{}, false
	}
	id := str(l["contentId"])
	if id == "" {
		return model.Video{}, false
	}
	meta := asMap(get(l, "metadata", "lockupMetadataViewModel"))
	v := model.Video{ID: id, Title: text(meta["title"])}
	for _, row := range asList(get(meta, "metadata", "contentMetadataViewModel", "metadataRows")) {
		for _, part := range asList(get(row, "metadataParts")) {
			s := strings.TrimSpace(text(get(part, "text")))
			switch {
			case s == "":
			case strings.Contains(s, "watching"):
				v.Views = parseCount(s)
				v.IsLive = true
			case strings.Contains(s, " views") || strings.HasPrefix(s, "No views") || strings.HasSuffix(s, " view"):
				v.Views = parseCount(s)
			case agoRe.MatchString(s):
				setPublished(&v, s, now)
			case v.ChannelName == "" && !strings.HasPrefix(s, "Streamed") && !strings.HasPrefix(s, "Premiere"):
				v.ChannelName = s
				v.ChannelID = str(findFirst(get(part, "text", "commandRuns"), "browseId"))
			}
		}
	}
	img := get(l, "contentImage")
	v.Thumbnail = bestThumb(asList(findFirst(img, "sources")))
	for _, b := range findAll(img, "thumbnailBadgeViewModel") {
		t := strings.TrimSpace(str(get(b, "text")))
		if d := parseDuration(t); d > 0 {
			v.DurationS = d
		} else if strings.EqualFold(t, "live") {
			v.IsLive = true
		}
	}
	v.URL = yt.VideoURL(id, false)
	return v, true
}

// parseShortsLockup reads a Short as search and the Shorts tab show it: title,
// view count and a vertical thumbnail, but no channel or duration.
func parseShortsLockup(s map[string]any) (model.Video, bool) {
	id := str(findFirst(s["onTap"], "videoId"))
	if id == "" {
		id = strings.TrimPrefix(str(s["entityId"]), "shorts-shelf-item-")
	}
	if id == "" || strings.HasPrefix(id, "shorts-") {
		return model.Video{}, false
	}
	v := model.Video{ID: id, IsShort: true, URL: yt.VideoURL(id, true)}
	v.Title = text(get(s, "overlayMetadata", "primaryText"))
	v.Views = parseCount(text(get(s, "overlayMetadata", "secondaryText")))
	v.Thumbnail = bestThumb(asList(findFirst(s["thumbnailViewModel"], "sources")))
	return v, true
}

// nextToken returns the continuation token that loads the next page: the last
// continuationItemRenderer below root that is not inside a comment's replies.
func nextToken(root any) string {
	tok := ""
	walk(root, func(k string, val any) bool {
		switch k {
		case "continuationItemRenderer":
			if t := str(get(val, "continuationEndpoint", "continuationCommand", "token")); t != "" {
				tok = t
			}
			return false
		case "commentRepliesRenderer":
			return false
		}
		return true
	})
	return tok
}

// dedupeVideos drops repeated ids, keeping the first.
func dedupeVideos(in []model.Video, seen map[string]bool) []model.Video {
	var out []model.Video
	for _, v := range in {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out
}
