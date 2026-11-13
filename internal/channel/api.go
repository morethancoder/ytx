package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/quota"
)

// Base URLs are vars so tests can point them at httptest servers.
var (
	dataBase      = "https://www.googleapis.com/youtube/v3"
	uploadBase    = "https://www.googleapis.com/upload/youtube/v3"
	analyticsBase = "https://youtubeanalytics.googleapis.com/v2"
)

// transport is shared by every call. It has connect and header timeouts but
// no overall deadline, because an upload can rightly take an hour; the
// command's ctx bounds everything else.
var transport http.RoundTripper = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 2 * time.Minute,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConnsPerHost:   4,
}

// baseClient is for the unsigned calls (token exchange, refresh, revoke).
var baseClient = &http.Client{Transport: transport, Timeout: 60 * time.Second}

var (
	// newTracker returns the 10,000-unit pool counter; tests swap it for a temp file.
	newTracker = func() *quota.Tracker {
		cfg, _ := config.Load()
		if cfg == nil {
			cfg = &config.Config{}
		}
		return quota.New(cfg.Limit())
	}
	// retryWait is the pause before the one retry on 429/5xx.
	retryWait = 1500 * time.Millisecond
	// stderrIsTTY decides whether upload progress is shown.
	stderrIsTTY = func() bool { return term.IsTerminal(int(os.Stderr.Fd())) }
)

// session is one command's signed-in connection: the OAuth client, the quota
// counters, and the pool units this command spent.
type session struct {
	hc      *http.Client
	pool    *quota.Tracker
	uploads *quota.Tracker
	used    int
}

// open signs in (from the Keychain) and returns a session, or login_required.
func open(ctx context.Context) (*session, error) {
	hc, err := HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	pool := newTracker()
	return &session{hc: hc, pool: pool, uploads: pool.Bucket(quota.BucketUploads, quota.BucketLimit)}, nil
}

// request is one API call.
type request struct {
	method string
	url    string
	header http.Header
	body   []byte
	// units are spent from bucket (the pool when nil) before the call.
	units  int
	bucket *quota.Tracker
	// analytics marks an Analytics API call. Its quota is separate at Google
	// and ytx doesn't count it, so its quotaExceeded must not mark the Data
	// API pool as used up.
	analytics bool
}

func endpointURL(base, path string, q url.Values) string {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// spend reserves units before a call, as CLAUDE.md asks.
func (s *session) spend(units int, bucket *quota.Tracker) error {
	if units <= 0 {
		return nil
	}
	if bucket == nil {
		if err := s.pool.Spend(units); err != nil {
			return err
		}
		s.used += units
		return nil
	}
	return bucket.Spend(units)
}

// send makes one call and maps Google's errors to apperr codes. It returns
// the response body on 2xx. GET, PUT and DELETE get at most one retry on a
// network error or 429/5xx because repeating them changes nothing. POST does
// not: a comment, playlist or caption Google saved before the answer was lost
// would be created twice.
func (s *session) send(ctx context.Context, r request) (*http.Response, []byte, error) {
	if err := s.spend(r.units, r.bucket); err != nil {
		return nil, nil, err
	}
	retry := r.method == http.MethodGet || r.method == http.MethodPut || r.method == http.MethodDelete
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, r.method, r.url, bytes.NewReader(r.body))
		if err != nil {
			return nil, nil, apperr.Wrap(apperr.Internal, err, "bad request")
		}
		if r.body == nil {
			req.Body = http.NoBody
			req.ContentLength = 0
		}
		for k, v := range r.header {
			req.Header[k] = v
		}
		resp, err := s.hc.Do(req)
		if err != nil {
			var ae *apperr.Error
			if errors.As(err, &ae) {
				return nil, nil, ae
			}
			if retry && attempt == 0 && ctx.Err() == nil {
				if werr := wait(ctx, retryWait); werr == nil {
					continue
				}
			}
			if !retry && ctx.Err() == nil {
				return nil, nil, apperr.Wrap(apperr.NetworkError, err, "the connection broke during a write, so it may already have happened; check before retrying")
			}
			return nil, nil, apperr.From(err)
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, nil, apperr.From(rerr)
		}
		transient := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if transient && retry && attempt == 0 {
			if werr := wait(ctx, retryWait); werr == nil {
				continue
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			gerr := s.googleError(resp.StatusCode, b, r.bucket, r.analytics)
			if transient && !retry && resp.StatusCode >= 500 {
				return resp, b, apperr.Wrap(apperr.YouTubeError, gerr, "YouTube failed during a write, so it may already have happened; check before retrying")
			}
			return resp, b, gerr
		}
		return resp, b, nil
	}
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getJSON / postJSON are the common shapes of send.
func (s *session) getJSON(ctx context.Context, u string, units int, out any) error {
	_, b, err := s.send(ctx, request{method: http.MethodGet, url: u, units: units})
	if err != nil {
		return err
	}
	return decode(b, out)
}

func (s *session) writeJSON(ctx context.Context, method, u string, body any, units int, out any) error {
	var raw []byte
	hdr := http.Header{}
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return apperr.Wrap(apperr.Internal, err, "encode request")
		}
		hdr.Set("Content-Type", "application/json; charset=UTF-8")
	}
	_, b, err := s.send(ctx, request{method: method, url: u, header: hdr, body: raw, units: units})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return decode(b, out)
}

