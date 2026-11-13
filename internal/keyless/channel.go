package keyless

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// Tab parameters for browse. They are fixed protobuf blobs that YouTube itself
// links to from every channel page.
const (
	tabVideos = "EgZ2aWRlb3PyBgQKAjoA"
	tabShorts = "EgZzaG9ydHPyBgUKA5oBAA=="
)

var (
	channelIDRe  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	videoCountRe = regexp.MustCompile(`^([\d.,]+[KMB]?)\s+videos?$`)
	subsRe       = regexp.MustCompile(`(?i)^[\d.,]+\s*[KMB]?\s+subscribers?$`)
)

// resolveChannel turns an id, @handle or channel URL into a UC... id.
func (c *Client) resolveChannel(ctx context.Context, ref string) (string, error) {
	if channelIDRe.MatchString(ref) {
		return ref, nil
	}
	target := ref
	if !strings.Contains(ref, "/") {
		target = "https://www.youtube.com/@" + strings.TrimPrefix(ref, "@")
	}
	resp, err := c.post(ctx, request{endpoint: "navigation/resolve_url", client: webClient(""), body: obj{"url": target}})
	if err != nil {
		if isAppErr(err, apperr.NotFound) {
			return "", apperr.New(apperr.NotFound, "channel %s not found", ref)
		}
		return "", err
	}
	id := str(get(resp, "endpoint", "browseEndpoint", "browseId"))
	if id == "" {
		return "", apperr.New(apperr.NotFound, "channel %s not found", ref)
	}
	return id, nil
}

// Channel reads a channel's header and its latest uploads (Videos tab, or the
// Shorts tab when o.Shorts).
func (c *Client) Channel(ctx context.Context, ref string, o yt.ChannelOpts) (*model.Channel, error) {
	limit := clampLimit(o.Limit, 30)
	id, err := c.resolveChannel(ctx, ref)
	if err != nil {
		return nil, err
	}
	params := tabVideos
	if o.Shorts {
		params = tabShorts
	}
	body := obj{"browseId": id}
	if p, derr := url.QueryUnescape(params); derr == nil {
		body["params"] = p
	}
	resp, err := c.post(ctx, request{endpoint: "browse", client: webClient(""), body: body})
	if err != nil {
		return nil, err
	}
	if err := browseAlert(resp, "channel "+ref); err != nil {
		return nil, err
	}
	ch, err := parseChannelHeader(resp, id, ref)
	if err != nil {
		return nil, err
	}
	now := c.now()
	seen := map[string]bool{}
	ch.Recent = []model.Video{}
	add := func(root any) {
		for _, v := range dedupeVideos(collectVideos(root, now), seen) {
			if o.Shorts {
				v.IsShort = true
				v.URL = yt.VideoURL(v.ID, true)
			}
			if v.ChannelID == "" {
				v.ChannelID = ch.ID
			}
			if v.ChannelName == "" {
				v.ChannelName = ch.Name
			}
			ch.Recent = append(ch.Recent, v)
		}
	}
	root := resp["contents"]
	add(root)
	token := nextToken(root)
	for page := 0; page < maxSearchPages && token != "" && len(ch.Recent) < limit; page++ {
		more, err := c.post(ctx, request{endpoint: "browse", client: webClient(""), body: obj{"continuation": token}})
		if err != nil {
			return nil, err
		}
		add(more["onResponseReceivedActions"])
		token = nextToken(more["onResponseReceivedActions"])
	}
	if len(ch.Recent) > limit {
		ch.Recent = ch.Recent[:limit]
	}
	return ch, nil
}

// browseAlert turns a page-level error alert ("This channel does not exist")
// into not_found.
func browseAlert(resp obj, what string) error {
	for _, a := range asList(resp["alerts"]) {
		r := asMap(get(a, "alertRenderer"))
		if str(r["type"]) == "ERROR" {
			return apperr.New(apperr.NotFound, "%s not found: %s", what, strings.TrimSpace(text(r["text"])))
		}
	}
	return nil
}

func parseChannelHeader(resp obj, id, ref string) (*model.Channel, error) {
	meta := asMap(get(resp, "metadata", "channelMetadataRenderer"))
	header := resp["header"]
	if meta == nil && header == nil {
		return nil, layoutErr("channel page has no header or metadata")
	}
	ch := &model.Channel{ID: orDefault(str(meta["externalId"]), id)}
	ch.Name = str(meta["title"])
	ch.Description = strings.TrimSpace(str(meta["description"]))
	ch.Thumbnail = bestThumb(asList(get(meta, "avatar", "thumbnails")))
	if ch.Thumbnail == "" {
		ch.Thumbnail = bestThumb(asList(findFirst(header, "sources")))
	}
	if u, err := url.Parse(str(meta["vanityChannelUrl"])); err == nil {
		if h := strings.Trim(u.Path, "/"); strings.HasPrefix(h, "@") {
			ch.Handle = h
		}
	}
	for _, s := range textsIn(header) {
		s = strings.TrimSpace(s)
		switch {
		case ch.Handle == "" && strings.HasPrefix(s, "@") && !strings.Contains(s, " "):
			ch.Handle = s
		case subsRe.MatchString(s):
			ch.Subscribers = parseCount(s)
		case videoCountRe.MatchString(s):
			ch.VideoCount = parseCount(s)
		}
	}
	if ch.Handle == "" && strings.HasPrefix(ref, "@") {
		ch.Handle = ref
	}
	if ch.Name == "" {
		ch.Name = orDefault(text(get(header, "c4TabbedHeaderRenderer", "title")), str(get(header, "pageHeaderRenderer", "pageTitle")))
	}
	if ch.Name == "" {
		return nil, layoutErr("channel page has no title")
	}
	if ch.Handle != "" {
		ch.URL = yt.ChannelURL(ch.Handle)
	} else {
		ch.URL = yt.ChannelURL(ch.ID)
	}
	return ch, nil
}
