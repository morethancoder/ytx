package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/yt"
)

// Pool units of the write calls (Data API quota table).
const (
	costList   = 1
	costWrite  = 50
	costCaptns = 400
)

// plan is what a write returns on a dry run: what it will do, as data.
func (s *session) plan(action string, target, changes map[string]any, units any) *ops.Result {
	return &ops.Result{
		Data: map[string]any{"action": action, "target": target, "changes": changes, "quota_units": units},
		Meta: ops.Meta{Source: "oauth", QuotaUsed: s.used},
	}
}

func (s *session) result(data any, warnings ...string) *ops.Result {
	return &ops.Result{Data: data, Meta: ops.Meta{Source: "oauth", QuotaUsed: s.used}, Warnings: warnings}
}

func videoTarget(v *videoItem) map[string]any {
	t := map[string]any{"video": v.ID, "url": yt.VideoURL(v.ID, false), "title": v.title()}
	if p := v.privacy(); p != "" {
		t["privacy"] = p
	}
	return t
}

// ---- video.edit

var (
	snippetWritable = []string{"title", "description", "tags", "categoryId", "defaultLanguage", "defaultAudioLanguage"}
	statusWritable  = []string{"privacyStatus", "embeddable", "license", "publicStatsViewable", "publishAt", "selfDeclaredMadeForKids", "containsSyntheticMedia"}
)

// writable copies only the fields videos.update accepts, so read-only ones
// (thumbnails, channelId, uploadStatus...) aren't sent back.
func writable(m map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func same(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func edit(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	type change struct {
		part, key, field string
		val              any
	}
	var want []change
	if in.Has("title") {
		t, err := checkTitle(in.Str("title"), maxTitle)
		if err != nil {
			return nil, err
		}
		want = append(want, change{"snippet", "title", "title", t})
	}
	if in.Has("description") {
		d, err := description(in.Str("description"))
		if err != nil {
			return nil, err
		}
		want = append(want, change{"snippet", "description", "description", d})
	}
	if in.Has("tags") {
		tags := in.List("tags")
		if err := checkTags(tags); err != nil {
			return nil, err
		}
		if tags == nil {
			tags = []string{}
		}
		want = append(want, change{"snippet", "tags", "tags", tags})
	}
	if in.Has("category") {
		c, err := checkCategory(in.Str("category"))
		if err != nil {
			return nil, err
		}
		want = append(want, change{"snippet", "categoryId", "category", c})
	}
	privacy := ""
	if in.Has("privacy") {
		if privacy, err = checkPrivacy(in.Str("privacy")); err != nil {
			return nil, err
		}
	}
	pub := ""
	if in.Has("publish-at") {
		if pub, err = publishAt(in.Str("publish-at")); err != nil {
			return nil, err
		}
		if privacy != "" && privacy != "private" {
			return nil, apperr.New(apperr.InvalidInput, "--publish-at needs --privacy private (YouTube makes the video public at that time)")
		}
		// YouTube only schedules private videos, so a schedule implies private.
		privacy = "private"
	}
	if privacy != "" {
		want = append(want, change{"status", "privacyStatus", "privacy", privacy})
	}
	if pub != "" {
		want = append(want, change{"status", "publishAt", "publish_at", pub})
	}
	if len(want) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "nothing to change: pass --title, --description, --tags, --privacy, --publish-at or --category")
	}

	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.video(ctx, id, "snippet,status")
	if err != nil {
		return nil, err
	}
	parts := map[string]map[string]any{"snippet": writable(v.Snippet, snippetWritable), "status": writable(v.Status, statusWritable)}
	touched := map[string]bool{}
	diff := map[string]any{}
	for _, c := range want {
		before := parts[c.part][c.key]
		if !same(before, c.val) {
			diff[c.field] = map[string]any{"before": before, "after": c.val}
			touched[c.part] = true
		}
		parts[c.part][c.key] = c.val
	}
	// A schedule left on a video made unlisted or public would be refused.
	if privacy != "" && privacy != "private" {
		if old, ok := parts["status"]["publishAt"]; ok {
			delete(parts["status"], "publishAt")
			diff["publish_at"] = map[string]any{"before": old, "after": nil}
			touched["status"] = true
		}
	}
	if len(touched) == 0 {
		if in.DryRun {
			r := s.plan("edit video", videoTarget(v), diff, 0)
			r.Warnings = []string{"the video already has these values; nothing would change"}
			return r, nil
		}
		return s.result(map[string]any{"video": id, "changed": []string{}}, "the video already has these values; nothing was changed"), nil
	}
	if in.DryRun {
		return s.plan("edit video", videoTarget(v), diff, costList+costWrite), nil
	}

	body := map[string]any{"id": id}
	var partNames []string
	for _, p := range []string{"snippet", "status"} {
		if touched[p] {
			body[p] = parts[p]
			partNames = append(partNames, p)
		}
	}
	var out videoItem
	u := endpointURL(dataBase, "/videos", url.Values{"part": {strings.Join(partNames, ",")}})
	if err := s.writeJSON(ctx, http.MethodPut, u, body, costWrite, &out); err != nil {
		return nil, err
	}
	changed := make([]string, 0, len(diff))
	for k := range diff {
		changed = append(changed, k)
	}
	data := map[string]any{"video": id, "url": yt.VideoURL(id, false), "changed": changed}
	if t := out.title(); t != "" {
		data["title"] = t
	}
	if p := out.privacy(); p != "" {
		data["privacy"] = p
	}
	return s.result(data), nil
}

