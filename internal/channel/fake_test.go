package channel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/quota"
)

// memStore is the fake Keychain.
type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) Get(a string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[a], nil
}
func (s *memStore) Set(a, v string) error { s.mu.Lock(); defer s.mu.Unlock(); s.m[a] = v; return nil }
func (s *memStore) Delete(a string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, a)
	return nil
}

// call is one request the fake Google saw.
type call struct {
	Method, Path, Query, Auth, CType string
	Body                             []byte
}

// fakeGoogle serves the token endpoint, the Data API (plain and upload) and
// the Analytics API from testdata/channel, and records every request.
type fakeGoogle struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []call
	override map[string]http.HandlerFunc // "METHOD /path"
	ms       *memStore
	pool     *quota.Tracker
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "channel", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func setup(t *testing.T) *fakeGoogle {
	t.Helper()
	home := t.TempDir()
	t.Setenv("YTX_HOME", home)
	t.Setenv("YTX_NO_KEYCHAIN", "1")
	f := &fakeGoogle{t: t, override: map[string]http.HandlerFunc{}, ms: &memStore{m: map[string]string{}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.pool = quota.NewAt(filepath.Join(home, "quota.json"), 10000, time.Now)

	saved := struct {
		data, up, an, rev string
		ep                oauth2.Endpoint
		st                secretStore
		tr                func() *quota.Tracker
		rw                time.Duration
		se                io.Writer
		ob                func(string) error
		lw                time.Duration
		cs                int64
		tty               func() bool
	}{dataBase, uploadBase, analyticsBase, revokeURL, endpoint, store, newTracker, retryWait, stderr, openBrowser, loginWait, chunkSize, stderrIsTTY}
	t.Cleanup(func() {
		dataBase, uploadBase, analyticsBase, revokeURL, endpoint = saved.data, saved.up, saved.an, saved.rev, saved.ep
		store, newTracker, retryWait, stderr, openBrowser = saved.st, saved.tr, saved.rw, saved.se, saved.ob
		loginWait, chunkSize, stderrIsTTY = saved.lw, saved.cs, saved.tty
	})
	dataBase = f.srv.URL + "/youtube/v3"
	uploadBase = f.srv.URL + "/upload/youtube/v3"
	analyticsBase = f.srv.URL + "/v2"
	revokeURL = f.srv.URL + "/revoke"
	endpoint = oauth2.Endpoint{AuthURL: f.srv.URL + "/auth", TokenURL: f.srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	store = f.ms
	newTracker = func() *quota.Tracker { return f.pool }
	retryWait = 0
	stderr = io.Discard
	stderrIsTTY = func() bool { return false }
	openBrowser = func(string) error { t.Error("unexpected browser open"); return nil }
	return f
}

// signIn stores a client and a valid token.
func (f *fakeGoogle) signIn() {
	f.ms.m[config.SecretOAuthClient] = string(fixture(f.t, "client_installed.json"))
	b, _ := json.Marshal(&oauth2.Token{AccessToken: "good-access", TokenType: "Bearer", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)})
	f.ms.m[config.SecretOAuthToken] = string(b)
}

func (f *fakeGoogle) on(key string, h http.HandlerFunc) {
	f.mu.Lock()
	f.override[key] = h
	f.mu.Unlock()
}

func (f *fakeGoogle) reset() { f.mu.Lock(); f.calls = nil; f.mu.Unlock() }

func (f *fakeGoogle) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

// writes are the requests that would change something at Google.
func (f *fakeGoogle) writes() []string {
	var out []string
	for _, c := range f.seen() {
		if c.Method != http.MethodGet && c.Path != "/token" {
			out = append(out, c.Method+" "+c.Path)
		}
	}
	return out
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
	h := f.override[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if h != nil {
		h(w, r)
		return
	}
	if r.URL.Path == "/token" || r.URL.Path == "/revoke" {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch r.Method + " " + r.URL.Path {
	case "GET /youtube/v3/videos":
		var all struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(fixture(f.t, "videos_list.json"), &all)
		want := map[string]bool{}
		for _, id := range strings.Split(q.Get("id"), ",") {
			want[id] = true
		}
		var items []map[string]any
		for _, it := range all.Items {
			if want[it["id"].(string)] {
				items = append(items, it)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case "GET /youtube/v3/channels":
		w.Write(fixture(f.t, "channels_mine.json"))
	case "GET /youtube/v3/playlistItems":
		w.Write(fixture(f.t, "playlist_items.json"))
	case "GET /youtube/v3/playlists":
		w.Write(fixture(f.t, "playlists_list.json"))
	case "GET /youtube/v3/comments":
		w.Write(fixture(f.t, "comments_list.json"))
	case "GET /youtube/v3/captions":
		w.Write(fixture(f.t, "captions_list.json"))
	case "GET /v2/reports":
		switch {
		case strings.HasPrefix(q.Get("filters"), "video=="):
			w.Write(fixture(f.t, "analytics_retention.json"))
		case q.Get("dimensions") == "video":
			w.Write(fixture(f.t, "analytics_top_videos.json"))
		default:
			w.Write(fixture(f.t, "analytics_channel.json"))
		}
	case "PUT /youtube/v3/videos":
		w.Write(body)
	case "DELETE /youtube/v3/playlistItems":
		w.WriteHeader(204)
	case "POST /youtube/v3/videos/rate", "POST /youtube/v3/comments/setModerationStatus":
		w.WriteHeader(204)
	default:
		if r.Method == http.MethodPost {
			io.WriteString(w, `{"id":"newid0000001"}`)
			return
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"code":404,"message":"not in the fake"}}`)
	}
}

func input(op string, dry bool, vals map[string]any) ops.Input {
	in := ops.Input{Op: ops.Find(op), Values: map[string]any{}, Given: map[string]bool{}, DryRun: dry}
	for k, v := range vals {
		in.Values[k] = v
		in.Given[k] = true
	}
	return in
}

func codeOf(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	if err == nil {
		return ""
	}
	return "untyped: " + err.Error()
}

// tempFile writes a file under the test's temp dir and returns its path.
func tempFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func jpeg(n int) []byte { b := make([]byte, n); copy(b, []byte{0xFF, 0xD8, 0xFF, 0xE0}); return b }
