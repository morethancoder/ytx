package keyless

import (
	"context"
	"regexp"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
)

var playlistCountRe = regexp.MustCompile(`^([\d,]+)\s+videos?$`)

// Playlist reads a playlist's header and its videos up to limit.
func (c *Client) Playlist(ctx context.Context, id string, limit int) (*model.Playlist, []model.Video, error) {
	limit = clampLimit(limit, 50)
	resp, err := c.post(ctx, request{endpoint: "browse", client: webClient(""), body: obj{"browseId": "VL" + id}})
	if err != nil {
		// YouTube answers an unknown playlist with 400 "invalid argument".
		var ae *apperr.Error
		if isAppErr(err, apperr.YouTubeError) && asAppErr(err, &ae) && strings.Contains(ae.Message, "HTTP 400") {
			return nil, nil, apperr.New(apperr.NotFound, "playlist %s not found", id)
		}
		return nil, nil, err
	}
	if err := browseAlert(resp, "playlist "+id); err != nil {
		return nil, nil, err
	}
	pl := &model.Playlist{ID: id, URL: "https://www.youtube.com/playlist?list=" + id}
	hdr := resp["header"]
	pl.Title = orDefault(str(get(resp, "metadata", "playlistMetadataRenderer", "title")),
		orDefault(text(findFirst(hdr, "title")), str(get(hdr, "pageHeaderRenderer", "pageTitle"))))
	if pl.Title == "" {
		return nil, nil, layoutErr("playlist page has no title")
	}
	pl.Description = strings.TrimSpace(text(findFirst(get(hdr, "pageHeaderRenderer", "content", "pageHeaderViewModel", "description"), "description")))
	for _, s := range textsIn(hdr) {
		s = strings.TrimSpace(s)
		if m := playlistCountRe.FindStringSubmatch(s); m != nil {
			pl.ItemCount = parseCount(m[1])
		}
		switch s {
		case "Public", "Private", "Unlisted":
			pl.Privacy = strings.ToLower(s)
		}
	}
	now := c.now()
	seen := map[string]bool{}
	videos := []model.Video{}
	root := resp["contents"]
	videos = append(videos, dedupeVideos(collectVideos(root, now), seen)...)
	token := nextToken(root)
	for page := 0; page < maxSearchPages && token != "" && len(videos) < limit; page++ {
		more, err := c.post(ctx, request{endpoint: "browse", client: webClient(""), body: obj{"continuation": token}})
		if err != nil {
			return nil, nil, err
		}
		actions := more["onResponseReceivedActions"]
		videos = append(videos, dedupeVideos(collectVideos(actions, now), seen)...)
		token = nextToken(actions)
	}
	if len(videos) > limit {
		videos = videos[:limit]
	}
	return pl, videos, nil
}
