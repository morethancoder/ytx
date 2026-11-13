// Package download owns the `download` op: it finds yt-dlp, runs it through a
// swappable Runner, and maps its failures to ytx's error codes. The yt-dlp layer
// is ported from Studio's backend/internal/fetch (tool, probe, download, exec,
// progress and rules), cut down to a single YouTube video.
package download
