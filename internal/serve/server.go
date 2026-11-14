package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// envelopeBytes is room kept for the ok, cached and truncated keys added to
// every answer after it is cut to MaxBytes.
const envelopeBytes = 64

// Reader is the part of ytx's keyless reader the service uses. It leaves out
// Video, Transcript and Trending on purpose, so serve mode cannot call the
// player endpoint that YouTube refuses from servers.
type Reader interface {
	Search(ctx context.Context, o yt.SearchOpts) ([]model.Video, error)
	Channel(ctx context.Context, ref string, o yt.ChannelOpts) (*model.Channel, error)
	Comments(ctx context.Context, videoID string, o yt.CommentOpts) ([]model.Comment, error)
	Suggest(ctx context.Context, query, lang, region string) ([]string, error)
	Playlist(ctx context.Context, id string, limit int) (*model.Playlist, []model.Video, error)
}

// Server answers the JSON reads. Build it with New and use it as an http.Handler.
type Server struct {
	cfg     Config
	reader  Reader
	cache   *cache
	slots   chan struct{}
	version string
	logf    func(format string, args ...any)
	mux     *http.ServeMux
}

// New builds a Server. The config must already be valid.
func New(cfg Config, reader Reader, version string) *Server {
	s := &Server{
		cfg: cfg, reader: reader, version: version,
		cache: newCache(cfg.CacheEntries, nil),
		slots: make(chan struct{}, cfg.MaxInflight),
		logf:  log.Printf,
		mux:   http.NewServeMux(),
	}
	s.mux.HandleFunc("/health", s.health)
	s.mux.HandleFunc("/v1/search", s.read("search", s.search))
	s.mux.HandleFunc("/v1/channel", s.read("channel", s.channel))
	s.mux.HandleFunc("/v1/playlist", s.read("playlist", s.playlist))
	s.mux.HandleFunc("/v1/comments", s.read("comments", s.comments))
	s.mux.HandleFunc("/v1/suggest", s.read("suggest", s.suggest))
	return s
}

// ServeHTTP recovers a panic in any handler and answers it as "unavailable".
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logf("serve: panic in handler: %v\n%s", rec, debug.Stack())
			writeError(w, CodeUnavailable, http.StatusServiceUnavailable)
		}
	}()
	// Unknown paths, including /v1/video and /v1/transcript, are plain not_found.
	if _, pattern := s.mux.Handler(r); pattern == "" {
		writeError(w, CodeNotFound, http.StatusNotFound)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.version})
}

// call is one validated read: the cache key, how long to keep it, and the fetch.
// The fetch returns the data keys of the answer and the list it may shorten.
type call struct {
	key   string
	ttl   time.Duration
	fetch func(ctx context.Context) (data map[string]any, list listCut, err error)
}

// listCut lets the size cap shorten an answer: n is how many list items there
// are, and rebuild returns the data with only the first k of them.
type listCut struct {
	n       int
	rebuild func(k int) map[string]any
}

// result is what the cache keeps: an answer already cut to the size cap.
type result struct {
	data      map[string]any
	truncated bool
}

type parser func(q url.Values) (call, error)

// read wraps a parser into an HTTP handler: method check, validation, cache,
// coalescing, the per-request timeout, and the JSON answer.
func (s *Server) read(name string, parse parser) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, CodeBadRequest, http.StatusMethodNotAllowed)
			return
		}
		c, err := parse(r.URL.Query())
		if err != nil {
			code, status := classify(err)
			s.logf("serve: %s %s %s", name, code, time.Since(start).Round(time.Millisecond))
			writeError(w, code, status)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
		defer cancel()
		val, how, err := s.cache.do(ctx, name+"|"+c.key, c.ttl, s.cfg.ErrorTTL, func() (any, error) {
			return s.fetch(c)
		})
		if err != nil {
			code, status := classify(err)
			// The detail stays in the log: it may hold YouTube's words.
			s.logf("serve: %s %s %s (%v)", name, code, time.Since(start).Round(time.Millisecond), err)
			writeError(w, code, status)
			return
		}
		res := val.(result)
		out := make(map[string]any, len(res.data)+3)
		for k, v := range res.data {
			out[k] = v
		}
		out["ok"] = true
		out["cached"] = how == hit
		out["truncated"] = res.truncated
		s.logf("serve: %s ok %v %s", name, how, time.Since(start).Round(time.Millisecond))
		writeJSON(w, http.StatusOK, out)
	}
}

