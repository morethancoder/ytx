// Package serve is `ytx serve`: the five keyless reads Power launches with
// (search, channel, playlist, comments, suggest) as a small JSON-over-HTTP
// service.
//
// It has no authentication on purpose. It is meant to run on a private
// network that only Power's backend can reach, never on the public internet.
//
// It never offers video, transcript or player reads: YouTube refuses the
// player endpoint from datacenter addresses, and the Reader interface used here
// does not even have those methods, so they cannot be added by accident.
//
// Every answer is JSON. Errors carry one plain code and nothing from YouTube
// (no text, no URLs, no stacks); the full detail goes to the server's stderr.
package serve
