package keyless

import (
	"context"
	"encoding/base64"
	"sort"
	"time"

	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// Search filter codes in InnerTube's `params` protobuf.
const (
	sortViews = 3
	sortDate  = 2
	typeVideo = 1
	typeShort = 9
)

// maxSearchPages caps continuation requests so a filter that drops most
// results cannot loop for long.
const maxSearchPages = 25

// uploadDateFilter maps "within N days" to the nearest upload-date filter at or
// above N (so nothing inside the window is lost); results are trimmed after.
func uploadDateFilter(days int) int {
	switch {
	case days <= 0:
		return 0
	case days <= 1:
		return 2 // today
	case days <= 7:
		return 3 // this week
	case days <= 31:
		return 4 // this month
	case days <= 366:
		return 5 // this year
	}
	return 0
}

// searchParams builds the `params` value: sort, then a filter message holding
// upload date (field 1) and type (field 2).
func searchParams(sortBy, date, typ int) string {
	var f, b []byte
	if date > 0 {
		f = append(f, 0x08, byte(date))
	}
	if typ > 0 {
		f = append(f, 0x10, byte(typ))
	}
	if sortBy > 0 {
		b = append(b, 0x08, byte(sortBy))
	}
	if len(f) > 0 {
		b = append(b, 0x12, byte(len(f)))
		b = append(b, f...)
	}
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Search runs a search and follows continuations until Limit results or no more pages.
func (c *Client) Search(ctx context.Context, o yt.SearchOpts) ([]model.Video, error) {
	limit := clampLimit(o.Limit, 20)
	sortBy, typ := 0, 0
	switch o.Order {
	case "views":
		sortBy = sortViews
	case "date":
		sortBy = sortDate
	}
	switch o.Type {
	case "short":
		typ = typeShort
	case "any":
	default:
		typ = typeVideo
	}
	params := searchParams(sortBy, uploadDateFilter(o.SinceDays), typ)
	now := c.now()
	out := []model.Video{}
	seen := map[string]bool{}
	token := ""
	for page := 0; page < maxSearchPages && len(out) < limit; page++ {
		body := obj{}
		if page == 0 {
			body["query"] = o.Query
			if params != "" {
				body["params"] = params
			}
		} else {
			body["continuation"] = token
		}
		resp, err := c.post(ctx, request{endpoint: "search", client: webClient(o.Region), body: body})
		if err != nil {
			return nil, err
		}
		if page == 0 && resp["contents"] == nil && resp["onResponseReceivedCommands"] == nil {
			return nil, layoutErr("search answer has no results section")
		}
		for _, v := range dedupeVideos(collectVideos(resp, now), seen) {
			if !keepForSearch(v, o, now) {
				continue
			}
			out = append(out, v)
		}
		token = nextToken(resp)
		if token == "" {
			break
		}
	}
	if o.Order == "date" {
		// YouTube only half-applies its own date sort for some queries, so order
		// by the approximate publish time too. Items without one (Shorts) go last.
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i].Published, out[j].Published
			if a.IsZero() || b.IsZero() {
				return !a.IsZero() && b.IsZero()
			}
			return a.After(b)
		})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// keepForSearch applies the type and age filters YouTube's own filters miss.
func keepForSearch(v model.Video, o yt.SearchOpts, now time.Time) bool {
	switch o.Type {
	case "short":
		if !v.IsShort {
			return false
		}
	case "any":
	default:
		if v.IsShort {
			return false
		}
	}
	if o.SinceDays > 0 && v.PublishedText != "" {
		// "1 month ago" means 30 to 59 days, so only drop what is surely older.
		if _, minAge, ok := parseAgo(v.PublishedText, now); ok && minAge > time.Duration(o.SinceDays)*24*time.Hour {
			return false
		}
	}
	return true
}
