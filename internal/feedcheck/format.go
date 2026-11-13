package feedcheck

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/morethancoder/ytx/internal/model"
)

// Views writes a view count the way YouTube's feed does. It rounds down, as
// YouTube does, so 999,999 reads "999K views" and never "1000K views".
func Views(n int64) string {
	switch {
	case n <= 0:
		return "No views"
	case n == 1:
		return "1 view"
	case n < 1000:
		return fmt.Sprintf("%d views", n)
	case n < 1_000_000:
		return short(n, 1000) + "K views"
	case n < 1_000_000_000:
		return short(n, 1_000_000) + "M views"
	}
	return short(n, 1_000_000_000) + "B views"
}

// short is n/unit with one decimal below 10 ("1.2") and none from 10 up ("12"),
// and no trailing ".0".
func short(n, unit int64) string {
	tenths := n * 10 / unit
	if tenths >= 100 {
		return fmt.Sprint(tenths / 10)
	}
	if tenths%10 == 0 {
		return fmt.Sprint(tenths / 10)
	}
	return fmt.Sprintf("%d.%d", tenths/10, tenths%10)
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// Age writes how long ago a video was published, in YouTube's style.
func Age(published, now time.Time) string {
	d := now.Sub(published)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	days := int(d / (24 * time.Hour))
	switch {
	case days < 7:
		return plural(days, "day")
	case days < 30:
		return plural(days/7, "week")
	case days < 365:
		return plural(days/30, "month")
	}
	return plural(days/365, "year")
}

// videoAge prefers the exact date and falls back to YouTube's own text.
func videoAge(v model.Video, now time.Time) string {
	if !v.Published.IsZero() {
		return Age(v.Published, now)
	}
	return strings.TrimSpace(v.PublishedText)
}

// Duration writes seconds as 12:04 or 1:02:03.
func Duration(s int) string {
	if s <= 0 {
		return ""
	}
	h, m, sec := s/3600, s%3600/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

// isRTL is true when the text holds an Arabic or Hebrew letter.
func isRTL(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && (unicode.Is(unicode.Arabic, r) || unicode.Is(unicode.Hebrew, r)) {
			return true
		}
	}
	return false
}

// text is the value agd wants for a title or channel: a plain string, or
// {text, dir: rtl} when the text reads right to left.
func text(s string) any {
	if isRTL(s) {
		return map[string]any{"text": s, "dir": "rtl"}
	}
	return s
}

// fit shortens s to at most max characters, on a word when one fits in the
// back half, and marks the cut with an ellipsis.
func fit(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := r[:max-1]
	for i := len(cut) - 1; i >= max/2; i-- {
		if cut[i] == ' ' {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRight(string(cut), " ,.;:-–—") + "…"
}
