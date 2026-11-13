// Package feedcheck builds `ytx feedcheck`: it puts your thumbnail and title
// among the top five real videos for a topic and renders review sheets with
// the agd CLI (template y4-feed-preview). Every file of one run goes into one
// new folder that is never overwritten.
package feedcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/reads"
	"github.com/morethancoder/ytx/internal/research"
	"github.com/morethancoder/ytx/internal/yt"
)

func init() { ops.Handle("feedcheck", run) }

const (
	competitorCount = 5
	maxTitle        = 100
	maxHeading      = 40
	maxNote         = 90
	maxSheets       = 12 // thumbs x titles; each sheet is two renders
	searchSize      = 30 // enough to still find five after Shorts and live streams are dropped
)

// Swappable for tests.
var (
	now  = time.Now
	pick = func(n int) int { return rand.IntN(n) } // blind position: 0..n-1
)

var (
	durationRE = regexp.MustCompile(`^(\d{1,2}:)?\d{1,2}:\d{2}$`)
	themeRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Competitor is one real video from the topic search.
type Competitor struct {
	Title    string `json:"title"`
	Channel  string `json:"channel"`
	URL      string `json:"url"`
	Views    string `json:"views"`
	Age      string `json:"age,omitempty"`
	Duration string `json:"duration,omitempty"`
	Thumb    string `json:"thumb_file"`

	views int64          // the raw count, for the blind sheet's believable numbers
	item  map[string]any // its entry in the agd input; not part of the output
}

// Sheet is one thumbnail x title combination, as a marked and a blind sheet.
type Sheet struct {
	Thumb         string `json:"thumb"`
	Title         string `json:"title"`
	MarkedPNG     string `json:"marked_png,omitempty"`
	BlindPNG      string `json:"blind_png,omitempty"`
	BlindPosition int    `json:"blind_position"`
	AgdFile       string `json:"agd_file"`
	AgdBlindFile  string `json:"agd_blind_file"`
}

// Output is the data of the envelope.
type Output struct {
	Folder        string       `json:"folder"`
	Competitors   []Competitor `json:"competitors"`
	Sheets        []Sheet      `json:"sheets"`
	YoursPosition int          `json:"yours_position"`
}

func run(ctx context.Context, in ops.Input) (*ops.Result, error) {
	topic := strings.TrimSpace(in.Str("topic"))
	if topic == "" {
		return nil, apperr.New(apperr.InvalidInput, "empty topic")
	}
	thumbs, titles := in.Strings("thumb"), in.Strings("title")
	if len(thumbs) == 0 || len(titles) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "give at least one --thumb and one --title")
	}
	if len(thumbs)*len(titles) > maxSheets {
		return nil, apperr.New(apperr.InvalidInput, "%d thumbnails x %d titles is %d sheets; the most is %d", len(thumbs), len(titles), len(thumbs)*len(titles), maxSheets)
	}
	position := in.Int("position", 2)
	if position < 1 || position > 6 {
		return nil, apperr.New(apperr.InvalidInput, "--position must be 1 to 6")
	}
	for i, t := range titles {
		titles[i] = strings.TrimSpace(t)
		if n := len([]rune(titles[i])); n == 0 || n > maxTitle {
			return nil, apperr.New(apperr.InvalidInput, "title %d is %d characters; YouTube allows 1 to %d", i+1, n, maxTitle)
		}
	}
	theme := strings.TrimSpace(in.Str("theme"))
	if theme == "" {
		theme = "light"
	}
	if !themeRE.MatchString(theme) {
		return nil, apperr.New(apperr.InvalidInput, "--theme must be an agd palette id such as light or dark")
	}
	duration := ""
	if d := strings.TrimSpace(in.Str("duration")); d != "" {
		if !durationRE.MatchString(d) {
			return nil, apperr.New(apperr.InvalidInput, "--duration must look like 12:04 or 1:02:03")
		}
		duration = d
	}

	var warnings []string
	thumbPaths := make([]string, len(thumbs))
	thumbExt := make([]string, len(thumbs))
	for i, t := range thumbs {
		abs, err := filepath.Abs(t)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "bad thumbnail path %s", t)
		}
		ext, w, err := checkThumb(abs)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
		thumbPaths[i], thumbExt[i] = abs, ext
	}
	// A local avatar is checked up front, like the thumbnails, before anything is fetched or written.
	avatarRef := strings.TrimSpace(in.Str("avatar"))
	localAvatar, avatarExt := "", ""
	if avatarRef != "" && !strings.HasPrefix(avatarRef, "http://") && !strings.HasPrefix(avatarRef, "https://") {
		abs, err := filepath.Abs(avatarRef)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "bad avatar path %s", avatarRef)
		}
		if avatarExt, err = checkAvatar(abs); err != nil {
			return nil, err
		}
		localAvatar = abs
	}

	r, err := reads.Reader(in)
	if err != nil {
		return nil, err
	}

	// Who you are comes first: it also tells the search filter which videos are yours.
	// The sign-in is always read, for the channel id; --channel and --avatar still win for how you look.
	name := strings.TrimSpace(in.Str("channel"))
	var ownID, ownAvatarURL string
	if m := signedIn(ctx); m != nil {
		ownID = m.ID
		if name == "" {
			name = m.Name
		}
		if avatarRef == "" {
			ownAvatarURL = m.Avatar
		}
	}
	if name == "" {
		name = "Your channel"
	}

	lang, region := reads.Defaults(in)
	vs, err := r.Search(ctx, yt.SearchOpts{Query: topic, Limit: searchSize, Type: "video", Order: "relevance", Lang: lang, Region: region})
	if err != nil {
		return nil, err
	}
	pool := candidates(vs, ownID)
	if len(pool) == 0 {
		return nil, apperr.New(apperr.NotFound, "no long videos found for %q to compare with", topic)
	}

	folder, err := newFolder(in.Str("out"), topic, now())
	if err != nil {
		return nil, err
	}

	comps, w := fetchCompetitors(ctx, r, pool, folder)
	warnings = append(warnings, w...)
	if len(comps) == 0 {
		if ctx.Err() != nil {
			return nil, ctxError(ctx, "downloading competitor thumbnails")
		}
		return nil, apperr.New(apperr.NetworkError, "couldn't download any competitor thumbnail (folder %s)", folder)
	}
	if len(comps) < competitorCount {
		warnings = append(warnings, fmt.Sprintf("only %d competitors found, not %d", len(comps), competitorCount))
	}
	slots := len(comps) + 1
	if position > slots {
		warnings = append(warnings, fmt.Sprintf("--position %d is past the end of a feed of %d; yours is at %d", position, slots, slots))
		position = slots
	}
	medViews := Views(medianViews(comps))

	// Your own pieces.
	yoursThumb := make([]string, len(thumbPaths))
	for i, p := range thumbPaths {
		if yoursThumb[i], err = copyFile(p, folder, fmt.Sprintf("thumb-%d", i+1), thumbExt[i]); err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "copying thumbnail %s", p)
		}
	}
	ownAvatar := ""
	switch {
	case avatarRef == "" && ownAvatarURL != "":
		// A failed avatar is not worth a warning: the sheet shows the initial instead.
		if p, err := download(ctx, ownAvatarURL, folder, "avatar-you"); err == nil {
			ownAvatar = p
		}
	case strings.HasPrefix(avatarRef, "http://") || strings.HasPrefix(avatarRef, "https://"):
		p, err := download(ctx, avatarRef, folder, "avatar-you")
		if err != nil {
			warnings = append(warnings, "your avatar could not be downloaded: "+err.Error())
		} else {
			ownAvatar = p
		}
	case localAvatar != "":
		p, err := copyFile(localAvatar, folder, "avatar-you", avatarExt)
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "copying avatar %s", localAvatar)
		}
		ownAvatar = p
	}

	// One input per sheet; the marked and blind sheet of a combination share a number.
	out := &Output{Folder: folder, Competitors: comps, YoursPosition: position}
	var designs []design
	date := now().Format("2006-01-02")
	n := 0
	for ti := range thumbs {
		for hi := range titles {
			n++
			label := fmt.Sprintf("v%d", n)
			note := fit(fmt.Sprintf("Thumbnail %d, title %d · %s", ti+1, hi+1, date), maxNote)
			// The blind sheet says nothing about which version it is, and your card looks like
			// a competitor's: the competitors' median views and an ordinary age.
			blindNote := fit("Blind check · "+date, maxNote)
			you := map[string]any{"thumb": yoursThumb[ti], "title": text(titles[hi]), "channel": text(name),
				"views": Views(0), "age": "just now", "yours": true}
			if ownAvatar != "" {
				you["avatar"] = ownAvatar
			}
			if duration != "" {
				you["duration"] = duration
			}
			blindYou := map[string]any{}
			for k, v := range you {
				blindYou[k] = v
			}
			delete(blindYou, "yours") // without the ring, agd's phone feed would show cards 1-3 unless phoneFrom says otherwise
			blindYou["views"], blindYou["age"] = medViews, "2 days ago"
			blindPos := pick(slots) + 1
			phoneFrom := phoneStart(blindPos, slots)
			s := Sheet{Thumb: thumbs[ti], Title: titles[hi], BlindPosition: blindPos,
				AgdFile: label + ".agd.json", AgdBlindFile: label + "-blind.agd.json"}
			marked := design{label: label, input: label + ".input.json", agd: s.AgdFile, png: label + ".png"}
			blind := design{label: label + "-blind", input: label + "-blind.input.json", agd: s.AgdBlindFile, png: label + "-blind.png"}
			if err := writeInput(folder, marked.input, sheetInput(fit(titles[hi], maxHeading), note, nil, lineup(comps, you, position), 0)); err != nil {
				return nil, err
			}
			if err := writeInput(folder, blind.input, sheetInput(fit(topic, maxHeading), blindNote, false, lineup(comps, blindYou, blindPos), phoneFrom)); err != nil {
				return nil, err
			}
			designs = append(designs, marked, blind)
			out.Sheets = append(out.Sheets, s)
		}
	}

	bin := agdPath()
	if bin == "" {
		warnings = append(warnings, manual(folder, designs, theme))
	}
	for _, d := range designs {
		if bin == "" {
			break
		}
		w, err := build(ctx, bin, folder, theme, d)
		warnings = append(warnings, w...)
		if err != nil {
			return nil, err
		}
	}
	for i := range out.Sheets {
		if bin != "" {
			out.Sheets[i].MarkedPNG = filepath.Join(folder, fmt.Sprintf("v%d.png", i+1))
			out.Sheets[i].BlindPNG = filepath.Join(folder, fmt.Sprintf("v%d-blind.png", i+1))
		}
		out.Sheets[i].AgdFile = filepath.Join(folder, out.Sheets[i].AgdFile)
		out.Sheets[i].AgdBlindFile = filepath.Join(folder, out.Sheets[i].AgdBlindFile)
	}
	return &ops.Result{Data: out, Warnings: warnings}, nil
}