func decode(b []byte, out any) error {
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return apperr.Wrap(apperr.YouTubeError, err, "couldn't read YouTube's answer")
	}
	return nil
}

// googleError turns Google's {"error": {...}} into an apperr code. bucket is
// the counter the call spent from (nil = the pool), so a quotaExceeded marks
// the right one as used up. analytics calls mark nothing: their quota is
// Google's separate Analytics API quota.
func (s *session) googleError(status int, body []byte, bucket *quota.Tracker, analytics bool) error {
	var g struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &g)
	msg := g.Error.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	reason := ""
	if len(g.Error.Errors) > 0 {
		reason = g.Error.Errors[0].Reason
	}
	switch reason {
	case "quotaExceeded", "dailyLimitExceeded":
		if analytics {
			return apperr.New(apperr.QuotaExceeded, "YouTube Analytics says its quota is used up for now; Data API commands still work: %s", msg)
		}
		if bucket != nil {
			bucket.Exhaust()
			return apperr.New(apperr.QuotaExceeded, "YouTube says today's upload calls are used up (resets at midnight Pacific): %s", msg)
		}
		s.pool.Exhaust()
		return apperr.New(apperr.QuotaExceeded, "YouTube says today's API quota is used up (resets at midnight Pacific): %s", msg)
	case "uploadLimitExceeded":
		return apperr.New(apperr.QuotaExceeded, "YouTube says this channel has reached its upload limit for now: %s", msg)
	case "commentsDisabled":
		return apperr.New(apperr.CommentsDisabled, "comments are turned off on this video")
	case "insufficientPermissions", "insufficientScopes", "ACCESS_TOKEN_SCOPE_INSUFFICIENT":
		return apperr.New(apperr.Forbidden, "the sign-in lacks a permission this needs (%s); run `ytx login` again and tick every box", msg)
	case "youtubeSignupRequired":
		return apperr.New(apperr.Forbidden, "this Google account has no YouTube channel yet")
	}
	switch {
	case status == http.StatusUnauthorized:
		return apperr.New(apperr.LoginRequired, "Google refused the sign-in (%s); run `%s`", msg, LoginFix)
	case status == http.StatusForbidden:
		if reason != "" {
			return apperr.New(apperr.Forbidden, "YouTube refused (%s): %s", reason, msg)
		}
		return apperr.New(apperr.Forbidden, "YouTube refused: %s", msg)
	case status == http.StatusNotFound:
		return apperr.New(apperr.NotFound, "YouTube found nothing: %s", msg)
	case status == http.StatusBadRequest:
		return apperr.New(apperr.InvalidInput, "YouTube rejected the request: %s", msg)
	}
	return apperr.New(apperr.YouTubeError, "YouTube answered %d: %s", status, msg)
}

// ---- small reads shared by plans and handlers

type ytChannel struct {
	ID      string `json:"id"`
	Title   string
	Uploads string
}

func (s *session) myChannel(ctx context.Context) (*ytChannel, error) {
	var r struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	q := url.Values{"part": {"snippet,contentDetails"}, "mine": {"true"}}
	if err := s.getJSON(ctx, endpointURL(dataBase, "/channels", q), 1, &r); err != nil {
		return nil, err
	}
	if len(r.Items) == 0 {
		return nil, apperr.New(apperr.Forbidden, "this Google account has no YouTube channel yet")
	}
	it := r.Items[0]
	return &ytChannel{ID: it.ID, Title: it.Snippet.Title, Uploads: it.ContentDetails.RelatedPlaylists.Uploads}, nil
}

// videoItem keeps snippet and status as maps so an edit can send back every
// writable field it didn't change (videos.update clears what is left out).
type videoItem struct {
	ID             string         `json:"id"`
	Snippet        map[string]any `json:"snippet"`
	Status         map[string]any `json:"status"`
	ContentDetails struct {
		Duration string `json:"duration"`
	} `json:"contentDetails"`
	Statistics map[string]any `json:"statistics"`
}

func (v *videoItem) title() string   { return str(v.Snippet, "title") }
func (v *videoItem) privacy() string { return str(v.Status, "privacyStatus") }

// videos reads up to 50 videos per call (1 unit each call).
func (s *session) videos(ctx context.Context, ids []string, part string) (map[string]*videoItem, error) {
	out := map[string]*videoItem{}
	for i := 0; i < len(ids); i += 50 {
		end := min(i+50, len(ids))
		var r struct {
			Items []*videoItem `json:"items"`
		}
		q := url.Values{"part": {part}, "id": {joinComma(ids[i:end])}, "maxResults": {"50"}}
		if err := s.getJSON(ctx, endpointURL(dataBase, "/videos", q), 1, &r); err != nil {
			return nil, err
		}
		for _, it := range r.Items {
			out[it.ID] = it
		}
	}
	return out, nil
}

func (s *session) video(ctx context.Context, id, part string) (*videoItem, error) {
	m, err := s.videos(ctx, []string{id}, part)
	if err != nil {
		return nil, err
	}
	v := m[id]
	if v == nil {
		return nil, apperr.New(apperr.NotFound, "video %s not found, or not visible to this account", id)
	}
	return v, nil
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	if v, ok := m[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func joinComma(s []string) string {
	var b bytes.Buffer
	for i, x := range s {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(x)
	}
	return b.String()
}
