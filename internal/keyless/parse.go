package keyless

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	countRe    = regexp.MustCompile(`(?i)([\d][\d.,]*)\s*(thousand|million|billion|[kmb])?\b`)
	agoRe      = regexp.MustCompile(`(?i)(\d+)\s+(second|minute|hour|day|week|month|year)s?\s+ago`)
	durationRe = regexp.MustCompile(`^\d{1,2}(:\d{2}){1,2}$`)
)

// parseCount reads "1,184,827 views", "209K views", "9.3 thousand views",
// "4.56M subscribers" and "No views". Abbreviated forms are approximate; YouTube
// only sends the short form in many places.
func parseCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(strings.ToLower(s), "no ") {
		return 0
	}
	m := countRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(m[2]) {
	case "k", "thousand":
		n *= 1e3
	case "m", "million":
		n *= 1e6
	case "b", "billion":
		n *= 1e9
	}
	return int64(math.Round(n))
}

// parseDuration reads "3:51" or "1:07:53" into seconds (0 when it is not one).
func parseDuration(s string) int {
	s = strings.TrimSpace(s)
	if !durationRe.MatchString(s) {
		return 0
	}
	total := 0
	for _, p := range strings.Split(s, ":") {
		n, _ := strconv.Atoi(p)
		total = total*60 + n
	}
	return total
}

var unitDur = map[string]time.Duration{
	"second": time.Second, "minute": time.Minute, "hour": time.Hour,
	"day": 24 * time.Hour, "week": 7 * 24 * time.Hour,
	"month": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour,
}

// parseAgo turns "3 weeks ago" or "Streamed 5 hours ago" into an approximate
// time and the smallest age it can mean. A month counts 30 days and a year 365,
// so the time is only a guide; minAge is safe to compare against a cutoff.
func parseAgo(s string, now time.Time) (t time.Time, minAge time.Duration, ok bool) {
	m := agoRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, 0, false
	}
	n, _ := strconv.Atoi(m[1])
	d := time.Duration(n) * unitDur[strings.ToLower(m[2])]
	return now.Add(-d), d, true
}

// bestThumb picks the largest image from a {thumbnails:[{url,width,height}]} or
// {sources:[...]} list.
func bestThumb(list []any) string {
	best, area := "", -1
	for _, it := range list {
		u := str(get(it, "url"))
		if u == "" {
			continue
		}
		w, _ := get(it, "width").(float64)
		h, _ := get(it, "height").(float64)
		if a := int(w * h); a > area {
			best, area = u, a
		}
	}
	if strings.HasPrefix(best, "//") {
		best = "https:" + best
	}
	return best
}