// candidates keeps long, finished videos that are not yours, in search order.
func candidates(vs []model.Video, ownChannel string) []model.Video {
	var out []model.Video
	seen := map[string]bool{}
	for _, v := range vs {
		upcoming := strings.HasPrefix(v.PublishedText, "Premiere") || strings.HasPrefix(v.PublishedText, "Scheduled")
		switch {
		case v.ID == "" || seen[v.ID] || v.Thumbnail == "":
		case v.IsShort || v.IsLive || upcoming || v.DurationS <= 0:
		case ownChannel != "" && v.ChannelID == ownChannel:
		default:
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	return out
}

// phoneStart picks the first card of the 3-card phone window for a blind sheet: at random among the
// windows that hold position p in a feed of n videos, so the window never points at your video.
func phoneStart(p, n int) int {
	lo, hi := max(1, p-2), min(p, max(1, n-2))
	if hi < lo {
		return lo
	}
	return lo + min(max(pick(hi-lo+1), 0), hi-lo)
}

// lineup puts you at position (1-based) among the competitors.
func lineup(comps []Competitor, you map[string]any, position int) []map[string]any {
	items := make([]map[string]any, 0, len(comps)+1)
	for _, c := range comps {
		items = append(items, c.item)
	}
	if position > len(items)+1 {
		position = len(items) + 1
	}
	items = append(items, nil)
	copy(items[position:], items[position-1:])
	items[position-1] = you
	return items
}

// sheetInput is the agd input for one sheet. marker is nil for the default
// legend or false for the blind test.
// phoneFrom (1 to 4, 0 = leave out) is the first card of the phone feed's window.
func sheetInput(heading, note string, marker any, videos []map[string]any, phoneFrom int) map[string]any {
	slide := map[string]any{"role": "feed", "heading": heading, "note": note, "videos": videos}
	if phoneFrom > 0 {
		slide["phoneFrom"] = phoneFrom
	}
	if marker != nil {
		slide["marker"] = marker
	}
	return map[string]any{"format": "review-wide", "slides": []any{slide}}
}

func writeInput(folder, name string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return apperr.Wrap(apperr.Internal, err, "writing %s", name)
	}
	if err := os.WriteFile(filepath.Join(folder, name), buf.Bytes(), 0o644); err != nil {
		return apperr.Wrap(apperr.Internal, err, "writing %s", name)
	}
	return nil
}

// newFolder makes <base>/feedcheck/<topic>-<date>, adding -2, -3 ... when it exists.
// os.Mkdir fails on an existing name, so two runs can't share or overwrite a folder.
func newFolder(out, topic string, t time.Time) (string, error) {
	base := out
	if base == "" {
		if cfg, _ := config.Load(); cfg != nil {
			base = cfg.ResearchDir
		}
	}
	if base == "" {
		base = "."
	}
	root := filepath.Join(base, "feedcheck")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", apperr.New(apperr.InvalidInput, "can't use the folder %s: %v", root, err)
	}
	name := research.Slug(topic) + "-" + t.Format("2006-01-02")
	for i := 1; i < 1000; i++ {
		p := filepath.Join(root, name)
		if i > 1 {
			p = fmt.Sprintf("%s-%d", filepath.Join(root, name), i)
		}
		err := os.Mkdir(p, 0o755)
		if err == nil {
			abs, aerr := filepath.Abs(p)
			if aerr != nil {
				return p, nil
			}
			return abs, nil
		}
		if !os.IsExist(err) {
			return "", apperr.New(apperr.InvalidInput, "can't create %s: %v", p, err)
		}
	}
	return "", apperr.New(apperr.Internal, "too many folders named %s in %s", name, root)
}

