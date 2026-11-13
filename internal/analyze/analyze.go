// Package analyze holds the small pure functions research is built on:
// medians and outlier scores, question detection, key terms.
package analyze

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/morethancoder/ytx/internal/model"
)

// Median of the views (0 for none). Videos with 0 views (premieres, fresh uploads) are skipped.
func Median(vs []model.Video) int64 {
	var xs []int64
	for _, v := range vs {
		if v.Views > 0 && !v.IsLive {
			xs = append(xs, v.Views)
		}
	}
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	m := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[m]
	}
	return (xs[m-1] + xs[m]) / 2
}

// SettleDays is how old an upload must be before its views count toward a channel's
// median. Fresh uploads have barely started collecting views, so they drag it down.
const SettleDays = 7

// MinSettled is the fewest settled uploads a channel needs to get a median at all.
const MinSettled = 5

// SettledMedian is the median of the uploads at least SettleDays old at `now`.
// An upload with no Published date counts as old. ok is false when fewer than
// MinSettled usable uploads (views above 0, not live) remain; the median is 0 then.
func SettledMedian(vs []model.Video, now time.Time) (median int64, ok bool) {
	cutoff := now.Add(-SettleDays * 24 * time.Hour)
	var old []model.Video
	n := 0
	for _, v := range vs {
		if !v.Published.IsZero() && v.Published.After(cutoff) {
			continue
		}
		old = append(old, v)
		if v.Views > 0 && !v.IsLive {
			n++
		}
	}
	if n < MinSettled {
		return 0, false
	}
	return Median(old), true
}

// Outlier is views ÷ median, rounded to 2 decimals (0 when the median is unknown).
func Outlier(views, median int64) float64 {
	if median <= 0 {
		return 0
	}
	return math.Round(float64(views)/float64(median)*100) / 100
}

var questionStart = regexp.MustCompile(`(?i)^\s*(how|why|what|which|when|where|who|can|could|should|would|is|are|do|does|did|will|any|anyone|has|have)\b`)

// arabic and other question marks count too.
var questionMark = regexp.MustCompile(`[?？؟]`)

// IsQuestion says whether a comment asks something: a question mark, or a sentence opening with a question word.
func IsQuestion(text string) bool {
	if questionMark.MatchString(text) {
		return true
	}
	for _, s := range regexp.MustCompile(`[.!\n]+`).Split(text, -1) {
		if questionStart.MatchString(s) && len(strings.Fields(s)) >= 3 {
			return true
		}
	}
	return false
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or but if then so of to in on at for with from by about as into like through over after before under
		is are was were be been being am do does did doing have has had having can could should would will shall may might must
		i me my we our you your he she it its they them their this that these those there here what which who whom whose when where why how
		not no yes just very really also too more most much many some any all each every other such only own same than
		get got make made use using used want need know think see go going one two video videos please thanks thank lol bro guys
		دا في من على عن الى إلى ما ماذا كيف هل هذا هذه ذلك التي الذي و او أو مع لا نعم`) {
		stop[w] = true
	}
}

// Terms returns the lower-cased content words of a text (stop words and short words removed).
func Terms(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) < 3 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// stem drops a plural "s" so "agents" and "agent" count as the same term.
func stem(w string) string {
	if n := len([]rune(w)); n > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		return strings.TrimSuffix(w, "s")
	}
	return w
}

// TermSet is the set of stemmed content terms in a text, for asking "is this word covered anywhere".
func TermSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, w := range Terms(text) {
		set[stem(w)] = true
	}
	return set
}

// Missing returns the terms that are not in covered (a TermSet, or several merged).
func Missing(terms []string, covered map[string]bool) []string {
	var out []string
	for _, t := range terms {
		if !covered[stem(t)] {
			out = append(out, t)
		}
	}
	return out
}

// Merge adds every term of src to dst.
func Merge(dst, src map[string]bool) {
	for k := range src {
		dst[k] = true
	}
}

// MostlyMissing is true when more than half of the terms are missing (and there are terms to judge).
func MostlyMissing(terms, missing []string) bool {
	return len(terms) > 0 && len(missing)*2 > len(terms)
}
