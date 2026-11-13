package channel

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
)

// YouTube's own limits (Data API videos/playlists/comments resources).
const (
	maxTitle       = 100
	maxPlaylistTtl = 150
	maxDescBytes   = 5000
	maxTagsChars   = 500
	maxComment     = 10000
	maxThumbBytes  = 2 << 20
	maxCaptionSize = 100 << 20
)

var (
	now        = time.Now
	categoryRe = regexp.MustCompile(`^[0-9]{1,4}$`)
	langRe     = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
	commentRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{6,}$`)
)

// readLimited reads a small local file, refusing anything bigger than max.
func readLimited(path string, max int64, what string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "can't open the %s %q: %v", what, path, errText(err))
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, apperr.New(apperr.InvalidInput, "the %s %q is not a regular file", what, path)
	}
	if st.Size() > max {
		return nil, apperr.New(apperr.InvalidInput, "the %s %q is %d bytes; the limit is %d", what, path, st.Size(), max)
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

func errText(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

func checkTitle(t string, max int) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", apperr.New(apperr.InvalidInput, "the title is empty")
	}
	if n := utf8.RuneCountInString(t); n > max {
		return "", apperr.New(apperr.InvalidInput, "the title has %d characters; YouTube allows %d", n, max)
	}
	if strings.ContainsAny(t, "<>") {
		return "", apperr.New(apperr.InvalidInput, "YouTube doesn't allow < or > in a title")
	}
	return t, nil
}

// description takes text, or "@file.txt" to read it from a file.
func description(v string) (string, error) {
	if strings.HasPrefix(v, "@") {
		b, err := readLimited(config.ExpandHome(v[1:]), 1<<20, "description file")
		if err != nil {
			return "", err
		}
		if !utf8.Valid(b) {
			return "", apperr.New(apperr.InvalidInput, "the description file is not UTF-8 text")
		}
		v = strings.TrimRight(string(b), "\r\n")
	}
	if len(v) > maxDescBytes {
		return "", apperr.New(apperr.InvalidInput, "the description is %d bytes; YouTube allows %d", len(v), maxDescBytes)
	}
	if strings.ContainsAny(v, "<>") {
		return "", apperr.New(apperr.InvalidInput, "YouTube doesn't allow < or > in a description")
	}
	return v, nil
}

// checkTags applies YouTube's 500-character total, where a tag with a space
// counts its surrounding quotes too.
func checkTags(tags []string) error {
	n := 0
	for i, t := range tags {
		if strings.ContainsAny(t, "<>") {
			return apperr.New(apperr.InvalidInput, "YouTube doesn't allow < or > in a tag")
		}
		n += utf8.RuneCountInString(t)
		if strings.Contains(t, " ") {
			n += 2
		}
		if i > 0 {
			n++
		}
	}
	if n > maxTagsChars {
		return apperr.New(apperr.InvalidInput, "the tags add up to %d characters; YouTube allows %d", n, maxTagsChars)
	}
	return nil
}

func checkPrivacy(p string) (string, error) {
	switch p = strings.ToLower(strings.TrimSpace(p)); p {
	case "private", "unlisted", "public":
		return p, nil
	}
	return "", apperr.New(apperr.InvalidInput, "privacy must be private, unlisted or public")
}

// publishAt parses a schedule time; it must be in the future.
func publishAt(v string) (string, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		return "", apperr.New(apperr.InvalidInput, "--publish-at must be an RFC 3339 time like 2026-12-01T15:00:00Z")
	}
	if !t.After(now()) {
		return "", apperr.New(apperr.InvalidInput, "--publish-at must be in the future")
	}
	return t.UTC().Format(time.RFC3339), nil
}

func checkCategory(c string) (string, error) {
	c = strings.TrimSpace(c)
	if !categoryRe.MatchString(c) {
		return "", apperr.New(apperr.InvalidInput, "--category must be a numeric category id, e.g. 22 (People & Blogs) or 27 (Education)")
	}
	return c, nil
}

func checkLang(l string) (string, error) {
	l = strings.TrimSpace(l)
	if !langRe.MatchString(l) {
		return "", apperr.New(apperr.InvalidInput, "%q is not a language code like en or pt-BR", l)
	}
	return l, nil
}

func checkCommentID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !commentRe.MatchString(id) {
		return "", apperr.New(apperr.InvalidInput, "%q is not a comment id", id)
	}
	return id, nil
}

func checkText(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", apperr.New(apperr.InvalidInput, "the text is empty")
	}
	if n := utf8.RuneCountInString(t); n > maxComment {
		return "", apperr.New(apperr.InvalidInput, "the text has %d characters; YouTube allows %d", n, maxComment)
	}
	return t, nil
}

// image checks a thumbnail before any call: JPG or PNG by its bytes, at most 2 MB.
func image(path string) (data []byte, ctype string, err error) {
	b, err := readLimited(config.ExpandHome(path), maxThumbBytes, "thumbnail")
	if err != nil {
		return nil, "", err
	}
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return b, "image/jpeg", nil
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return b, "image/png", nil
	}
	return nil, "", apperr.New(apperr.InvalidInput, "the thumbnail must be a JPG or PNG image")
}

// isoSeconds turns an ISO 8601 duration (PT1H2M3S, P1DT2H) into seconds.
func isoSeconds(d string) int {
	if !strings.HasPrefix(d, "P") {
		return 0
	}
	total, num, inTime := 0, "", false
	for _, c := range d[1:] {
		switch {
		case c == 'T':
			inTime = true
		case c >= '0' && c <= '9' || c == '.':
			num += string(c)
		default:
			f, _ := strconv.ParseFloat(num, 64)
			n := int(f)
			num = ""
			switch {
			case c == 'W':
				total += n * 7 * 86400
			case c == 'D':
				total += n * 86400
			case c == 'H' && inTime:
				total += n * 3600
			case c == 'M' && inTime:
				total += n * 60
			case c == 'S' && inTime:
				total += n
			}
		}
	}
	return total
}
