package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/ops"
)

// Scopes are what ytx asks Google for: manage the channel (force-ssl covers
// edits, comments, captions and ratings), upload, and read analytics.
var Scopes = []string{
	"https://www.googleapis.com/auth/youtube.force-ssl",
	"https://www.googleapis.com/auth/youtube.upload",
	"https://www.googleapis.com/auth/yt-analytics.readonly",
}

// LoginFix is the command that fixes a missing or expired sign-in.
const LoginFix = "ytx login --client client_secret.json"

// The Google endpoints are pinned here rather than taken from the client
// file, so a tampered client file can't send the code or token elsewhere.
// They are vars so tests can point them at an httptest server.
var (
	endpoint = oauth2.Endpoint{
		AuthURL:   "https://accounts.google.com/o/oauth2/auth",
		TokenURL:  "https://oauth2.googleapis.com/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	revokeURL = "https://oauth2.googleapis.com/revoke"
	// loginWait is how long login waits for the browser to come back.
	loginWait = 5 * time.Minute
	// stderr gets the sign-in URL and what login is waiting for.
	stderr io.Writer = os.Stderr
	// openBrowser opens the sign-in page; tests replace it with a fake browser.
	openBrowser = defaultOpenBrowser
)

// ---- secret store

// secretStore is where the sign-in lives. The real one is the Keychain through
// internal/config; tests swap in a map so they never touch the Keychain.
type secretStore interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

type keychain struct{}

func (keychain) Get(a string) (string, error) { return config.Secret(a) }
func (keychain) Set(a, v string) error        { return config.SetSecret(a, v) }
func (keychain) Delete(a string) error        { return config.DeleteSecret(a) }

var store secretStore = keychain{}

// storeReady fails before the browser opens when the sign-in could not be
// saved afterwards (the Keychain is turned off).
func storeReady() error {
	if _, real := store.(keychain); real && os.Getenv("YTX_NO_KEYCHAIN") != "" {
		return apperr.New(apperr.InvalidInput, "the Keychain is off (YTX_NO_KEYCHAIN is set), so a sign-in could not be stored")
	}
	return nil
}

// ---- OAuth client file

// clientInfo is the part of Google's client JSON ytx uses.
type clientInfo struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// parseClient checks a Google Cloud OAuth client JSON and returns its id and secret.
func parseClient(b []byte) (*clientInfo, error) {
	var f struct {
		Installed *clientInfo     `json:"installed"`
		Web       json.RawMessage `json:"web"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file is not JSON from Google Cloud (download it from APIs & Services > Credentials)")
	}
	if f.Installed == nil {
		if f.Web != nil {
			return nil, apperr.New(apperr.InvalidInput, "this is a Web application client; create an OAuth client of type Desktop app and download its JSON")
		}
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file has no \"installed\" section; create an OAuth client of type Desktop app and download its JSON")
	}
	ci := f.Installed
	ci.ClientID = strings.TrimSpace(ci.ClientID)
	ci.ClientSecret = strings.TrimSpace(ci.ClientSecret)
	if !strings.HasSuffix(ci.ClientID, ".apps.googleusercontent.com") {
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file's client_id doesn't look like a Google client id")
	}
	if ci.ClientSecret == "" {
		return nil, apperr.New(apperr.InvalidInput, "the OAuth client file has no client_secret")
	}
	return ci, nil
}

func oauthConfig(ci *clientInfo, redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: ci.ClientID, ClientSecret: ci.ClientSecret, Endpoint: endpoint, RedirectURL: redirect, Scopes: Scopes}
}

// httpCtx makes oauth2's own token calls use ytx's client with timeouts.
func httpCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, baseClient)
}

// ---- token

func loadToken() (*oauth2.Token, error) {
	s, err := store.Get(config.SecretOAuthToken)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't read the sign-in from the Keychain")
	}
	if s == "" {
		return nil, nil
	}
	t := &oauth2.Token{}
	if err := json.Unmarshal([]byte(s), t); err != nil {
		return nil, apperr.New(apperr.LoginRequired, "the stored sign-in is unreadable; run `%s`", LoginFix)
	}
	return t, nil
}

func saveToken(t *oauth2.Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return store.Set(config.SecretOAuthToken, string(b))
}

// savingSource writes every new access token back to the store, so a refresh
// made by one command is reused by the next instead of refreshing again.
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.ErrorCode == "unauthorized_client" || re.ErrorCode == "invalid_client") {
			return nil, apperr.New(apperr.LoginRequired, "the Google sign-in expired or was revoked (%s); run `%s`", re.ErrorCode, LoginFix)
		}
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		// A failed save only costs one extra refresh next time, so it doesn't fail the call.
		_ = saveToken(t)
	}
	return t, nil
}

// HTTPClient returns an HTTP client signed in as the user's Google account. It
// refreshes the access token when needed and saves the new one in the
// Keychain. It returns apperr.LoginRequired when nobody is signed in.
func HTTPClient(ctx context.Context) (*http.Client, error) {
	tok, err := loadToken()
	if err != nil {
		return nil, err
	}
	if tok == nil || (tok.AccessToken == "" && tok.RefreshToken == "") {
		return nil, apperr.New(apperr.LoginRequired, "not signed in with Google; run `%s`", LoginFix)
	}
	raw, err := store.Get(config.SecretOAuthClient)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't read the OAuth client from the Keychain")
	}
	ci, err := parseClient([]byte(raw))
	if raw == "" || err != nil {
		return nil, apperr.New(apperr.LoginRequired, "no OAuth client is stored; run `%s`", LoginFix)
	}
	src := &savingSource{src: oauthConfig(ci, "").TokenSource(httpCtx(ctx), tok), last: tok.AccessToken}
	return &http.Client{Transport: &oauth2.Transport{Source: src, Base: transport}}, nil
}

// ---- login

func login(ctx context.Context, in ops.Input) (*ops.Result, error) {
	if err := storeReady(); err != nil {
		return nil, err
	}
	var raw []byte
	if p := in.Str("client"); p != "" {
		b, err := readLimited(config.ExpandHome(p), 64<<10, "OAuth client file")
		if err != nil {
			return nil, err
		}
		raw = b
	} else {
		s, err := store.Get(config.SecretOAuthClient)
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "couldn't read the OAuth client from the Keychain")
		}
		if s == "" {
			return nil, apperr.New(apperr.InvalidInput, "no OAuth client stored yet: pass --client client_secret.json "+
				"(Google Cloud console > APIs & Services > Credentials > Create credentials > OAuth client ID > Desktop app)")
		}
		raw = []byte(s)
	}
	ci, err := parseClient(raw)
	if err != nil {
		return nil, err
	}
	tok, err := loopbackLogin(ctx, ci)
	if err != nil {
		return nil, err
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "the OAuth client file is not valid JSON")
	}
	if err := store.Set(config.SecretOAuthClient, compact.String()); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't store the OAuth client in the Keychain")
	}
	if err := saveToken(tok); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't store the sign-in in the Keychain")
	}

	res := &ops.Result{Meta: ops.Meta{Source: "oauth"}}
	data := map[string]any{"signed_in": true, "stored": "keychain", "scopes": grantedScopes(tok)}
	if tok.RefreshToken == "" {
		res.Warnings = append(res.Warnings, "Google sent no refresh token, so the sign-in lasts about an hour; remove ytx's access at myaccount.google.com/permissions and log in again")
	}
	if missing := missingScopes(tok); len(missing) > 0 {
		res.Warnings = append(res.Warnings, "Google didn't grant: "+strings.Join(missing, ", ")+"; commands that need them will answer forbidden until you log in again and tick every box")
	}
	// Naming the channel confirms the sign-in works; it costs 1 unit and is optional.
	if s, err := open(ctx); err == nil {
		if ch, err := s.myChannel(ctx); err == nil {
			data["channel"] = map[string]any{"id": ch.ID, "title": ch.Title}
		} else {
			res.Warnings = append(res.Warnings, "signed in, but couldn't read the channel: "+apperr.From(err).Message)
		}
		res.Meta.QuotaUsed = s.used
	}
	res.Data = data
	return res, nil
}

func grantedScopes(t *oauth2.Token) []string {
	s, _ := t.Extra("scope").(string)
	if s == "" {
		return Scopes
	}
	return strings.Fields(s)
}

func missingScopes(t *oauth2.Token) []string {
	s, _ := t.Extra("scope").(string)
	if s == "" {
		return nil
	}
	got := map[string]bool{}
	for _, g := range strings.Fields(s) {
		got[g] = true
	}
	var out []string
	for _, w := range Scopes {
		if !got[w] {
			out = append(out, strings.TrimPrefix(w, "https://www.googleapis.com/auth/"))
		}
	}
	return out
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// loopbackLogin runs the installed-app flow: a one-shot server on
// 127.0.0.1:<random port> receives Google's redirect. The state must match
// (a forged or stale redirect is refused and the wait goes on), and the code
// is exchanged with the PKCE verifier only this process knows. It never reads
// stdin: it waits for the browser, at most loginWait.
func loopbackLogin(ctx context.Context, ci *clientInfo) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't open a local port for the sign-in")
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	cfg := oauthConfig(ci, redirect)
	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		ln.Close()
		return nil, apperr.Wrap(apperr.Internal, err, "no randomness for the sign-in state")
	}
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier))

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(r result) { once.Do(func() { done <- r }) }
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			// Not ours: refuse it but keep waiting, so a stray request can't end a real sign-in.
			http.Error(w, "ytx: this sign-in reply doesn't match the one ytx started. Run `ytx login` again.", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			page(w, "Sign-in was not completed ("+e+"). You can close this tab.")
			finish(result{err: apperr.New(apperr.LoginRequired, "Google sign-in was not completed (%s)", e)})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "ytx: the reply has no code", http.StatusBadRequest)
			return
		}
		page(w, "ytx is signed in. You can close this tab and go back to the terminal.")
		finish(result{code: code})
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	fmt.Fprintf(stderr, "Sign in with Google in the browser. If it didn't open, visit:\n\n  %s\n\n", authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintf(stderr, "(couldn't open the browser: %v)\n", err)
	}
	fmt.Fprintf(stderr, "Waiting up to %s for the browser sign-in to come back to %s (no terminal input needed)...\n", loginWait, redirect)

	timer := time.NewTimer(loginWait)
	defer timer.Stop()
	var code string
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		code = r.code
	case <-timer.C:
		return nil, apperr.New(apperr.Timeout, "no sign-in came back within %s; run `ytx login` again", loginWait)
	case <-ctx.Done():
		return nil, apperr.From(ctx.Err())
	}
	tok, err := cfg.Exchange(httpCtx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return nil, apperr.New(apperr.LoginRequired, "Google refused the sign-in code (%s); run `ytx login` again", re.ErrorCode)
		}
		return nil, apperr.From(err)
	}
	return tok, nil
}

func page(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>ytx</title><body style=\"font:16px system-ui;margin:3em\"><p>%s</p>", html.EscapeString(msg))
}

func defaultOpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		return errors.New("no known way to open a browser here")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// ---- logout

func logout(ctx context.Context, _ ops.Input) (*ops.Result, error) {
	tok, _ := loadToken()
	if err := store.Delete(config.SecretOAuthToken); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "couldn't remove the sign-in from the Keychain")
	}
	data := map[string]any{"signed_out": true, "client_kept": true}
	res := &ops.Result{Data: data, Meta: ops.Meta{Source: "local"}}
	if tok == nil {
		data["was_signed_in"] = false
		return res, nil
	}
	t := tok.RefreshToken
	if t == "" {
		t = tok.AccessToken
	}
	err := revoke(ctx, t)
	data["revoked"] = err == nil
	if err != nil {
		res.Warnings = append(res.Warnings, "forgot the sign-in here, but couldn't revoke it at Google ("+err.Error()+"); remove ytx at myaccount.google.com/permissions if you want")
	}
	return res, nil
}

func revoke(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := baseClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Google answered %s", resp.Status)
	}
	return nil
}