// fetch runs one read against YouTube with its own timeout (detached from any
// single caller, since others may be waiting on it) and a slot, then cuts the
// answer to the size cap.
func (s *Server) fetch(c call) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, list, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	return s.fit(data, list)
}

// fit shortens the list until the encoded answer fits MaxBytes.
func (s *Server) fit(data map[string]any, list listCut) (result, error) {
	size := func(d map[string]any) int {
		b, _ := json.Marshal(d)
		return len(b)
	}
	limit := s.cfg.MaxBytes - envelopeBytes
	if size(data) <= limit {
		return result{data: data}, nil
	}
	if list.rebuild == nil {
		return result{}, errors.New("answer over the size cap with no list to cut")
	}
	// Binary search for the most items that fit.
	lo, hi := 0, list.n
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if size(list.rebuild(mid)) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	d := list.rebuild(lo)
	if size(d) > limit {
		return result{}, errors.New("answer over the size cap even without its list")
	}
	return result{data: d, truncated: true}, nil
}

// ---- the five reads

func (s *Server) search(q url.Values) (call, error) {
	query, err := s.text(q, "q")
	if err != nil {
		return call{}, err
	}
	limit, err := s.limit(q)
	if err != nil {
		return call{}, err
	}
	order, err := oneOf(q, "order", "relevance", "relevance", "views", "date")
	if err != nil {
		return call{}, err
	}
	typ, err := oneOf(q, "type", "video", "video", "short", "any")
	if err != nil {
		return call{}, err
	}
	since, err := intIn(q, "since_days", 0, 3650)
	if err != nil {
		return call{}, err
	}
	lang, err := tag(q, "lang", langRe)
	if err != nil {
		return call{}, err
	}
	region, err := tag(q, "region", regionRe)
	if err != nil {
		return call{}, err
	}
	o := yt.SearchOpts{Query: query, Limit: limit, Order: order, Type: typ, SinceDays: since, Lang: lang, Region: region}
	return call{
		key: fmt.Sprintf("%q|%d|%s|%s|%d|%s|%s", query, limit, order, typ, since, lang, region),
		ttl: s.cfg.SearchTTL,
		fetch: func(ctx context.Context) (map[string]any, listCut, error) {
			vs, err := s.reader.Search(ctx, o)
			if err != nil {
				return nil, listCut{}, err
			}
			return listAnswer("videos", vs)
		},
	}, nil
}

func (s *Server) channel(q url.Values) (call, error) {
	raw, err := s.text(q, "ref")
	if err != nil {
		return call{}, err
	}
	ref, err := yt.ChannelRef(raw)
	if err != nil {
		return call{}, badRequest()
	}
	limit, err := s.limit(q)
	if err != nil {
		return call{}, err
	}
	shorts, err := flag(q, "shorts")
	if err != nil {
		return call{}, err
	}
	return call{
		key: fmt.Sprintf("%s|%d|%t", ref, limit, shorts),
		ttl: s.cfg.ChannelTTL,
		fetch: func(ctx context.Context) (map[string]any, listCut, error) {
			ch, err := s.reader.Channel(ctx, ref, yt.ChannelOpts{Limit: limit, Shorts: shorts})
			if err != nil {
				return nil, listCut{}, err
			}
			build := func(k int) map[string]any {
				c := *ch
				c.Recent = ch.Recent[:min(k, len(ch.Recent))]
				return map[string]any{"channel": c}
			}
			return build(len(ch.Recent)), listCut{n: len(ch.Recent), rebuild: build}, nil
		},
	}, nil
}

func (s *Server) playlist(q url.Values) (call, error) {
	raw, err := s.text(q, "id")
	if err != nil {
		return call{}, err
	}
	id, err := yt.PlaylistID(raw)
	if err != nil {
		return call{}, badRequest()
	}
	limit, err := s.limit(q)
	if err != nil {
		return call{}, err
	}
	return call{
		key: fmt.Sprintf("%s|%d", id, limit),
		ttl: s.cfg.PlaylistTTL,
		fetch: func(ctx context.Context) (map[string]any, listCut, error) {
			pl, vs, err := s.reader.Playlist(ctx, id, limit)
			if err != nil {
				return nil, listCut{}, err
			}
			build := func(k int) map[string]any {
				return map[string]any{"playlist": pl, "videos": vs[:min(k, len(vs))]}
			}
			return build(len(vs)), listCut{n: len(vs), rebuild: build}, nil
		},
	}, nil
}

