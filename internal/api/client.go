package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/quota"
	"github.com/morethancoder/ytx/internal/yt"
)

// baseURL is the API root. Tests point clients at an httptest.Server instead.
var baseURL = "https://www.googleapis.com/youtube/v3"

// Units each endpoint costs (https://developers.google.com/youtube/v3/determine_quota_cost).
const (
	costSearch = 1 // from the separate 100-calls-a-day search bucket, not the pool
	costList   = 1 // videos, channels, playlists, playlistItems and commentThreads
)

// maxPages stops paging loops that keep filtering results out (Shorts), so one
// command can't burn a day's quota.
const maxPages = 4

var httpClient = &http.Client{Timeout: 20 * time.Second}

// Client is the API-backed yt.Reader.
type Client struct {
	key  string
	q    *quota.Tracker
	base string
	http *http.Client
	now  func() time.Time
}

var _ yt.Reader = (*Client)(nil)

// New returns a reader that spends units on q. The key is sent in a header, so
// it never appears in a URL, and so never in an error message.
func New(key string, q *quota.Tracker) *Client {
	return &Client{key: key, q: q, base: baseURL, http: httpClient, now: time.Now}
}

func init() {
	yt.RegisterAPI(func(key string, q *quota.Tracker) yt.Reader { return New(key, q) })
}

type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Status string `json:"status"`
		Detail []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

const fixKey = "check that YOUTUBE_API_KEY is a valid key, that \"YouTube Data API v3\" is enabled for its Google Cloud project, and that the key has no restriction blocking this machine"

// get spends cost units, calls endpoint and decodes the JSON answer into out.
func (c *Client) get(ctx context.Context, endpoint string, cost int, params url.Values, out any) error {
	// search.list has its own daily bucket; everything else spends from the pool.
	tr, what := c.q, "pool"
	if endpoint == "search" {
		tr, what = c.q.Bucket(quota.BucketSearch, quota.BucketLimit), "daily search-call bucket"
	}
	if err := tr.Spend(cost); err != nil {
		return err
	}
	u := c.base + "/" + endpoint + "?" + params.Encode()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return apperr.From(ctx.Err())
			case <-time.After(400 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return apperr.New(apperr.Internal, "bad request for %s", endpoint)
		}
		req.Header.Set("X-Goog-Api-Key", c.key)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			// *url.Error carries the full URL; keep only the cause.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			if ctx.Err() != nil {
				return apperr.From(ctx.Err())
			}
			return apperr.Wrap(apperr.NetworkError, err, "could not reach the YouTube Data API")
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			return apperr.Wrap(apperr.NetworkError, rerr, "reading the YouTube Data API answer")
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = apperr.New(apperr.YouTubeError, "YouTube Data API answered %d on %s", resp.StatusCode, endpoint)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return c.fail(resp.StatusCode, endpoint, tr, what, body)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return apperr.Wrap(apperr.YouTubeError, err, "unreadable %s answer", endpoint)
		}
		return nil
	}
	return lastErr
}

// fail turns a Google error answer into an apperr code.
func (c *Client) fail(status int, endpoint string, tr *quota.Tracker, what string, body []byte) error {
	var e apiError
	_ = json.Unmarshal(body, &e)
	reasons := map[string]bool{}
	for _, r := range e.Error.Errors {
		reasons[r.Reason] = true
	}
	for _, r := range e.Error.Detail {
		reasons[r.Reason] = true
	}
	msg := e.Error.Message
	switch {
	case reasons["quotaExceeded"] || reasons["dailyLimitExceeded"] || (reasons["rateLimitExceeded"] && status == 403):
		tr.Exhaust()
		return apperr.New(apperr.QuotaExceeded, "the YouTube Data API says today's %s is used up (resets %s)", what, tr.Resets().Format(time.RFC3339))
	case reasons["commentsDisabled"]:
		return apperr.New(apperr.CommentsDisabled, "comments are turned off for this video")
	case reasons["keyInvalid"] || reasons["API_KEY_INVALID"] || reasons["accessNotConfigured"] || reasons["SERVICE_DISABLED"] ||
		reasons["ipRefererBlocked"] || reasons["API_KEY_HTTP_REFERRER_BLOCKED"] || reasons["API_KEY_IP_ADDRESS_BLOCKED"]:
		return apperr.New(apperr.Forbidden, "the YouTube Data API refused the key: %s", fixKey)
	case status == http.StatusNotFound || reasons["videoNotFound"] || reasons["playlistNotFound"] || reasons["channelNotFound"] || reasons["commentThreadNotFound"]:
		return apperr.New(apperr.NotFound, "YouTube found nothing for this request (%s)", endpoint)
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		return apperr.New(apperr.Forbidden, "the YouTube Data API said forbidden on %s (%s); %s", endpoint, msg, fixKey)
	}
	if msg == "" {
		msg = "no message"
	}
	return apperr.New(apperr.YouTubeError, "YouTube Data API error %d on %s: %s", status, endpoint, msg)
}
