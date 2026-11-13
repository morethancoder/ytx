package keyless

import (
	"sort"
	"strings"
)

// InnerTube answers are deeply nested JSON whose shape changes often. These
// helpers read them without panics: every accessor returns a zero value when a
// step of the path is missing or has another type.

type obj = map[string]any

func asMap(v any) obj {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// get follows a path of string keys and int indexes (negative counts from the end).
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m := asMap(v)
			if m == nil {
				return nil
			}
			v = m[k]
		case int:
			l := asList(v)
			if k < 0 {
				k += len(l)
			}
			if k < 0 || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// text reads YouTube's text shapes: a plain string, {simpleText}, {content}
// or {runs:[{text}]}.
func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["simpleText"].(string); ok {
			return s
		}
		if s, ok := t["content"].(string); ok {
			return s
		}
		if runs := asList(t["runs"]); runs != nil {
			var b strings.Builder
			for _, r := range runs {
				b.WriteString(str(get(r, "text")))
			}
			return b.String()
		}
	}
	return ""
}

// walk visits every key of every object under v in a stable (sorted) order.
// Arrays keep their order, which is what keeps search results in rank order.
// When fn returns false the value is not entered.
func walk(v any, fn func(key string, val any) bool) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if fn(k, t[k]) {
				walk(t[k], fn)
			}
		}
	case []any:
		for _, x := range t {
			walk(x, fn)
		}
	}
}

// findFirst returns the first value stored under key anywhere below v.
func findFirst(v any, key string) any {
	var out any
	walk(v, func(k string, val any) bool {
		if out != nil {
			return false
		}
		if k == key {
			out = val
			return false
		}
		return true
	})
	return out
}

// findAll returns every value stored under key below v, in document order.
func findAll(v any, key string) []any {
	var out []any
	walk(v, func(k string, val any) bool {
		if k == key {
			out = append(out, val)
			return false
		}
		return true
	})
	return out
}

// textsIn lists every visible text string below v (used to read header rows
// like "4.56M subscribers" whose container changes between layouts).
func textsIn(v any) []string {
	var out []string
	walk(v, func(k string, val any) bool {
		switch k {
		case "content", "simpleText":
			if s, ok := val.(string); ok && s != "" {
				out = append(out, s)
			}
		case "runs":
			if s := text(obj{"runs": val}); s != "" {
				out = append(out, s)
			}
			return false
		}
		return true
	})
	return out
}
