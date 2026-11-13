package feedcheck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/morethancoder/ytx/internal/channel"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/quota"
)

// dataBase is the Data API base; a var so tests can point it at a server.
var dataBase = "https://www.googleapis.com/youtube/v3"

// mine is the signed-in channel.
type mine struct{ ID, Name, Avatar string }

// signedIn is a var so tests can swap it.
var signedIn = signedInChannel

// signedInChannel reads your channel when a sign-in is stored. It never starts a
// login or a browser, and it stays silent about any failure: feedcheck then
// uses --channel and --avatar or "Your channel".
func signedInChannel(ctx context.Context) *mine {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hc, err := channel.HTTPClient(ctx)
	if err != nil || hc == nil {
		return nil
	}
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	if quota.New(cfg.Limit()).Spend(1) != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dataBase+"/channels?part=snippet&mine=true", nil)
	if err != nil {
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	var r struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				Thumbnails map[string]struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Items) == 0 {
		return nil
	}
	it := r.Items[0]
	m := &mine{ID: it.ID, Name: it.Snippet.Title}
	for _, k := range []string{"high", "medium", "default"} {
		if u := it.Snippet.Thumbnails[k].URL; u != "" {
			m.Avatar = u
			break
		}
	}
	return m
}
