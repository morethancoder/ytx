package channel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
)

func TestHTTPClientNeedsLogin(t *testing.T) {
	f := setup(t)
	_, err := HTTPClient(context.Background())
	if codeOf(err) != apperr.LoginRequired || !strings.Contains(err.Error(), LoginFix) {
		t.Fatalf("no token: got %v", err)
	}
	// A token without a client is no sign-in either.
	f.signIn()
	delete(f.ms.m, config.SecretOAuthClient)
	if _, err := HTTPClient(context.Background()); codeOf(err) != apperr.LoginRequired {
		t.Fatalf("no client: got %v", err)
	}
}

func TestParseClient(t *testing.T) {
	setup(t)
	if _, err := parseClient(fixture(t, "client_installed.json")); err != nil {
		t.Fatalf("installed client refused: %v", err)
	}
	for name, b := range map[string][]byte{
		"web":     fixture(t, "client_web.json"),
		"garbage": []byte("not json"),
		"badid":   []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`),
		"nosec":   []byte(`{"installed":{"client_id":"1-a.apps.googleusercontent.com"}}`),
	} {
		if _, err := parseClient(b); codeOf(err) != apperr.InvalidInput {
			t.Errorf("%s: want invalid_input, got %v", name, err)
		}
	}
}

// fakeBrowser plays the Google consent page: it checks the auth URL, then
// sends a forged redirect (wrong state) and, when good is set, the real one.
type fakeBrowser struct {
	t          *testing.T
	good       bool
	mu         sync.Mutex
	challenge  string
	forgedCode int
}

func (b *fakeBrowser) forged() int { b.mu.Lock(); defer b.mu.Unlock(); return b.forgedCode }

func (b *fakeBrowser) open(u string) error {
	pu, err := url.Parse(u)
	if err != nil {
		b.t.Fatal(err)
	}
	q := pu.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		b.t.Errorf("auth URL lacks PKCE S256: %s", u)
	}
	if len(q.Get("state")) < 32 || q.Get("access_type") != "offline" {
		b.t.Errorf("auth URL lacks a strong state or offline access: %s", u)
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
		b.t.Errorf("redirect is not loopback: %s", q.Get("redirect_uri"))
	}
	for _, s := range Scopes {
		if !strings.Contains(q.Get("scope"), s) {
			b.t.Errorf("scope %s missing", s)
		}
	}
	b.mu.Lock()
	b.challenge = q.Get("code_challenge")
	b.mu.Unlock()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	go func() {
		resp, err := http.Get(redirect + "?state=forged&code=evil-code")
		if err != nil {
			b.t.Error(err)
			return
		}
		resp.Body.Close()
		b.mu.Lock()
		b.forgedCode = resp.StatusCode
		b.mu.Unlock()
		if !b.good {
			return
		}
		resp, err = http.Get(redirect + "?state=" + url.QueryEscape(state) + "&code=good-code")
		if err != nil {
			b.t.Error(err)
			return
		}
		resp.Body.Close()
	}()
	return nil
}

func TestLoginPKCEAndState(t *testing.T) {
	f := setup(t)
	br := &fakeBrowser{t: t, good: true}
	openBrowser = br.open
	var exchanged []string
	f.on("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		exchanged = append(exchanged, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		br.mu.Lock()
		ok := base64.RawURLEncoding.EncodeToString(sum[:]) == br.challenge
		br.mu.Unlock()
		if r.Form.Get("code") != "good-code" || !ok || r.Form.Get("grant_type") != "authorization_code" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"a1","refresh_token":"r1","token_type":"Bearer","expires_in":3600,"scope":"`+strings.Join(Scopes, " ")+`"}`)
	})
	clientPath := tempFile(t, "client.json", fixture(t, "client_installed.json"))
	res, err := login(context.Background(), input("auth.login", false, map[string]any{"client": clientPath}))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if br.forged() != http.StatusBadRequest {
		t.Errorf("forged state answered %d, want 400", br.forged())
	}
	if len(exchanged) != 1 || exchanged[0] != "good-code" {
		t.Errorf("exchanged codes %v, want only good-code", exchanged)
	}
	tok := &oauth2.Token{}
	if err := json.Unmarshal([]byte(f.ms.m[config.SecretOAuthToken]), tok); err != nil || tok.RefreshToken != "r1" {
		t.Errorf("token not stored: %v %+v", err, tok)
	}
	if !strings.Contains(f.ms.m[config.SecretOAuthClient], "apps.googleusercontent.com") {
		t.Error("client not stored")
	}
	data := res.Data.(map[string]any)
	if data["signed_in"] != true || data["channel"] == nil || len(res.Warnings) != 0 {
		t.Errorf("result %+v warnings %v", data, res.Warnings)
	}
}

