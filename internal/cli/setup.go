package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/cache"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/quota"
)

func init() {
	ops.Handle("doctor", doctor)
	ops.Handle("quota", func(context.Context, ops.Input) (*ops.Result, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		q := quota.New(cfg.Limit())
		bucket := func(name string) map[string]int {
			b := q.Bucket(name, quota.BucketLimit)
			return map[string]int{"used": b.Used(), "left": b.Left(), "limit": b.Limit()}
		}
		return &ops.Result{Data: map[string]any{
			"used": q.Used(), "left": q.Left(), "limit": q.Limit(), "resets": q.Resets().Format(time.RFC3339),
			"search_calls": bucket(quota.BucketSearch), "upload_calls": bucket(quota.BucketUploads),
			"note": "ytx's own count; searches and uploads have their own daily buckets of 100 calls, everything else spends the unit pool; other apps on the same Google project share these",
		}, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("config.show", func(context.Context, ops.Input) (*ops.Result, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"file": config.Path("config.json"), "config": cfg, "keys": config.Keys},
			Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("config.set", func(_ context.Context, in ops.Input) (*ops.Result, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		if err := cfg.Set(in.Str("key"), in.Str("value")); err != nil {
			return nil, err
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return &ops.Result{Data: cfg, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("cache.clear", func(context.Context, ops.Input) (*ops.Result, error) {
		n, err := cache.Clear()
		if err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"removed": n}, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("key.set", func(context.Context, ops.Input) (*ops.Result, error) {
		key, err := readKey()
		if err != nil {
			return nil, err
		}
		if err := config.SetSecret(config.SecretAPIKey, key); err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"stored": "keychain", "key": mask(key)}, Meta: ops.Meta{Source: "local"}}, nil
	})
	ops.Handle("key.remove", func(context.Context, ops.Input) (*ops.Result, error) {
		if err := config.DeleteSecret(config.SecretAPIKey); err != nil {
			return nil, err
		}
		return &ops.Result{Data: map[string]any{"removed": true}, Meta: ops.Meta{Source: "local"}}, nil
	})
}

// readKey takes the key from piped stdin, or a hidden prompt on a terminal.
// It never prompts without a terminal.
func readKey() (string, error) {
	var key string
	if !isTTY(os.Stdin) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return "", err
		}
		key = string(b)
	} else {
		s, err := readSecretTTY("YouTube Data API key: ")
		if err != nil {
			return "", err
		}
		key = s
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", apperr.New(apperr.InvalidInput, "no key given: pipe it in, e.g. `printf %%s \"$KEY\" | ytx key set`")
	}
	if strings.ContainsAny(key, " \t\n") || len(key) < 20 {
		return "", apperr.New(apperr.InvalidInput, "that doesn't look like an API key")
	}
	return key, nil
}

func mask(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

type check struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func doctor(ctx context.Context, _ ops.Input) (*ops.Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	out := map[string]check{}

	// keyless: can we reach youtube.com?
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodHead, "https://www.youtube.com/", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil {
		out["keyless"] = check{false, "can't reach youtube.com: " + err.Error(), "check the network"}
	} else {
		resp.Body.Close()
		out["keyless"] = check{resp.StatusCode < 500, "youtube.com answered " + resp.Status, ""}
	}

	if key, where := config.APIKey(); key != "" {
		q := quota.New(cfg.Limit())
		out["api_key"] = check{true, "set (" + where + ", " + mask(key) + "); " + itoa(q.Left()) + " of " + itoa(q.Limit()) + " units left today", ""}
	} else {
		out["api_key"] = check{false, "not set: reads run keyless", "optional: export YOUTUBE_API_KEY=... or `printf %s \"$KEY\" | ytx key set`"}
	}

	if tok, _ := config.Secret(config.SecretOAuthToken); tok != "" {
		out["signed_in"] = check{true, "Google sign-in stored in the Keychain", ""}
	} else {
		out["signed_in"] = check{false, "not signed in: channel writes and analytics need it", "ytx login --client client_secret.json"}
	}

	out["yt-dlp"] = toolCheck(ctx, "yt-dlp", "--version", "uv tool install yt-dlp")
	out["ffmpeg"] = toolCheck(ctx, "ffmpeg", "-version", "brew install ffmpeg")

	dir := cfg.ResearchDir
	if dir == "" {
		out["research_dir"] = check{false, "not set: research notes go to the current folder", "ytx config set research_dir <folder>"}
	} else if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		out["research_dir"] = check{false, dir + " is not a folder", "ytx config set research_dir <folder>"}
	} else {
		out["research_dir"] = check{true, dir, ""}
	}
	return &ops.Result{Data: map[string]any{"version": Version, "home": config.Home(), "checks": out}, Meta: ops.Meta{Source: "local"}}, nil
}

func toolCheck(ctx context.Context, name, flag, fix string) check {
	p, err := exec.LookPath(name)
	if err != nil {
		return check{false, name + " not found", fix}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, p, flag).Output()
	if err != nil {
		return check{false, p + ": " + err.Error(), fix}
	}
	v := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
	return check{true, p + " " + v, ""}
}

func itoa(n int) string { return strconv.Itoa(n) }
