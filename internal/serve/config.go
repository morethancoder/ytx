package serve

import (
	"fmt"
	"strconv"
	"time"
)

// Config holds every limit of the service. Defaults() is the only place the
// default numbers live; each one can be overridden by a flag or an env var.
type Config struct {
	Addr string

	DefaultResults int           // items returned when the caller gives no limit
	MaxResults     int           // the most items one call may ask for
	MaxBytes       int           // the most bytes one answer may carry; more is cut and flagged truncated
	MaxQueryLen    int           // longest search or suggest text, in characters
	Timeout        time.Duration // the whole time one fetch from YouTube may take
	MaxInflight    int           // YouTube fetches running at once; others wait for a slot

	CacheEntries int // most answers kept in memory
	// Time an answer is kept, per read. ErrorTTL is how long a failed read is
	// remembered so a blocked or missing item is not asked for again at once.
	SearchTTL, ChannelTTL, PlaylistTTL, CommentsTTL, SuggestTTL, ErrorTTL time.Duration
}

// Env var names, one per setting. A flag of the same meaning wins over its env var.
const (
	EnvAddr           = "YTX_SERVE_ADDR"
	EnvDefaultResults = "YTX_SERVE_DEFAULT_RESULTS"
	EnvMaxResults     = "YTX_SERVE_MAX_RESULTS"
	EnvMaxBytes       = "YTX_SERVE_MAX_BYTES"
	EnvMaxQueryLen    = "YTX_SERVE_MAX_QUERY_LEN"
	EnvTimeout        = "YTX_SERVE_TIMEOUT"
	EnvMaxInflight    = "YTX_SERVE_MAX_INFLIGHT"
	EnvCacheEntries   = "YTX_SERVE_CACHE_ENTRIES"
	EnvSearchTTL      = "YTX_SERVE_SEARCH_TTL"
	EnvChannelTTL     = "YTX_SERVE_CHANNEL_TTL"
	EnvPlaylistTTL    = "YTX_SERVE_PLAYLIST_TTL"
	EnvCommentsTTL    = "YTX_SERVE_COMMENTS_TTL"
	EnvSuggestTTL     = "YTX_SERVE_SUGGEST_TTL"
	EnvErrorTTL       = "YTX_SERVE_ERROR_TTL"
)

// Defaults is the built-in configuration. The cache times follow what ytx's
// disk cache already uses for the same reads.
func Defaults() Config {
	return Config{
		// Loopback unless told otherwise: the service has no auth, so it must
		// not listen on every interface by accident.
		Addr:           "127.0.0.1:8787",
		DefaultResults: 20,
		MaxResults:     50,
		MaxBytes:       1 << 20,
		MaxQueryLen:    200,
		Timeout:        25 * time.Second,
		MaxInflight:    8,
		CacheEntries:   2000,
		SearchTTL:      24 * time.Hour,
		ChannelTTL:     time.Hour,
		PlaylistTTL:    time.Hour,
		CommentsTTL:    6 * time.Hour,
		SuggestTTL:     6 * time.Hour,
		ErrorTTL:       30 * time.Second,
	}
}

// ApplyEnv overrides the config from env vars (getenv is os.Getenv, or a fake in tests).
func (c *Config) ApplyEnv(getenv func(string) string) error {
	str := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	var err error
	num := func(name string, dst *int) {
		if v := getenv(name); v != "" && err == nil {
			n, e := strconv.Atoi(v)
			if e != nil {
				err = fmt.Errorf("%s must be a whole number", name)
				return
			}
			*dst = n
		}
	}
	dur := func(name string, dst *time.Duration) {
		if v := getenv(name); v != "" && err == nil {
			d, e := time.ParseDuration(v)
			if e != nil {
				err = fmt.Errorf("%s must be a duration such as 30s or 6h", name)
				return
			}
			*dst = d
		}
	}
	str(EnvAddr, &c.Addr)
	num(EnvDefaultResults, &c.DefaultResults)
	num(EnvMaxResults, &c.MaxResults)
	num(EnvMaxBytes, &c.MaxBytes)
	num(EnvMaxQueryLen, &c.MaxQueryLen)
	dur(EnvTimeout, &c.Timeout)
	num(EnvMaxInflight, &c.MaxInflight)
	num(EnvCacheEntries, &c.CacheEntries)
	dur(EnvSearchTTL, &c.SearchTTL)
	dur(EnvChannelTTL, &c.ChannelTTL)
	dur(EnvPlaylistTTL, &c.PlaylistTTL)
	dur(EnvCommentsTTL, &c.CommentsTTL)
	dur(EnvSuggestTTL, &c.SuggestTTL)
	dur(EnvErrorTTL, &c.ErrorTTL)
	return err
}

// Validate refuses settings that would break the service.
func (c Config) Validate() error {
	switch {
	case c.Addr == "":
		return fmt.Errorf("the listen address is empty")
	case c.MaxResults < 1 || c.DefaultResults < 1 || c.DefaultResults > c.MaxResults:
		return fmt.Errorf("results: need 1 <= default (%d) <= max (%d)", c.DefaultResults, c.MaxResults)
	case c.MaxBytes < 1024:
		return fmt.Errorf("max bytes must be at least 1024")
	case c.MaxQueryLen < 1:
		return fmt.Errorf("max query length must be at least 1")
	case c.Timeout <= 0:
		return fmt.Errorf("the timeout must be positive")
	case c.MaxInflight < 1:
		return fmt.Errorf("max inflight must be at least 1")
	case c.CacheEntries < 1:
		return fmt.Errorf("cache entries must be at least 1")
	}
	return nil
}
