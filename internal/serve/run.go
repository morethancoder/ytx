package serve

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Fixed protocol timings of the HTTP listener itself. The limits an operator
// tunes (fetch timeout, caps, cache times) live in Config.
const (
	shutdownGrace     = 10 * time.Second // in-flight requests finish after a stop signal
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeSlack        = 5 * time.Second // added to Config.Timeout for writing the answer
	idleTimeout       = 60 * time.Second
)

// Run listens on cfg.Addr and serves until ctx is cancelled.
func Run(ctx context.Context, cfg Config, reader Reader, version string) error {
	s := New(cfg, reader, version)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		// A fetch may use the whole timeout before the answer is written.
		WriteTimeout: cfg.Timeout + writeSlack,
		IdleTimeout:  idleTimeout,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
