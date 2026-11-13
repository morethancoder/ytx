package download

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
)

// Fragments of yt-dlp's stderr, lowercase. Ported from Studio's rules.go and
// extended with the not-found and network cases ytx needs.
var (
	loginPatterns = []string{
		"sign in to confirm", "sign in to view", "sign in to download", "login required", "log in to",
		"you need to log in", "private video", "this video is private", "is private", "confirm your age",
		"age-restricted", "members-only", "join this channel", "use --cookies", "--cookies-from-browser",
	}
	livePatterns = []string{
		"this live event will begin", "premieres in", "live stream recording is not available", "is live", "upcoming",
	}
	notFoundPatterns = []string{
		"video unavailable", "this video is not available", "video is unavailable", "has been removed",
		"does not exist", "no longer available", "account associated with this video has been terminated",
		"is not a valid url", "unable to extract video data", "http error 404",
	}
	networkPatterns = []string{
		"getaddrinfo", "temporary failure in name resolution", "network is unreachable",
		"connection refused", "connection reset", "timed out", "unable to download webpage",
	}
)

func containsAny(s string, patterns []string) bool {
	s = strings.ToLower(s)
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// stderrTail keeps the last n bytes (yt-dlp errors end with the useful part).
func stderrTail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// Classify maps a failed yt-dlp run to an apperr code. A cancelled ctx comes back
// as the plain ctx error so the caller sees it was the user, not YouTube.
func Classify(ctx context.Context, err error, stderr string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return toolMissing("yt-dlp", InstallHint)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return apperr.Wrap(apperr.Timeout, ctx.Err(), "yt-dlp did not finish in time")
	}
	tail := stderrTail(stderr, 400)
	switch {
	case containsAny(stderr, loginPatterns):
		return apperr.Wrap(apperr.Forbidden, err, "this video needs a signed-in session (login, age gate or private): %s", tail)
	case containsAny(stderr, livePatterns):
		return apperr.Wrap(apperr.YouTubeError, err, "this is a live or upcoming stream; try again when it has ended: %s", tail)
	case containsAny(stderr, notFoundPatterns):
		return apperr.Wrap(apperr.NotFound, err, "video not found or unavailable: %s", tail)
	case containsAny(stderr, networkPatterns):
		return apperr.Wrap(apperr.NetworkError, err, "network error while running yt-dlp: %s", tail)
	}
	return apperr.Wrap(apperr.YouTubeError, err, "yt-dlp failed: %s", tail)
}

func toolMissing(name, hint string) error {
	e := apperr.New(apperr.ToolMissing, "%s is not installed. Install it with: %s", name, hint)
	e.Detail = map[string]string{"tool": name, "install": hint}
	return e
}
