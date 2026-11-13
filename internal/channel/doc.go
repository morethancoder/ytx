// Package channel is the signed-in part of ytx: Google sign-in (OAuth 2.0
// installed-app loopback flow with PKCE), writes to the user's own channel
// through the YouTube Data API v3, and the channel's numbers from the YouTube
// Analytics API v2.
//
// The OAuth client and token live only in the Keychain (internal/config).
// Every write handler validates its input first, and when in.DryRun is set it
// returns a plan and makes no write call; the CLI asks, then calls it again.
package channel
