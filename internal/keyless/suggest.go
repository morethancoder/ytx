package keyless

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/morethancoder/ytx/internal/apperr"
)

// Suggest returns YouTube's search autocomplete for a query. The answer is
// JSONP ("window.google.ac.h([...])"); hl and gl come from lang and region.
func (c *Client) Suggest(ctx context.Context, query, lang, region string) ([]string, error) {
	q := url.Values{}
	q.Set("client", "youtube")
	q.Set("ds", "yt")
	q.Set("q", query)
	q.Set("oe", "utf-8")
	if lang != "" {
		q.Set("hl", lang)
	}
	if region != "" {
		q.Set("gl", strings.ToLower(region))
	}
	base := c.SuggestBase
	if base == "" {
		base = DefaultSuggestBase
	}
	raw, status, err := c.getRaw(ctx, strings.TrimRight(base, "/")+"/complete/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, apperr.New(apperr.YouTubeError, "suggest answered HTTP %d", status)
	}
	return parseSuggest(raw)
}

func parseSuggest(raw []byte) ([]string, error) {
	if !utf8.Valid(raw) {
		// Some answers still come as Latin-1 despite oe=utf-8.
		r := make([]rune, len(raw))
		for i, b := range raw {
			r[i] = rune(b)
		}
		raw = []byte(string(r))
	}
	s := string(raw)
	start, end := strings.Index(s, "("), strings.LastIndex(s, ")")
	if start < 0 || end <= start {
		return nil, apperr.New(apperr.YouTubeError, "suggest answer is not JSONP")
	}
	var top []json.RawMessage
	if err := json.Unmarshal([]byte(s[start+1:end]), &top); err != nil || len(top) < 2 {
		return nil, apperr.New(apperr.YouTubeError, "suggest answer is not readable")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(top[1], &items); err != nil {
		return nil, apperr.New(apperr.YouTubeError, "suggest list is not readable")
	}
	out := []string{}
	for _, it := range items {
		var row []json.RawMessage
		if json.Unmarshal(it, &row) != nil || len(row) == 0 {
			continue
		}
		var text string
		if json.Unmarshal(row[0], &text) == nil && text != "" {
			out = append(out, text)
		}
	}
	return out, nil
}