// ---- video.thumbnail

func thumbnail(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	img, ctype, err := image(in.Str("file"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		v, err := s.video(ctx, id, "snippet,status")
		if err != nil {
			return nil, err
		}
		return s.plan("set thumbnail", videoTarget(v),
			map[string]any{"thumbnail": map[string]any{"file": in.Str("file"), "bytes": len(img), "type": ctype}}, costWrite), nil
	}
	if err := s.setThumbnail(ctx, id, img, ctype); err != nil {
		return nil, err
	}
	return s.result(map[string]any{"video": id, "url": yt.VideoURL(id, false), "thumbnail_set": true}), nil
}

func (s *session) setThumbnail(ctx context.Context, id string, img []byte, ctype string) error {
	u := endpointURL(uploadBase, "/thumbnails/set", url.Values{"videoId": {id}, "uploadType": {"media"}})
	_, _, err := s.send(ctx, request{method: http.MethodPost, url: u, header: http.Header{"Content-Type": {ctype}}, body: img, units: costWrite})
	return err
}

// ---- video.rate

func rate(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	rating := strings.ToLower(strings.TrimSpace(in.Str("rating")))
	if rating != "like" && rating != "dislike" && rating != "none" {
		return nil, apperr.New(apperr.InvalidInput, "rating must be like, dislike or none")
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		v, err := s.video(ctx, id, "snippet")
		if err != nil {
			return nil, err
		}
		return s.plan("rate video", videoTarget(v), map[string]any{"rating": rating}, costWrite), nil
	}
	u := endpointURL(dataBase, "/videos/rate", url.Values{"id": {id}, "rating": {rating}})
	if err := s.writeJSON(ctx, http.MethodPost, u, nil, costWrite, nil); err != nil {
		return nil, err
	}
	return s.result(map[string]any{"video": id, "rating": rating}), nil
}

// ---- playlists

func playlistCreate(ctx context.Context, in ops.Input) (*ops.Result, error) {
	title, err := checkTitle(in.Str("title"), maxPlaylistTtl)
	if err != nil {
		return nil, err
	}
	desc := ""
	if in.Has("description") {
		if desc, err = description(in.Str("description")); err != nil {
			return nil, err
		}
	}
	privacy := "private"
	if in.Str("privacy") != "" {
		if privacy, err = checkPrivacy(in.Str("privacy")); err != nil {
			return nil, err
		}
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		return s.plan("create playlist", map[string]any{"channel": "yours"},
			map[string]any{"title": title, "description": desc, "privacy": privacy}, costWrite), nil
	}
	body := map[string]any{"snippet": map[string]any{"title": title, "description": desc}, "status": map[string]any{"privacyStatus": privacy}}
	var out struct {
		ID string `json:"id"`
	}
	u := endpointURL(dataBase, "/playlists", url.Values{"part": {"snippet,status"}})
	if err := s.writeJSON(ctx, http.MethodPost, u, body, costWrite, &out); err != nil {
		return nil, err
	}
	return s.result(model.Playlist{ID: out.ID, URL: "https://www.youtube.com/playlist?list=" + out.ID, Title: title, Description: desc, Privacy: privacy}), nil
}

func (s *session) playlistTitle(ctx context.Context, id string) (string, error) {
	var r struct {
		Items []struct {
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := s.getJSON(ctx, endpointURL(dataBase, "/playlists", url.Values{"part": {"snippet"}, "id": {id}}), costList, &r); err != nil {
		return "", err
	}
	if len(r.Items) == 0 {
		return "", apperr.New(apperr.NotFound, "playlist %s not found, or not visible to this account", id)
	}
	return r.Items[0].Snippet.Title, nil
}

func playlistAdd(ctx context.Context, in ops.Input) (*ops.Result, error) {
	pl, err := yt.PlaylistID(in.Str("playlist"))
	if err != nil {
		return nil, err
	}
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	pos := -1
	if in.Has("position") {
		if pos = in.Int("position", -1); pos < 0 {
			return nil, apperr.New(apperr.InvalidInput, "--position must be 0 or more")
		}
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		pt, err := s.playlistTitle(ctx, pl)
		if err != nil {
			return nil, err
		}
		v, err := s.video(ctx, id, "snippet")
		if err != nil {
			return nil, err
		}
		t := videoTarget(v)
		t["playlist"], t["playlist_title"] = pl, pt
		where := any("end")
		if pos >= 0 {
			where = pos
		}
		return s.plan("add to playlist", t, map[string]any{"add_video": id, "position": where}, costWrite), nil
	}
	item, err := s.addToPlaylist(ctx, pl, id, pos)
	if err != nil {
		return nil, err
	}
	return s.result(map[string]any{"playlist": pl, "video": id, "item": item}), nil
}

func (s *session) addToPlaylist(ctx context.Context, pl, id string, pos int) (string, error) {
	sn := map[string]any{"playlistId": pl, "resourceId": map[string]any{"kind": "youtube#video", "videoId": id}}
	if pos >= 0 {
		sn["position"] = pos
	}
	var out struct {
		ID string `json:"id"`
	}
	u := endpointURL(dataBase, "/playlistItems", url.Values{"part": {"snippet"}})
	if err := s.writeJSON(ctx, http.MethodPost, u, map[string]any{"snippet": sn}, costWrite, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func playlistRemove(ctx context.Context, in ops.Input) (*ops.Result, error) {
	pl, err := yt.PlaylistID(in.Str("playlist"))
	if err != nil {
		return nil, err
	}
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	q := url.Values{"part": {"id,snippet"}, "playlistId": {pl}, "videoId": {id}, "maxResults": {"50"}}
	if err := s.getJSON(ctx, endpointURL(dataBase, "/playlistItems", q), costList, &r); err != nil {
		return nil, err
	}
	if len(r.Items) == 0 {
		return nil, apperr.New(apperr.NotFound, "video %s is not in playlist %s", id, pl)
	}
	items := make([]string, len(r.Items))
	for i, it := range r.Items {
		items[i] = it.ID
	}
	if in.DryRun {
		t := map[string]any{"playlist": pl, "video": id, "title": r.Items[0].Snippet.Title}
		return s.plan("remove from playlist", t, map[string]any{"remove_items": items}, costList+costWrite*len(items)), nil
	}
	for _, it := range items {
		u := endpointURL(dataBase, "/playlistItems", url.Values{"id": {it}})
		if err := s.writeJSON(ctx, http.MethodDelete, u, nil, costWrite, nil); err != nil {
			return nil, err
		}
	}
	return s.result(map[string]any{"playlist": pl, "video": id, "removed_items": items}), nil
}

// ---- comments

func (s *session) comment(ctx context.Context, id string) (map[string]any, error) {
	var r struct {
		Items []struct {
			ID      string         `json:"id"`
			Snippet map[string]any `json:"snippet"`
		} `json:"items"`
	}
	q := url.Values{"part": {"snippet"}, "id": {id}, "textFormat": {"plainText"}}
	if err := s.getJSON(ctx, endpointURL(dataBase, "/comments", q), costList, &r); err != nil {
		return nil, err
	}
	if len(r.Items) == 0 {
		return nil, apperr.New(apperr.NotFound, "comment %s not found", id)
	}
	sn := r.Items[0].Snippet
	text := str(sn, "textOriginal")
	if text == "" {
		text = str(sn, "textDisplay")
	}
	t := map[string]any{"comment": id, "author": str(sn, "authorDisplayName"), "text": clip(text, 200)}
	if v := str(sn, "videoId"); v != "" {
		t["video"] = v
	}
	if p := str(sn, "parentId"); p != "" {
		t["parent"] = p
	}
	return t, nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func commentPost(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	text, err := checkText(in.Str("text"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		v, err := s.video(ctx, id, "snippet")
		if err != nil {
			return nil, err
		}
		return s.plan("post comment", videoTarget(v), map[string]any{"text": text}, costWrite), nil
	}
	body := map[string]any{"snippet": map[string]any{"videoId": id, "topLevelComment": map[string]any{"snippet": map[string]any{"textOriginal": text}}}}
	var out struct {
		ID string `json:"id"`
	}
	if err := s.writeJSON(ctx, http.MethodPost, endpointURL(dataBase, "/commentThreads", url.Values{"part": {"snippet"}}), body, costWrite, &out); err != nil {
		return nil, err
	}
	return s.result(map[string]any{"comment": out.ID, "video": id, "url": yt.VideoURL(id, false) + "&lc=" + out.ID}), nil
}

func commentReply(ctx context.Context, in ops.Input) (*ops.Result, error) {
	cid, err := checkCommentID(in.Str("comment"))
	if err != nil {
		return nil, err
	}
	text, err := checkText(in.Str("text"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		t, err := s.comment(ctx, cid)
		if err != nil {
			return nil, err
		}
		return s.plan("reply to comment", t, map[string]any{"text": text}, costWrite), nil
	}
	var out struct {
		ID string `json:"id"`
	}
	body := map[string]any{"snippet": map[string]any{"parentId": cid, "textOriginal": text}}
	if err := s.writeJSON(ctx, http.MethodPost, endpointURL(dataBase, "/comments", url.Values{"part": {"snippet"}}), body, costWrite, &out); err != nil {
		return nil, err
	}
	return s.result(map[string]any{"comment": out.ID, "parent": cid}), nil
}

func commentModerate(ctx context.Context, in ops.Input) (*ops.Result, error) {
	cid, err := checkCommentID(in.Str("comment"))
	if err != nil {
		return nil, err
	}
	status := strings.TrimSpace(in.Str("status"))
	switch strings.ToLower(status) {
	case "published":
		status = "published"
	case "heldforreview", "held":
		status = "heldForReview"
	case "rejected":
		status = "rejected"
	default:
		return nil, apperr.New(apperr.InvalidInput, "status must be published, heldForReview or rejected")
	}
	ban := in.Bool("ban")
	if ban && status != "rejected" {
		return nil, apperr.New(apperr.InvalidInput, "--ban only works with rejected")
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		t, err := s.comment(ctx, cid)
		if err != nil {
			return nil, err
		}
		return s.plan("moderate comment", t, map[string]any{"status": status, "ban_author": ban}, costWrite), nil
	}
	q := url.Values{"id": {cid}, "moderationStatus": {status}}
	if ban {
		q.Set("banAuthor", "true")
	}
	if err := s.writeJSON(ctx, http.MethodPost, endpointURL(dataBase, "/comments/setModerationStatus", q), nil, costWrite, nil); err != nil {
		return nil, err
	}
	return s.result(map[string]any{"comment": cid, "status": status, "ban_author": ban}), nil
}

// ---- captions

func captionsUpload(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	path := config.ExpandHome(in.Str("file"))
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".srt" && ext != ".vtt" {
		return nil, apperr.New(apperr.InvalidInput, "the caption file must be .srt or .vtt")
	}
	body, err := readLimited(path, maxCaptionSize, "caption file")
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "the caption file is empty")
	}
	lang, err := checkLang(in.Str("lang"))
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Str("name"))
	if utf8.RuneCountInString(name) > 150 {
		return nil, apperr.New(apperr.InvalidInput, "the track name is longer than 150 characters")
	}
	draft := in.Bool("draft")
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		v, err := s.video(ctx, id, "snippet,status")
		if err != nil {
			return nil, err
		}
		return s.plan("upload captions", videoTarget(v), map[string]any{
			"file": in.Str("file"), "bytes": len(body), "language": lang, "name": name, "draft": draft}, costCaptns), nil
	}
	meta := map[string]any{"snippet": map[string]any{"videoId": id, "language": lang, "name": name, "isDraft": draft}}
	track, err := s.uploadCaption(ctx, meta, body)
	if err != nil {
		return nil, err
	}
	return s.result(map[string]any{"video": id, "caption": track, "language": lang, "name": name, "draft": draft}), nil
}

func captionsList(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []struct {
			ID      string         `json:"id"`
			Snippet map[string]any `json:"snippet"`
		} `json:"items"`
	}
	if err := s.getJSON(ctx, endpointURL(dataBase, "/captions", url.Values{"part": {"snippet"}, "videoId": {id}}), 50, &r); err != nil {
		return nil, err
	}
	tracks := make([]map[string]any, 0, len(r.Items))
	for _, it := range r.Items {
		sn := it.Snippet
		kind := str(sn, "trackKind")
		tracks = append(tracks, map[string]any{
			"id": it.ID, "language": str(sn, "language"), "name": str(sn, "name"), "kind": kind,
			"auto_generated": strings.EqualFold(kind, "asr"), "draft": str(sn, "isDraft") == "true",
			"status": str(sn, "status"), "updated": str(sn, "lastUpdated"),
		})
	}
	return s.result(map[string]any{"video": id, "tracks": tracks}), nil
}

// ---- video.upload

func upload(ctx context.Context, in ops.Input) (*ops.Result, error) {
	path := config.ExpandHome(in.Str("file"))
	st, err := os.Stat(path)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "can't open the video %q: %v", in.Str("file"), errText(err))
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return nil, apperr.New(apperr.InvalidInput, "the video %q is empty or not a regular file", in.Str("file"))
	}
	if st.Size() > 256<<30 {
		return nil, apperr.New(apperr.InvalidInput, "the video is over YouTube's 256 GB limit")
	}
	title, err := checkTitle(in.Str("title"), maxTitle)
	if err != nil {
		return nil, err
	}
	snippet := map[string]any{"title": title}
	changes := map[string]any{"title": title}
	if in.Has("description") {
		d, err := description(in.Str("description"))
		if err != nil {
			return nil, err
		}
		snippet["description"], changes["description"] = d, d
	}
	if in.Has("tags") {
		tags := in.List("tags")
		if err := checkTags(tags); err != nil {
			return nil, err
		}
		snippet["tags"], changes["tags"] = tags, tags
	}
	if in.Has("category") {
		c, err := checkCategory(in.Str("category"))
		if err != nil {
			return nil, err
		}
		snippet["categoryId"], changes["category"] = c, c
	}
	if in.Has("lang") {
		l, err := checkLang(in.Str("lang"))
		if err != nil {
			return nil, err
		}
		snippet["defaultLanguage"], snippet["defaultAudioLanguage"], changes["language"] = l, l, l
	}
	privacy := "private"
	if in.Str("privacy") != "" {
		if privacy, err = checkPrivacy(in.Str("privacy")); err != nil {
			return nil, err
		}
	}
	status := map[string]any{"privacyStatus": privacy}
	changes["privacy"] = privacy
	if in.Has("publish-at") {
		p, err := publishAt(in.Str("publish-at"))
		if err != nil {
			return nil, err
		}
		if privacy != "private" {
			return nil, apperr.New(apperr.InvalidInput, "--publish-at needs --privacy private (YouTube makes the video public at that time)")
		}
		status["publishAt"], changes["publish_at"] = p, p
	}
	if in.Has("kids") {
		status["selfDeclaredMadeForKids"], changes["made_for_kids"] = in.Bool("kids"), in.Bool("kids")
	}
	var img []byte
	var imgType string
	if in.Has("thumbnail") && in.Str("thumbnail") != "" {
		if img, imgType, err = image(in.Str("thumbnail")); err != nil {
			return nil, err
		}
		changes["thumbnail"] = in.Str("thumbnail")
	}
	pl := ""
	if in.Has("playlist") && in.Str("playlist") != "" {
		if pl, err = yt.PlaylistID(in.Str("playlist")); err != nil {
			return nil, err
		}
		changes["playlist"] = pl
	}
	units := 0
	if img != nil {
		units += costWrite
	}
	if pl != "" {
		units += costWrite
	}

	s, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		target := map[string]any{"file": in.Str("file"), "bytes": st.Size(), "size_mb": float64(st.Size()*10/(1<<20)) / 10}
		return s.plan("upload video", target, changes, map[string]any{"upload_calls": 1, "units": units}), nil
	}

	v, err := s.resumableUpload(ctx, path, st.Size(), videoType(path), map[string]any{"snippet": snippet, "status": status})
	if err != nil {
		return nil, err
	}
	data := map[string]any{"video": v.ID, "url": yt.VideoURL(v.ID, false), "title": title, "privacy": privacy}
	if p, ok := status["publishAt"]; ok {
		data["publish_at"] = p
	}
	if up := str(v.Status, "uploadStatus"); up != "" {
		data["upload_status"] = up
	}
	// The video is already on YouTube, so a failed follow-up is a warning, not an error.
	var warns []string
	if img != nil {
		err := s.setThumbnail(ctx, v.ID, img, imgType)
		data["thumbnail_set"] = err == nil
		if err != nil {
			warns = append(warns, "uploaded, but the thumbnail wasn't set: "+apperr.From(err).Message+" (retry with `ytx thumbnail "+v.ID+" <file>`)")
		}
	}
	if pl != "" {
		_, err := s.addToPlaylist(ctx, pl, v.ID, -1)
		data["playlist_added"] = err == nil
		if err != nil {
			warns = append(warns, "uploaded, but not added to the playlist: "+apperr.From(err).Message+" (retry with `ytx playlist add "+pl+" "+v.ID+"`)")
		}
	}
	return s.result(data, warns...), nil
}
