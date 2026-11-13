package keyless

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
)

// The WEB client's caption URLs now need a PO token (they come back empty).
// The Android and iOS player clients still hand out caption URLs that work
// without one, so transcripts come from them; yt-dlp is the last resort.
const (
	androidUA = "com.google.android.youtube/20.10.38 (Linux; U; Android 11) gzip"
	iosUA     = "com.google.ios.youtube/20.10.4 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)"
)

func androidClient() obj {
	return obj{"clientName": "ANDROID", "clientVersion": "20.10.38", "androidSdkVersion": 30, "hl": "en", "gl": "US"}
}

func iosClient() obj {
	return obj{"clientName": "IOS", "clientVersion": "20.10.4", "deviceModel": "iPhone16,2",
		"osVersion": "18.3.2.22D82", "hl": "en", "gl": "US"}
}

// track is one caption track from the player response.
type track struct {
	URL, Lang string
	Auto      bool
}

// Transcript returns lang's caption track, else the video's original-language
// track, preferring hand-made captions over auto-generated ones.
func (c *Client) Transcript(ctx context.Context, videoID, lang string) (*model.Transcript, error) {
	var firstErr error
	noCaptions := 0
	attempts := []struct {
		client obj
		ua     string
	}{{androidClient(), androidUA}, {iosClient(), iosUA}}
	for _, a := range attempts {
		resp, err := c.post(ctx, request{endpoint: "player", client: a.client, userAgnt: a.ua,
			body: obj{"videoId": videoID, "contentCheckOk": true, "racyCheckOk": true}})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if perr := playabilityErr(resp); perr != nil {
			if isAppErr(perr, apperr.NotFound) {
				return nil, perr
			}
			if firstErr == nil {
				firstErr = perr
			}
			continue
		}
		if str(get(resp, "playabilityStatus", "status")) != "OK" {
			if firstErr == nil {
				firstErr = apperr.New(apperr.YouTubeError, "player refused the %s client: %s",
					str(a.client["clientName"]), str(get(resp, "playabilityStatus", "reason")))
			}
			continue
		}
		tracks, original := parseTracks(resp)
		pick := chooseTrack(tracks, lang, original)
		if pick == nil {
			noCaptions++
			continue
		}
		tr, err := c.fetchTrack(ctx, videoID, *pick, a.ua)
		if err == nil {
			return tr, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if noCaptions == len(attempts) {
		return nil, apperr.New(apperr.NoTranscript, "video %s has no captions", videoID)
	}
	// Neither client produced a transcript: try yt-dlp before giving up.
	if tr, err := c.transcriptViaYTDLP(ctx, videoID, lang); err == nil {
		return tr, nil
	} else if isAppErr(err, apperr.NoTranscript) && firstErr == nil {
		return nil, err
	}
	if firstErr == nil {
		firstErr = apperr.New(apperr.YouTubeError, "no caption track could be downloaded")
	}
	return nil, firstErr
}

// parseTracks lists the caption tracks and the language of the original track.
func parseTracks(resp obj) ([]track, string) {
	r := asMap(get(resp, "captions", "playerCaptionsTracklistRenderer"))
	var tracks []track
	for _, t := range asList(r["captionTracks"]) {
		tr := track{URL: str(get(t, "baseUrl")), Lang: str(get(t, "languageCode")), Auto: str(get(t, "kind")) == "asr"}
		if tr.URL != "" && tr.Lang != "" {
			tracks = append(tracks, tr)
		}
	}
	original := ""
	// A video with auto-dubbed audio lists one audio track per dub. The default
	// audio track is the original; its id starts with the spoken language
	// ("en-US.4"), while dubs end in ".10". Prefer it over everything else.
	if di, ok := r["defaultAudioTrackIndex"].(float64); ok {
		id := str(get(r, "audioTracks", int(di), "audioTrackId"))
		if i := strings.Index(id, "."); i > 0 {
			id = id[:i]
		}
		for _, t := range tracks {
			if id != "" && strings.EqualFold(t.Lang, id) {
				original = t.Lang
				break
			}
		}
		if original == "" {
			for _, t := range tracks {
				if id != "" && baseLang(t.Lang) == baseLang(id) {
					original = t.Lang
					break
				}
			}
		}
	}
	if idx, ok := get(r, "audioTracks", 0, "defaultCaptionTrackIndex").(float64); ok && original == "" {
		if i := int(idx); i >= 0 && i < len(asList(r["captionTracks"])) {
			original = str(get(r, "captionTracks", i, "languageCode"))
		}
	}
	if original == "" {
		// The auto-generated track is always in the spoken language.
		for _, t := range tracks {
			if t.Auto {
				original = t.Lang
				break
			}
		}
	}
	if original == "" && len(tracks) > 0 {
		original = tracks[0].Lang
	}
	return tracks, original
}

func baseLang(l string) string {
	l = strings.ToLower(l)
	if i := strings.IndexAny(l, "-_"); i > 0 {
		return l[:i]
	}
	return l
}

// chooseTrack picks lang's track (an exact code first, then the same base
// language), else the original-language track. Hand-made beats auto-generated.
func chooseTrack(tracks []track, lang, original string) *track {
	best := func(match func(track) bool) *track {
		var found *track
		for i := range tracks {
			t := &tracks[i]
			if !match(*t) {
				continue
			}
			if found == nil || (found.Auto && !t.Auto) {
				found = t
			}
		}
		return found
	}
	if lang != "" {
		if t := best(func(t track) bool { return strings.EqualFold(t.Lang, lang) }); t != nil {
			return t
		}
		if t := best(func(t track) bool { return baseLang(t.Lang) == baseLang(lang) }); t != nil {
			return t
		}
	}
	return best(func(t track) bool { return strings.EqualFold(t.Lang, original) })
}

// fetchTrack downloads one track as json3 and turns it into a Transcript.
func (c *Client) fetchTrack(ctx context.Context, videoID string, t track, ua string) (*model.Transcript, error) {
	u, err := url.Parse(t.URL)
	if err != nil {
		return nil, apperr.Wrap(apperr.YouTubeError, err, "caption URL")
	}
	if b, perr := url.Parse(c.Base); perr == nil && b.Host != "" {
		u.Scheme, u.Host = b.Scheme, b.Host // the same host in production; lets tests redirect it
	}
	q := u.Query()
	q.Set("fmt", "json3")
	u.RawQuery = q.Encode()
	raw, status, err := c.getRaw(ctx, u.String(), map[string]string{"User-Agent": ua})
	if err != nil {
		return nil, err
	}
	if status != 200 || len(raw) == 0 {
		return nil, apperr.New(apperr.YouTubeError, "caption download answered HTTP %d with %d bytes", status, len(raw))
	}
	tr, err := parseJSON3(raw)
	if err != nil {
		return nil, err
	}
	tr.VideoID, tr.Language, tr.Generated = videoID, t.Lang, t.Auto
	return tr, nil
}

type json3 struct {
	Events []struct {
		Start float64 `json:"tStartMs"`
		Dur   float64 `json:"dDurationMs"`
		Segs  []struct {
			Text string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

// parseJSON3 reads YouTube's json3 caption format.
func parseJSON3(raw []byte) (*model.Transcript, error) {
	var d json3
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, apperr.Wrap(apperr.YouTubeError, err, "captions were not json3")
	}
	tr := &model.Transcript{Segments: []model.Segment{}}
	var parts []string
	for _, e := range d.Events {
		var b strings.Builder
		for _, s := range e.Segs {
			b.WriteString(s.Text)
		}
		line := strings.Join(strings.Fields(strings.ReplaceAll(b.String(), "\n", " ")), " ")
		if line == "" {
			continue
		}
		tr.Segments = append(tr.Segments, model.Segment{StartS: e.Start / 1000, DurS: e.Dur / 1000, Text: line})
		parts = append(parts, line)
	}
	if len(tr.Segments) == 0 {
		return nil, apperr.New(apperr.NoTranscript, "the caption track is empty")
	}
	tr.Text = strings.Join(parts, " ")
	tr.Words = len(strings.Fields(tr.Text))
	return tr, nil
}

// transcriptViaYTDLP is the last resort when YouTube refuses caption URLs to
// both clients. It runs yt-dlp into a temp dir and always removes it.
func (c *Client) transcriptViaYTDLP(ctx context.Context, videoID, lang string) (*model.Transcript, error) {
	bin := c.YTDLP
	if bin == "off" {
		return nil, apperr.New(apperr.ToolMissing, "yt-dlp fallback is off")
	}
	if bin == "" {
		bin = "yt-dlp"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, apperr.New(apperr.ToolMissing, "yt-dlp is not installed")
	}
	dir, err := os.MkdirTemp("", "ytx-subs-")
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "temp dir")
	}
	defer os.RemoveAll(dir)
	langs := "en.*,.*-orig"
	if lang != "" {
		langs = lang + ".*," + langs
	}
	// Hand-made subtitles first, then auto-generated ones.
	for _, auto := range []bool{false, true} {
		flag := "--write-subs"
		if auto {
			flag = "--write-auto-subs"
		}
		cmd := exec.CommandContext(ctx, path, "--skip-download", flag, "--sub-format", "json3",
			"--sub-langs", langs, "--no-warnings", "--quiet", "--no-playlist",
			"-o", filepath.Join(dir, "%(id)s.%(ext)s"), "https://www.youtube.com/watch?v="+videoID)
		cmd.Stdin = nil
		_ = cmd.Run() // a missing track is not an error; the files tell
		files, _ := filepath.Glob(filepath.Join(dir, videoID+".*.json3"))
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)
		var tracks []track
		for _, f := range files {
			l := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), videoID+"."), ".json3")
			tracks = append(tracks, track{URL: f, Lang: l, Auto: auto})
		}
		original := ""
		for _, t := range tracks {
			if strings.HasSuffix(t.Lang, "-orig") {
				original = t.Lang
			}
		}
		if original == "" {
			original = tracks[0].Lang
		}
		pick := chooseTrack(tracks, lang, original)
		if pick == nil {
			continue
		}
		raw, rerr := os.ReadFile(pick.URL)
		if rerr != nil {
			continue
		}
		tr, perr := parseJSON3(raw)
		if perr != nil {
			continue
		}
		tr.VideoID, tr.Language, tr.Generated = videoID, strings.TrimSuffix(pick.Lang, "-orig"), pick.Auto
		return tr, nil
	}
	return nil, apperr.New(apperr.NoTranscript, "yt-dlp found no captions for %s", videoID)
}
