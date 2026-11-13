package feedcheck

import (
	"context"
	"fmt"
	"image"
	_ "image/gif" // registers the GIF decoder for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
)

const (
	maxDownload   = 5 << 20 // a competitor thumbnail or avatar bigger than this is refused
	maxYourThumb  = 2 << 20 // YouTube's own limit for an uploaded thumbnail
	downloadLimit = 4       // parallel downloads and lookups; YouTube's CDN is fine with a few
)

// httpClient does every image download. It is a var so tests can swap it.
// It stops after 5 redirects so a bad URL can't bounce a download around.
var httpClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("stopped after 5 redirects")
	}
	return nil
}}

// checkThumb reads your thumbnail's header. A file that is not a readable
// JPG, PNG or GIF is invalid_input; size and shape problems are warnings.
func checkThumb(path string) (ext string, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, apperr.New(apperr.InvalidInput, "can't read thumbnail %s: %v", path, err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return "", nil, apperr.New(apperr.InvalidInput, "thumbnail %s is not a readable JPG, PNG or GIF", path)
	}
	// The extension comes from the content, so a file named without one (or wrongly) still gets the right one.
	ext = formatExt[format]
	name := filepath.Base(path)
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return "", nil, apperr.New(apperr.InvalidInput, "thumbnail %s has no size", path)
	}
	ratio := float64(cfg.Width) / float64(cfg.Height)
	if d := ratio/(16.0/9.0) - 1; d > 0.02 || d < -0.02 {
		warnings = append(warnings, fmt.Sprintf("thumbnail %s is %dx%d, not 16:9; YouTube crops or pads it", name, cfg.Width, cfg.Height))
	}
	if cfg.Width < 1280 || cfg.Height < 720 {
		warnings = append(warnings, fmt.Sprintf("thumbnail %s is %dx%d, smaller than 1280x720", name, cfg.Width, cfg.Height))
	}
	if st, err := f.Stat(); err == nil && st.Size() > maxYourThumb {
		warnings = append(warnings, fmt.Sprintf("thumbnail %s is %.1f MB; YouTube accepts up to 2 MB", name, float64(st.Size())/(1<<20)))
	}
	return ext, warnings, nil
}

// checkAvatar makes sure a local avatar is a readable JPG, PNG or GIF and returns its extension.
func checkAvatar(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", apperr.New(apperr.InvalidInput, "can't read avatar %s: %v", path, err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", apperr.New(apperr.InvalidInput, "avatar %s is not a readable JPG, PNG or GIF", path)
	}
	return formatExt[format], nil
}

var formatExt = map[string]string{"jpeg": ".jpg", "png": ".png", "gif": ".gif"}

var imageExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
}

// safeName keeps ids usable as file names.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "img"
	}
	return b.String()
}

// download saves an image into dir as base plus an extension from its content
// type, and returns the path. It refuses anything that is not a JPG, PNG, WebP
// or GIF, and anything over 5 MB.
func download(ctx context.Context, rawURL, dir, base string) (string, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("not an http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxDownload {
		return "", fmt.Errorf("bigger than %d MB", maxDownload>>20)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("content type %q is not an image", ct)
	}
	ext, ok := imageExt[ct]
	if !ok {
		return "", fmt.Errorf("image type %q is not supported", ct)
	}
	path := filepath.Join(dir, base+ext)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// forEach runs f for 0..n-1, at most downloadLimit at a time.
func forEach(n int, f func(i int)) {
	sem := make(chan struct{}, downloadLimit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			f(i)
		}(i)
	}
	wg.Wait()
}

// copyFile copies src into dir/base+ext and returns the new path.
func copyFile(src, dir, base, ext string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	dst := filepath.Join(dir, base+ext)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
}
