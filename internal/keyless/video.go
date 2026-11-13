package keyless

import (
	"context"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// playabilityErr maps the player's playabilityStatus to an app error. It
// returns nil for statuses that still carry usable data.
func playabilityErr(resp obj) error {
	ps := asMap(resp["playabilityStatus"])
	reason := strings.TrimSpace(str(ps["reason"]))
	switch str(ps["status"]) {
	case "ERROR":
		return apperr.New(apperr.NotFound, "video not found: %s", orDefault(reason, "unavailable"))
	case "LOGIN_REQUIRED":
		return apperr.New(apperr.Forbidden, "video needs a login: %s", orDefault(reason, "sign in required"))
	}
	return nil
}

// Video reads the WEB player answer, which holds the exact publish date,
// likes, tags and description even when it refuses to play the video (YouTube
// now answers WEB without a PO token with "page needs to be reloaded" but
// still fills videoDetails and microformat). /next adds the comment count.
func (c *Client) Video(ctx context.Context, id string) (*model.Video, error) {
	resp, err := c.post(ctx, request{endpoint: "player", client: webClient("US"),
		body: obj{"videoId": id, "contentCheckOk": true, "racyCheckOk": true}})
	if err != nil {
		return nil, err
	}
	if resp["videoDetails"] == nil && resp["microformat"] == nil {
		if perr := playabilityErr(resp); perr != nil {
			return nil, perr
		}
		// WEB gave nothing; the Android client still returns videoDetails.
		resp, err = c.post(ctx, request{endpoint: "player", client: androidClient(), userAgnt: androidUA,
			body: obj{"videoId": id, "contentCheckOk": true, "racyCheckOk": true}})
		if err != nil {
			return nil, err
		}
		if perr := playabilityErr(resp); perr != nil {
			return nil, perr
		}
		if resp["videoDetails"] == nil {
			return nil, layoutErr("player answer has no videoDetails")
		}
	}
	v := parsePlayerVideo(resp, id)
	if v.ID == "" {
		return nil, layoutErr("player answer has no video id")
	}
	// The comment count is a nice extra: ignore failures.
	if next, nerr := c.post(ctx, request{endpoint: "next", client: webClient("US"), body: obj{"videoId": id}}); nerr == nil {
		v.Comments = commentCountFromNext(next)
	}
	return &v, nil
}

// parsePlayerVideo fills a Video from a player response.
func parsePlayerVideo(resp obj, wantID string) model.Video {
	vd := asMap(resp["videoDetails"])
	mf := asMap(get(resp, "microformat", "playerMicroformatRenderer"))
	v := model.Video{ID: orDefault(str(vd["videoId"]), orDefault(str(mf["externalVideoId"]), wantID))}
	v.Title = orDefault(str(vd["title"]), text(mf["title"]))
	v.ChannelID = orDefault(str(vd["channelId"]), str(mf["externalChannelId"]))
	v.ChannelName = orDefault(str(vd["author"]), str(mf["ownerChannelName"]))
	v.Views = parseCount(orDefault(str(vd["viewCount"]), str(mf["viewCount"])))
	v.Likes = parseCount(str(mf["likeCount"]))
	v.DurationS = int(parseCount(orDefault(str(vd["lengthSeconds"]), str(mf["lengthSeconds"]))))
	v.Description = orDefault(str(vd["shortDescription"]), text(mf["description"]))
	for _, k := range asList(vd["keywords"]) {
		if s := str(k); s != "" {
			v.Tags = append(v.Tags, s)
		}
	}
	if d := orDefault(str(mf["publishDate"]), str(mf["uploadDate"])); d != "" {
		if t, err := time.Parse(time.RFC3339, d); err == nil {
			v.Published = t
		} else if t, err := time.Parse("2006-01-02", d); err == nil {
			v.Published = t
		}
	}
	v.Thumbnail = bestThumb(asList(get(vd, "thumbnail", "thumbnails")))
	if v.Thumbnail == "" {
		v.Thumbnail = bestThumb(asList(get(mf, "thumbnail", "thumbnails")))
	}
	if live, _ := vd["isLive"].(bool); live {
		v.IsLive = true
	}
	if live, _ := get(mf, "liveBroadcastDetails", "isLiveNow").(bool); live {
		v.IsLive = true
	}
	v.URL = yt.VideoURL(v.ID, false)
	return v
}

// commentCountFromNext reads the comment total from the comments panel header.
// YouTube only gives the short form there ("2.4M"), so a big count is approximate.
func commentCountFromNext(next obj) int64 {
	for _, p := range asList(next["engagementPanels"]) {
		panel := asMap(get(p, "engagementPanelSectionListRenderer"))
		if str(panel["panelIdentifier"]) == "engagement-panel-comments-section" {
			return parseCount(text(get(panel, "header", "engagementPanelTitleHeaderRenderer", "contextualInfo")))
		}
	}
	return 0
}
