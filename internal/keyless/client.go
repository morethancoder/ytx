package keyless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/yt"
)

func init() {
	yt.RegisterKeyless(func() yt.Reader { return New() })
}

// Default endpoints. A test points them at an httptest.Server.
const (
	DefaultBase        = "https://www.youtube.com"
	DefaultSuggestBase = "https://suggestqueries.google.com"
	DefaultChartsBase  = "https://charts.youtube.com"
)

const (
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	// webVersion is only a label; InnerTube accepts old WEB versions for years.
	webVersion = "2.20260101.00.00"
)

// sharedHTTP is the one client every Client uses unless a test swaps it.
var sharedHTTP = &http.Client{Timeout: 30 * time.Second}

// Client is the keyless reader. The zero value is not usable; call New.
type Client struct {
	Base        string // youtube.com, for the InnerTube endpoints
	SuggestBase string
	ChartsBase  string
	HTTP        *http.Client
	Now         func() time.Time
	// YTDLP is the yt-dlp binary used as the last transcript fallback. "" means
	// "yt-dlp" from PATH; "off" turns the fallback off (used by offline tests).
	YTDLP string
	// Backoff is the wait before the one retry on 429/5xx.
	Backoff time.Duration
}

// New returns a Client pointed at the real YouTube.
func New() *Client {
	return &Client{Base: DefaultBase, SuggestBase: DefaultSuggestBase, ChartsBase: DefaultChartsBase,
		HTTP: sharedHTTP, Now: time.Now, Backoff: 600 * time.Millisecond}
}

var _ yt.Reader = (*Client)(nil)

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// webClient is the WEB client context. hl=en keeps number formats stable;
// gl is the region (left out when empty).
func webClient(region string) obj {
	cl := obj{"clientName": "WEB", "clientVersion": webVersion, "hl": "en"}
	if region != "" {
		cl["gl"] = strings.ToUpper(region)
	}
	return cl
}

// request describes one POST to an InnerTube endpoint.
type request struct {
	base     string // empty means c.Base
	endpoint string // "search", "player", "next", "browse", "navigation/resolve_url"
	client   obj
	body     obj
	userAgnt string
	headers  map[string]string
}

// post sends the request and returns the decoded JSON object.
func (c *Client) post(ctx context.Context, r request) (obj, error) {
	body := obj{"context": obj{"client": r.client}}
	for k, v := range r.body {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "encode request")
	}
	base := r.base
	if base == "" {
		base = c.Base
	}
	url := strings.TrimRight(base, "/") + "/youtubei/v1/" + r.endpoint + "?prettyPrint=false"
	ua := r.userAgnt
	if ua == "" {
		ua = userAgent
	}
	raw, status, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Origin", DefaultBase)
		// A consent cookie keeps requests from EU addresses off the consent wall.
		req.Header.Set("Cookie", "SOCS=CAI")
		for k, v := range r.headers {
			req.Header.Set(k, v)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var out obj
	if jerr := json.Unmarshal(raw, &out); jerr != nil || out == nil {
		if status >= 400 {
			return nil, statusErr(status, r.endpoint)
		}
		return nil, apperr.New(apperr.YouTubeError, "%s: the answer was not JSON", r.endpoint)
	}
	if status >= 400 {
		msg := str(get(out, "error", "message"))
		if status == 404 {
			return nil, apperr.New(apperr.NotFound, "%s: %s", r.endpoint, orDefault(msg, "not found"))
		}
		return nil, apperr.New(apperr.YouTubeError, "%s: HTTP %d %s", r.endpoint, status, msg)
	}
	return out, nil
}

func statusErr(status int, what string) error {
	if status == 404 {
		return apperr.New(apperr.NotFound, "%s: not found", what)
	}
	return apperr.New(apperr.YouTubeError, "%s: HTTP %d", what, status)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// getRaw fetches a plain URL (suggest, captions, charts' non-JSON helpers).
func (c *Client) getRaw(ctx context.Context, url string, headers map[string]string) ([]byte, int, error) {
	return c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req, nil
	})
}

// do runs a request with one retry on 429 or 5xx and returns the body and status.
func (c *Client) do(ctx context.Context, build func() (*http.Request, error)) ([]byte, int, error) {
	hc := c.HTTP
	if hc == nil {
		hc = sharedHTTP
	}
	var lastStatus int
	for attempt := 0; attempt < 2; attempt++ {
		req, err := build()
		if err != nil {
			return nil, 0, apperr.Wrap(apperr.Internal, err, "build request")
		}
		resp, err := hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, apperr.From(ctx.Err())
			}
			return nil, 0, apperr.Wrap(apperr.NetworkError, err, "network error")
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, 0, apperr.Wrap(apperr.NetworkError, rerr, "reading the answer")
		}
		lastStatus = resp.StatusCode
		if (lastStatus == 429 || lastStatus >= 500) && attempt == 0 {
			select {
			case <-time.After(c.Backoff):
			case <-ctx.Done():
				return nil, 0, apperr.From(ctx.Err())
			}
			continue
		}
		return raw, lastStatus, nil
	}
	return nil, lastStatus, apperr.New(apperr.YouTubeError, "HTTP %d", lastStatus)
}

// layoutErr is the error for a response whose shape this reader cannot read.
func layoutErr(format string, args ...any) error {
	return apperr.New(apperr.YouTubeError, "unreadable YouTube layout: "+format, args...)
}

// isAppErr reports whether err already carries the given code.
func isAppErr(err error, code string) bool {
	var e *apperr.Error
	return errors.As(err, &e) && e.Code == code
}

func clampLimit(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// asAppErr is errors.As for *apperr.Error.
func asAppErr(err error, target **apperr.Error) bool {
	return errors.As(err, target)
}
