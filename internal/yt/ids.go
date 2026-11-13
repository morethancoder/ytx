package yt

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
)

var (
	videoIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	channelIDRe  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	playlistIDRe = regexp.MustCompile(`^(PL|UU|LL|FL|OL|RD)[A-Za-z0-9_-]{10,}$`)
	handleRe     = regexp.MustCompile(`^@[A-Za-z0-9._-]{3,30}$`)
)

// VideoID takes an id or any YouTube video URL (watch, youtu.be, shorts, live, embed).
func VideoID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if videoIDRe.MatchString(s) {
		return s, nil
	}
	u, err := url.Parse(s)
	if err == nil && u.Host != "" {
		host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		host = strings.TrimPrefix(host, "m.")
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		switch {
		case host == "youtu.be" && len(parts) > 0:
			s = parts[0]
		case strings.HasSuffix(host, "youtube.com") && u.Query().Get("v") != "":
			s = u.Query().Get("v")
		case strings.HasSuffix(host, "youtube.com") && len(parts) >= 2 &&
			(parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed" || parts[0] == "v"):
			s = parts[1]
		}
		if videoIDRe.MatchString(s) {
			return s, nil
		}
	}
	return "", apperr.New(apperr.InvalidInput, "%q is not a YouTube video id or URL", s)
}

// ChannelRef normalises a channel id, @handle or channel URL to "UC..." or "@handle".
func ChannelRef(s string) (string, error) {
	s = strings.TrimSpace(s)
	if channelIDRe.MatchString(s) || handleRe.MatchString(s) {
		return s, nil
	}
	if handleRe.MatchString("@"+s) && !strings.Contains(s, "/") && !strings.Contains(s, ".") {
		return "@" + s, nil
	}
	u, err := url.Parse(s)
	if err == nil && u.Host != "" && strings.HasSuffix(strings.ToLower(u.Host), "youtube.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 1 && handleRe.MatchString(parts[0]) {
			return parts[0], nil
		}
		if len(parts) >= 2 && parts[0] == "channel" && channelIDRe.MatchString(parts[1]) {
			return parts[1], nil
		}
	}
	return "", apperr.New(apperr.InvalidInput, "%q is not a channel id (UC...), @handle or channel URL", s)
}

// PlaylistID takes an id or a playlist URL.
func PlaylistID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if playlistIDRe.MatchString(s) {
		return s, nil
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		if l := u.Query().Get("list"); playlistIDRe.MatchString(l) {
			return l, nil
		}
	}
	return "", apperr.New(apperr.InvalidInput, "%q is not a playlist id or URL", s)
}

// VideoURL is a video's watch URL (or Shorts URL).
func VideoURL(id string, short bool) string {
	if short {
		return "https://www.youtube.com/shorts/" + id
	}
	return "https://www.youtube.com/watch?v=" + id
}

// ChannelURL is a channel's URL from its id or handle.
func ChannelURL(ref string) string {
	if strings.HasPrefix(ref, "@") {
		return "https://www.youtube.com/" + ref
	}
	return "https://www.youtube.com/channel/" + ref
}