func (s *Server) comments(q url.Values) (call, error) {
	raw, err := s.text(q, "video")
	if err != nil {
		return call{}, err
	}
	id, err := yt.VideoID(raw)
	if err != nil {
		return call{}, badRequest()
	}
	limit, err := s.limit(q)
	if err != nil {
		return call{}, err
	}
	order, err := oneOf(q, "order", "top", "top", "new")
	if err != nil {
		return call{}, err
	}
	return call{
		key: fmt.Sprintf("%s|%d|%s", id, limit, order),
		ttl: s.cfg.CommentsTTL,
		fetch: func(ctx context.Context) (map[string]any, listCut, error) {
			cs, err := s.reader.Comments(ctx, id, yt.CommentOpts{Limit: limit, Order: order})
			if err != nil {
				return nil, listCut{}, err
			}
			return listAnswer("comments", cs)
		},
	}, nil
}

func (s *Server) suggest(q url.Values) (call, error) {
	query, err := s.text(q, "q")
	if err != nil {
		return call{}, err
	}
	lang, err := tag(q, "lang", langRe)
	if err != nil {
		return call{}, err
	}
	region, err := tag(q, "region", regionRe)
	if err != nil {
		return call{}, err
	}
	limit, err := s.limit(q)
	if err != nil {
		return call{}, err
	}
	return call{
		key: fmt.Sprintf("%q|%s|%s|%d", query, lang, region, limit),
		ttl: s.cfg.SuggestTTL,
		fetch: func(ctx context.Context) (map[string]any, listCut, error) {
			ss, err := s.reader.Suggest(ctx, query, lang, region)
			if err != nil {
				return nil, listCut{}, err
			}
			if len(ss) > limit {
				ss = ss[:limit]
			}
			return listAnswer("suggestions", ss)
		},
	}, nil
}

// listAnswer makes {name: items} that the size cap can shorten. A nil list
// becomes an empty one so the JSON is [] and never null.
func listAnswer[T any](name string, items []T) (map[string]any, listCut, error) {
	if items == nil {
		items = []T{}
	}
	build := func(k int) map[string]any { return map[string]any{name: items[:min(k, len(items))]} }
	return build(len(items)), listCut{n: len(items), rebuild: build}, nil
}

// ---- input checks. Every failure is the plain code bad_request.

var (
	langRe   = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)
	regionRe = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// text reads a required, trimmed, bounded text parameter without control characters.
func (s *Server) text(q url.Values, name string) (string, error) {
	v := strings.TrimSpace(q.Get(name))
	if v == "" || utf8.RuneCountInString(v) > s.cfg.MaxQueryLen || !utf8.ValidString(v) {
		return "", badRequest()
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", badRequest()
		}
	}
	return v, nil
}

// limit reads the optional limit: absent means the default, above the cap is cut to the cap.
func (s *Server) limit(q url.Values) (int, error) {
	raw := q.Get("limit")
	if raw == "" {
		return s.cfg.DefaultResults, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, badRequest()
	}
	return min(n, s.cfg.MaxResults), nil
}

// oneOf reads an optional word that must be one of the allowed ones.
func oneOf(q url.Values, name, def string, allowed ...string) (string, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", badRequest()
}

func intIn(q url.Values, name string, lo, hi int) (int, error) {
	raw := q.Get(name)
	if raw == "" {
		return lo, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, badRequest()
	}
	return n, nil
}

func tag(q url.Values, name string, re *regexp.Regexp) (string, error) {
	v := q.Get(name)
	if v == "" {
		return "", nil
	}
	if !re.MatchString(v) {
		return "", badRequest()
	}
	return v, nil
}

func flag(q url.Values, name string) (bool, error) {
	switch q.Get(name) {
	case "":
		return false, nil
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	}
	return false, badRequest()
}

// ---- answers

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, CodeUnavailable, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, code string, status int) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code}})
}
