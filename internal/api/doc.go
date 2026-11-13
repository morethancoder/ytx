// Package api reads YouTube through the official Data API v3 with an API key.
// It implements yt.Reader. Every HTTP call first reserves its quota units, and
// a quotaExceeded answer from Google marks the day's quota as used up so yt.Get
// can fall back to the keyless reader. Transcripts and suggestions have no
// public API endpoint, so those return not_implemented and yt.Get falls back.
package api
