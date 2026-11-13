// Package keyless reads public YouTube data without an API key. It talks to
// InnerTube, the JSON endpoints youtube.com calls itself (search, player, next,
// browse, navigation/resolve_url), plus two small public helpers: the
// suggestqueries autocomplete and the YouTube Charts feed. It implements
// yt.Reader and registers itself in init.
//
// Everything here parses responses defensively: a missing field becomes a zero
// value, and a layout it cannot read becomes apperr.YouTubeError with a short
// reason, never a panic.
package keyless
