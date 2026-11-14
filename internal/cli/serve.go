package cli

import (
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/keyless"
	"github.com/morethancoder/ytx/internal/serve"
)

// serveCmd is `ytx serve`. It is not in the ops table: it is a long-running
// server, not a command that prints one envelope, so it has its own flags and
// exits only on SIGINT or SIGTERM. Settings come from flags, then YTX_SERVE_*
// env vars, then the defaults in serve.Defaults.
func serveCmd() *cobra.Command {
	cfg := serve.Defaults()
	envErr := cfg.ApplyEnv(os.Getenv)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve search, channel, playlist, comments and suggest as JSON over HTTP (private network only, no auth)",
		Long: `Serve the five keyless YouTube reads Power launches with as a small JSON service:
GET /v1/search, /v1/channel, /v1/playlist, /v1/comments, /v1/suggest, and /health.

There is no authentication. Run it on a private network that only your own backend
can reach, never on the public internet. It never offers video, transcript or
player reads. Each flag has a YTX_SERVE_* env var of the same meaning.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if envErr != nil {
				return emitErr(apperr.New(apperr.InvalidInput, "%s", envErr.Error()))
			}
			if err := cfg.Validate(); err != nil {
				return emitErr(apperr.New(apperr.InvalidInput, "%s", err.Error()))
			}
			if err := serve.Run(c.Context(), cfg, keyless.New(), Version); err != nil {
				return emitErr(apperr.From(err))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&cfg.Addr, "addr", cfg.Addr, "address to listen on ("+serve.EnvAddr+")")
	f.IntVar(&cfg.DefaultResults, "default-results", cfg.DefaultResults, "items returned when a call gives no limit ("+serve.EnvDefaultResults+")")
	f.IntVar(&cfg.MaxResults, "max-results", cfg.MaxResults, "most items one call may ask for ("+serve.EnvMaxResults+")")
	f.IntVar(&cfg.MaxBytes, "max-bytes", cfg.MaxBytes, "most bytes in one answer; more is cut and flagged truncated ("+serve.EnvMaxBytes+")")
	f.IntVar(&cfg.MaxQueryLen, "max-query", cfg.MaxQueryLen, "longest query text in characters ("+serve.EnvMaxQueryLen+")")
	f.DurationVar(&cfg.Timeout, "request-timeout", cfg.Timeout, "time one read from YouTube may take ("+serve.EnvTimeout+")")
	f.IntVar(&cfg.MaxInflight, "max-inflight", cfg.MaxInflight, "reads from YouTube running at once ("+serve.EnvMaxInflight+")")
	f.IntVar(&cfg.CacheEntries, "cache-entries", cfg.CacheEntries, "answers kept in memory ("+serve.EnvCacheEntries+")")
	ttl := func(dst *time.Duration, name, env, what string) {
		f.DurationVar(dst, name, *dst, "how long "+what+" answers are kept ("+env+")")
	}
	ttl(&cfg.SearchTTL, "search-ttl", serve.EnvSearchTTL, "search")
	ttl(&cfg.ChannelTTL, "channel-ttl", serve.EnvChannelTTL, "channel")
	ttl(&cfg.PlaylistTTL, "playlist-ttl", serve.EnvPlaylistTTL, "playlist")
	ttl(&cfg.CommentsTTL, "comments-ttl", serve.EnvCommentsTTL, "comments")
	ttl(&cfg.SuggestTTL, "suggest-ttl", serve.EnvSuggestTTL, "suggest")
	ttl(&cfg.ErrorTTL, "error-ttl", serve.EnvErrorTTL, "failed")
	return cmd
}