func TestLoginWrongStateOnlyTimesOut(t *testing.T) {
	f := setup(t)
	br := &fakeBrowser{t: t, good: false}
	openBrowser = br.open
	loginWait = 400 * time.Millisecond
	f.ms.m[config.SecretOAuthClient] = string(fixture(t, "client_installed.json"))
	_, err := login(context.Background(), input("auth.login", false, nil))
	if codeOf(err) != apperr.Timeout {
		t.Fatalf("want timeout, got %v", err)
	}
	if br.forged() != http.StatusBadRequest {
		t.Errorf("forged state answered %d, want 400", br.forged())
	}
	if f.ms.m[config.SecretOAuthToken] != "" {
		t.Error("a token was stored from a forged redirect")
	}
	for _, c := range f.seen() {
		if c.Path == "/token" {
			t.Error("the forged code was exchanged")
		}
	}
}

func TestLoginNeedsClient(t *testing.T) {
	setup(t)
	if _, err := login(context.Background(), input("auth.login", false, nil)); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("want invalid_input, got %v", err)
	}
}

func TestLoginRefusesWithKeychainOff(t *testing.T) {
	setup(t)
	store = keychain{}
	if _, err := login(context.Background(), input("auth.login", false, nil)); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("want invalid_input, got %v", err)
	}
}

func TestRefreshSavedBack(t *testing.T) {
	f := setup(t)
	f.signIn()
	b, _ := json.Marshal(&oauth2.Token{AccessToken: "old-access", TokenType: "Bearer", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	f.ms.m[config.SecretOAuthToken] = string(b)
	s, err := open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.myChannel(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawRefresh, sawNew bool
	for _, c := range f.seen() {
		if c.Path == "/token" && strings.Contains(string(c.Body), "grant_type=refresh_token") && strings.Contains(string(c.Body), "refresh_token=r1") {
			sawRefresh = true
		}
		if c.Path == "/youtube/v3/channels" && c.Auth == "Bearer new-access" {
			sawNew = true
		}
	}
	if !sawRefresh || !sawNew {
		t.Fatalf("refresh %v, new token used %v", sawRefresh, sawNew)
	}
	tok := &oauth2.Token{}
	_ = json.Unmarshal([]byte(f.ms.m[config.SecretOAuthToken]), tok)
	if tok.AccessToken != "new-access" || tok.RefreshToken != "r1" {
		t.Fatalf("refreshed token not saved back: %+v", tok)
	}
}

func TestRevokedRefreshIsLoginRequired(t *testing.T) {
	f := setup(t)
	f.signIn()
	b, _ := json.Marshal(&oauth2.Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	f.ms.m[config.SecretOAuthToken] = string(b)
	f.on("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	})
	_, err := meChannel(context.Background(), input("me.channel", false, nil))
	if codeOf(err) != apperr.LoginRequired {
		t.Fatalf("want login_required, got %v", err)
	}
}

func TestLogout(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := logout(context.Background(), input("auth.logout", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if f.ms.m[config.SecretOAuthToken] != "" || f.ms.m[config.SecretOAuthClient] == "" {
		t.Fatal("token should be gone and the client kept")
	}
	var revoked bool
	for _, c := range f.seen() {
		revoked = revoked || (c.Path == "/revoke" && strings.Contains(string(c.Body), "token=r1"))
	}
	if !revoked || res.Data.(map[string]any)["revoked"] != true {
		t.Fatal("refresh token was not revoked")
	}

	// A failing revoke is only a warning.
	f.signIn()
	f.on("POST /revoke", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	res, err = logout(context.Background(), input("auth.logout", false, nil))
	if err != nil || len(res.Warnings) != 1 || f.ms.m[config.SecretOAuthToken] != "" {
		t.Fatalf("revoke failure: err %v warnings %v", err, res.Warnings)
	}
}

// With no client stored or passed, login reads ytx home's client_secret.json and gets as far
// as the browser step (the wrong-state browser makes it time out there).
func TestLoginReadsClientFromHome(t *testing.T) {
	setup(t)
	br := &fakeBrowser{t: t, good: false}
	openBrowser = br.open
	loginWait = 400 * time.Millisecond
	if _, err := login(context.Background(), input("auth.login", false, nil)); codeOf(err) != apperr.InvalidInput ||
		!strings.Contains(err.Error(), defaultClientPath()) {
		t.Fatalf("no client anywhere: want invalid_input naming %s, got %v", defaultClientPath(), err)
	}
	if err := os.WriteFile(defaultClientPath(), fixture(t, "client_installed.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := login(context.Background(), input("auth.login", false, nil)); codeOf(err) != apperr.Timeout {
		t.Fatalf("client in home: want timeout at the browser step, got %v", err)
	}
}