// fetchCompetitors takes the first five of pool whose thumbnail downloads (a
// failed one is replaced by the next), then looks up their channels' avatars.
func fetchCompetitors(ctx context.Context, r yt.Reader, pool []model.Video, folder string) ([]Competitor, []string) {
	var warnings []string
	var chosen []model.Video
	var files []string
	next := 0
	for len(chosen) < competitorCount && next < len(pool) {
		batch := pool[next:min(next+competitorCount-len(chosen), len(pool))]
		next += len(batch)
		paths := make([]string, len(batch))
		errs := make([]error, len(batch))
		forEach(len(batch), func(i int) {
			paths[i], errs[i] = download(ctx, batch[i].Thumbnail, folder, safeName(batch[i].ID))
		})
		for i, v := range batch {
			if errs[i] != nil {
				warnings = append(warnings, fmt.Sprintf("skipped %q: thumbnail download failed: %v", v.Title, errs[i]))
				continue
			}
			chosen = append(chosen, v)
			files = append(files, paths[i])
		}
	}

	// One avatar per channel, looked up in parallel.
	var chIDs []string
	idx := map[string]int{}
	for _, v := range chosen {
		if _, ok := idx[v.ChannelID]; !ok && v.ChannelID != "" {
			idx[v.ChannelID] = len(chIDs)
			chIDs = append(chIDs, v.ChannelID)
		}
	}
	avatars := make([]string, len(chIDs))
	forEach(len(chIDs), func(i int) {
		c, err := r.Channel(ctx, chIDs[i], yt.ChannelOpts{Limit: 1})
		if err != nil || c == nil || c.Thumbnail == "" {
			return
		}
		if p, err := download(ctx, c.Thumbnail, folder, safeName(chIDs[i])+"-avatar"); err == nil {
			avatars[i] = p
		}
	})

	t := now()
	comps := make([]Competitor, 0, len(chosen))
	for i, v := range chosen {
		c := Competitor{views: v.Views, Title: v.Title, Channel: v.ChannelName, URL: v.URL, Views: Views(v.Views),
			Age: videoAge(v, t), Duration: Duration(v.DurationS), Thumb: files[i]}
		item := map[string]any{"thumb": files[i], "title": text(v.Title), "channel": text(v.ChannelName), "views": c.Views}
		if j, ok := idx[v.ChannelID]; ok && avatars[j] != "" {
			item["avatar"] = avatars[j]
		}
		if c.Age != "" {
			item["age"] = c.Age
		}
		if c.Duration != "" {
			item["duration"] = c.Duration
		}
		c.item = item
		comps = append(comps, c)
	}
	return comps, warnings
}

// medianViews is the median view count of the competitors.
func medianViews(comps []Competitor) int64 {
	v := make([]int64, len(comps))
	for i, c := range comps {
		v[i] = c.views
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	if len(v) == 0 {
		return 0
	}
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}
