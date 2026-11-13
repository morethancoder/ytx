package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/quota"
)

// writeCases is one valid call of every write op.
func writeCases(t *testing.T) map[string]map[string]any {
	video := tempFile(t, "clip.mp4", bytes.Repeat([]byte("v"), 4096))
	thumb := tempFile(t, "thumb.jpg", jpeg(2048))
	srt := tempFile(t, "subs.srt", []byte("1\n00:00:00,000 --> 00:00:02,000\nHello\n"))
	desc := tempFile(t, "desc.txt", []byte("Line one\nLine two\n"))
	return map[string]map[string]any{
		"video.upload": {"file": video, "title": "My upload", "description": "@" + desc, "tags": "a,b",
			"privacy": "private", "publish-at": "2030-01-02T15:00:00Z", "thumbnail": thumb, "playlist": "PLtest00000000001"},
		"video.edit":       {"video": "dQw4w9WgXcQ", "title": "New title", "privacy": "unlisted"},
		"video.thumbnail":  {"video": "dQw4w9WgXcQ", "file": thumb},
		"video.rate":       {"video": "dQw4w9WgXcQ", "rating": "like"},
		"playlist.create":  {"title": "Tutorials", "privacy": "public"},
		"playlist.add":     {"playlist": "PLtest00000000001", "video": "dQw4w9WgXcQ", "position": 0},
		"playlist.remove":  {"playlist": "PLtest00000000001", "video": "vid00000001"},
		"comment.post":     {"video": "dQw4w9WgXcQ", "text": "Thanks for watching"},
		"comment.reply":    {"comment": "UgxCommentId123", "text": "Softbox at 45 degrees"},
		"comment.moderate": {"comment": "UgxCommentId123", "status": "rejected", "ban": true},
		"captions.upload":  {"video": "dQw4w9WgXcQ", "file": srt, "lang": "en", "name": "English"},
	}
}

func TestEveryWriteOpIsCovered(t *testing.T) {
	cases := writeCases(t)
	for _, op := range ops.All {
		if op.Kind == ops.Write {
			if _, ok := cases[op.Name]; !ok {
				t.Errorf("write op %s has no dry-run case", op.Name)
			}
			if !ops.Implemented(op.Name) {
				t.Errorf("write op %s has no handler", op.Name)
			}
		}
	}
}

func TestDryRunMakesNoWrites(t *testing.T) {
	f := setup(t)
	f.signIn()
	for name, vals := range writeCases(t) {
		t.Run(name, func(t *testing.T) {
			f.reset()
			res, err := ops.HandlerFor(name)(context.Background(), input(name, true, vals))
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if w := f.writes(); len(w) > 0 {
				t.Fatalf("dry run made write calls: %v", w)
			}
			plan, ok := res.Data.(map[string]any)
			if !ok {
				t.Fatalf("plan is %T", res.Data)
			}
			for _, k := range []string{"action", "target", "changes", "quota_units"} {
				if plan[k] == nil {
					t.Errorf("plan lacks %s: %+v", k, plan)
				}
			}
			if ch, _ := plan["changes"].(map[string]any); len(ch) == 0 {
				t.Errorf("plan has no changes: %+v", plan)
			}
		})
	}
}

func TestPlanDetails(t *testing.T) {
	f := setup(t)
	f.signIn()
	cases := writeCases(t)

	res, err := upload(context.Background(), input("video.upload", true, cases["video.upload"]))
	if err != nil {
		t.Fatal(err)
	}
	q := res.Data.(map[string]any)["quota_units"].(map[string]any)
	if q["upload_calls"] != 1 || q["units"] != 100 {
		t.Errorf("upload quota plan %v, want 1 upload call and 100 units", q)
	}
	ch := res.Data.(map[string]any)["changes"].(map[string]any)
	if ch["description"] != "Line one\nLine two" || ch["privacy"] != "private" || ch["publish_at"] != "2030-01-02T15:00:00Z" {
		t.Errorf("upload changes %v", ch)
	}

	res, err = edit(context.Background(), input("video.edit", true, cases["video.edit"]))
	if err != nil {
		t.Fatal(err)
	}
	plan := res.Data.(map[string]any)
	tg := plan["target"].(map[string]any)
	if tg["title"] != "Never Gonna Give You Up" || tg["privacy"] != "public" {
		t.Errorf("edit target %v", tg)
	}
	title := plan["changes"].(map[string]any)["title"].(map[string]any)
	if title["before"] != "Never Gonna Give You Up" || title["after"] != "New title" || plan["quota_units"] != 51 {
		t.Errorf("edit plan %v", plan)
	}
}

func TestValidationBeforeAnyCall(t *testing.T) {
	f := setup(t)
	f.signIn()
	big := tempFile(t, "big.jpg", jpeg(maxThumbBytes+1))
	gif := tempFile(t, "x.gif", []byte("GIF89a......"))
	video := tempFile(t, "clip.mp4", []byte("vvvv"))
	srt := tempFile(t, "subs.txt", []byte("hello"))
	bad := []struct {
		op   string
		vals map[string]any
	}{
		{"video.upload", map[string]any{"file": video, "title": "x", "privacy": "public", "publish-at": "2030-01-02T15:00:00Z"}},
		{"video.upload", map[string]any{"file": video, "title": "x", "privacy": "secret"}},
		{"video.upload", map[string]any{"file": video, "title": strings.Repeat("a", 101)}},
		{"video.upload", map[string]any{"file": video, "title": "a <b>"}},
		{"video.upload", map[string]any{"file": video, "title": "x", "publish-at": "2001-01-01T00:00:00Z"}},
		{"video.upload", map[string]any{"file": video, "title": "x", "description": "@/no/such/file.txt"}},
		{"video.upload", map[string]any{"file": "/no/such/video.mp4", "title": "x"}},
		{"video.upload", map[string]any{"file": video, "title": "x", "thumbnail": big}},
		{"video.edit", map[string]any{"video": "dQw4w9WgXcQ"}},
		{"video.edit", map[string]any{"video": "not a video"}},
		{"video.edit", map[string]any{"video": "dQw4w9WgXcQ", "category": "music"}},
		{"video.thumbnail", map[string]any{"video": "dQw4w9WgXcQ", "file": big}},
		{"video.thumbnail", map[string]any{"video": "dQw4w9WgXcQ", "file": gif}},
		{"video.rate", map[string]any{"video": "dQw4w9WgXcQ", "rating": "love"}},
		{"playlist.add", map[string]any{"playlist": "nope", "video": "dQw4w9WgXcQ"}},
		{"playlist.add", map[string]any{"playlist": "PLtest00000000001", "video": "dQw4w9WgXcQ", "position": -2}},
		{"comment.post", map[string]any{"video": "dQw4w9WgXcQ", "text": "   "}},
		{"comment.moderate", map[string]any{"comment": "UgxCommentId123", "status": "published", "ban": true}},
		{"comment.moderate", map[string]any{"comment": "UgxCommentId123", "status": "deleted"}},
		{"captions.upload", map[string]any{"video": "dQw4w9WgXcQ", "file": srt, "lang": "en"}},
		{"me.channel", map[string]any{"days": 0}},
	}
	for i, c := range bad {
		f.reset()
		_, err := ops.HandlerFor(c.op)(context.Background(), input(c.op, true, c.vals))
		if codeOf(err) != apperr.InvalidInput {
			t.Errorf("case %d (%s %v): want invalid_input, got %v", i, c.op, c.vals, err)
		}
		if n := len(f.seen()); n > 0 {
			t.Errorf("case %d (%s): %d calls before validation failed", i, c.op, n)
		}
	}
}

func TestEditSendsBackUnchangedFields(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := edit(context.Background(), input("video.edit", false, map[string]any{"video": "dQw4w9WgXcQ", "title": "New title"}))
	if err != nil {
		t.Fatal(err)
	}
	var put *call
	for _, c := range f.seen() {
		if c.Method == http.MethodPut {
			c := c
			put = &c
		}
	}
	if put == nil || put.Query != "part=snippet" {
		t.Fatalf("want one PUT with part=snippet, got %+v", put)
	}
	var body struct {
		ID      string         `json:"id"`
		Snippet map[string]any `json:"snippet"`
		Status  map[string]any `json:"status"`
	}
	_ = json.Unmarshal(put.Body, &body)
	if body.ID != "dQw4w9WgXcQ" || body.Snippet["title"] != "New title" || body.Snippet["categoryId"] != "22" ||
		body.Snippet["description"] != "Description of Never Gonna Give You Up" || body.Status != nil {
		t.Errorf("update body %s", put.Body)
	}
	if _, ok := body.Snippet["thumbnails"]; ok {
		t.Error("read-only snippet fields were sent back")
	}
	if res.Meta.QuotaUsed != 51 || f.pool.Used() != 51 {
		t.Errorf("quota used %d (pool %d), want 51", res.Meta.QuotaUsed, f.pool.Used())
	}
}

func TestErrorMapping(t *testing.T) {
	f := setup(t)
	f.signIn()
	f.on("GET /youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write(fixture(t, "error_quota.json"))
	})
	_, err := rate(context.Background(), input("video.rate", true, map[string]any{"video": "dQw4w9WgXcQ", "rating": "like"}))
	if codeOf(err) != apperr.QuotaExceeded || f.pool.Left() != 0 {
		t.Fatalf("quotaExceeded: got %v, pool left %d", err, f.pool.Left())
	}

	f2 := setup(t)
	f2.signIn()
	f2.on("POST /youtube/v3/videos/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write(fixture(t, "error_forbidden.json"))
	})
	if _, err := rate(context.Background(), input("video.rate", false, map[string]any{"video": "dQw4w9WgXcQ", "rating": "like"})); codeOf(err) != apperr.Forbidden {
		t.Fatalf("insufficientPermissions: got %v", err)
	}

	// A GET gets one retry on 5xx, then the error.
	n := 0
	f2.on("GET /youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(503) })
	if _, err := rate(context.Background(), input("video.rate", true, map[string]any{"video": "dQw4w9WgXcQ", "rating": "like"})); codeOf(err) != apperr.YouTubeError || n != 2 {
		t.Fatalf("GET 5xx: got %v after %d tries", err, n)
	}
}

// A POST is never retried: Google may have saved it before failing, and a
// second try would post a duplicate comment.
func TestPostIsNotRetried(t *testing.T) {
	f := setup(t)
	f.signIn()
	var n atomic.Int32
	f.on("POST /youtube/v3/commentThreads", func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(503) })
	_, err := commentPost(context.Background(), input("comment.post", false, map[string]any{"video": "dQw4w9WgXcQ", "text": "hi"}))
	if n.Load() != 1 {
		t.Fatalf("POST sent %d times, want exactly 1", n.Load())
	}
	if codeOf(err) != apperr.YouTubeError || !strings.Contains(err.Error(), "check before retrying") {
		t.Fatalf("got %v", err)
	}

	// A lost connection on a POST is not retried either.
	n.Store(0)
	f.on("POST /youtube/v3/commentThreads", func(w http.ResponseWriter, r *http.Request) { n.Add(1); dropConn(t, w) })
	_, err = commentPost(context.Background(), input("comment.post", false, map[string]any{"video": "dQw4w9WgXcQ", "text": "hi"}))
	if n.Load() != 1 || codeOf(err) != apperr.NetworkError || !strings.Contains(err.Error(), "check before retrying") {
		t.Fatalf("dropped POST: %d tries, %v", n.Load(), err)
	}
}

// dropConn closes the connection without an answer, like a network failure.
func dropConn(t *testing.T, w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	c.Close()
}

func TestUploadErrorsHideSessionURL(t *testing.T) {
	for name, statusOK := range map[string]bool{"status query fails": false, "second chunk fails": true} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.signIn()
			f.on("POST /upload/youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", f.srv.URL+"/upload-session/1?uploadType=resumable&upload_id=SECRETSESSION")
				w.WriteHeader(200)
			})
			f.on("PUT /upload-session/1", func(w http.ResponseWriter, r *http.Request) {
				if statusOK && strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
					w.WriteHeader(308)
					return
				}
				dropConn(t, w)
			})
			video := tempFile(t, "clip.mp4", []byte("vvvv"))
			_, err := upload(context.Background(), input("video.upload", false, map[string]any{"file": video, "title": "x"}))
			if codeOf(err) != apperr.NetworkError {
				t.Fatalf("got %v", err)
			}
			for e := error(err); e != nil; e = errors.Unwrap(e) {
				if msg := e.Error(); strings.Contains(msg, "upload_id") || strings.Contains(msg, "SECRETSESSION") {
					t.Fatalf("error leaks the session URL: %s", msg)
				}
			}
		})
	}
}

func TestUploadQuotaExceededHitsUploadsBucket(t *testing.T) {
	f := setup(t)
	f.signIn()
	f.on("POST /upload/youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write(fixture(t, "error_quota.json"))
	})
	video := tempFile(t, "clip.mp4", []byte("vvvv"))
	_, err := upload(context.Background(), input("video.upload", false, map[string]any{"file": video, "title": "x"}))
	if codeOf(err) != apperr.QuotaExceeded {
		t.Fatalf("got %v", err)
	}
	if left := f.pool.Bucket(quota.BucketUploads, quota.BucketLimit).Left(); left != 0 {
		t.Errorf("uploads bucket left %d, want 0", left)
	}
	if f.pool.Left() != 10000 {
		t.Errorf("pool left %d, want untouched 10000", f.pool.Left())
	}
}

// fakeSession is a resumable upload session on the fake server.
type fakeSession struct {
	mu       sync.Mutex
	got      []byte
	total    int
	failOnce bool
	puts     int
	meta     map[string]any
}

func TestResumableUpload(t *testing.T) {
	f := setup(t)
	f.signIn()
	chunkSize = 256 << 10
	file := make([]byte, 700_000)
	_, _ = rand.Read(file)
	path := tempFile(t, "clip.mp4", file)
	thumb := tempFile(t, "thumb.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...))
	sess := &fakeSession{failOnce: true}

	f.on("POST /upload/youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uploadType") != "resumable" || r.Header.Get("X-Upload-Content-Length") != strconv.Itoa(len(file)) ||
			r.Header.Get("X-Upload-Content-Type") != "video/mp4" {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sess.meta)
		sess.total = len(file)
		w.Header().Set("Location", f.srv.URL+"/upload-session/1")
		w.WriteHeader(200)
	})
	f.on("PUT /upload-session/1", func(w http.ResponseWriter, r *http.Request) {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		sess.puts++
		cr := r.Header.Get("Content-Range")
		b, _ := io.ReadAll(r.Body)
		status := func() {
			if len(sess.got) > 0 {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(sess.got)-1))
			}
			w.WriteHeader(308)
		}
		if strings.HasPrefix(cr, "bytes */") {
			status()
			return
		}
		var from, to, total int
		fmt.Sscanf(cr, "bytes %d-%d/%d", &from, &to, &total)
		if from != len(sess.got) || to-from+1 != len(b) || total != sess.total {
			w.WriteHeader(400)
			return
		}
		// The second chunk is lost once, to exercise the resume.
		if from > 0 && sess.failOnce {
			sess.failOnce = false
			w.WriteHeader(503)
			return
		}
		sess.got = append(sess.got, b...)
		if len(sess.got) == sess.total {
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"newvid00001","status":{"uploadStatus":"uploaded","privacyStatus":"private"}}`)
			return
		}
		status()
	})

	res, err := upload(context.Background(), input("video.upload", false, map[string]any{
		"file": path, "title": "Resumed", "privacy": "private", "thumbnail": thumb, "playlist": "PLtest00000000001", "kids": false}))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !bytes.Equal(sess.got, file) {
		t.Fatalf("server got %d bytes, want the %d of the file", len(sess.got), len(file))
	}
	if sess.puts < 4 {
		t.Errorf("only %d PUTs: chunking or resume didn't happen", sess.puts)
	}
	st := sess.meta["status"].(map[string]any)
	if sess.meta["snippet"].(map[string]any)["title"] != "Resumed" || st["privacyStatus"] != "private" || st["selfDeclaredMadeForKids"] != false {
		t.Errorf("metadata %v", sess.meta)
	}
	d := res.Data.(map[string]any)
	if d["video"] != "newvid00001" || d["thumbnail_set"] != true || d["playlist_added"] != true || len(res.Warnings) != 0 {
		t.Errorf("result %v warnings %v", d, res.Warnings)
	}
	if used := f.pool.Bucket(quota.BucketUploads, quota.BucketLimit).Used(); used != 1 {
		t.Errorf("uploads bucket used %d, want 1", used)
	}
	if f.pool.Used() != 100 {
		t.Errorf("pool used %d, want 100 (thumbnail + playlist)", f.pool.Used())
	}
	var thumbType string
	for _, c := range f.seen() {
		if c.Path == "/upload/youtube/v3/thumbnails/set" {
			thumbType = c.CType
		}
	}
	if thumbType != "image/png" {
		t.Errorf("thumbnail sent as %q", thumbType)
	}
}

func TestCaptionsUploadMultipart(t *testing.T) {
	f := setup(t)
	f.signIn()
	srt := tempFile(t, "subs.vtt", []byte("WEBVTT\n\n00:00.000 --> 00:02.000\nHello\n"))
	res, err := captionsUpload(context.Background(), input("captions.upload", false, map[string]any{"video": "dQw4w9WgXcQ", "file": srt, "lang": "en", "draft": true}))
	if err != nil {
		t.Fatal(err)
	}
	var c call
	for _, x := range f.seen() {
		if x.Path == "/upload/youtube/v3/captions" {
			c = x
		}
	}
	if !strings.HasPrefix(c.CType, "multipart/related; boundary=") || !strings.Contains(string(c.Body), `"isDraft":true`) || !strings.Contains(string(c.Body), "WEBVTT") {
		t.Fatalf("caption request %q %s", c.CType, c.Body)
	}
	if res.Meta.QuotaUsed != 400 {
		t.Errorf("quota %d, want 400", res.Meta.QuotaUsed)
	}
}

func TestCaptionsList(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := captionsList(context.Background(), input("captions.list", false, map[string]any{"video": "dQw4w9WgXcQ"}))
	if err != nil {
		t.Fatal(err)
	}
	tracks := res.Data.(map[string]any)["tracks"].([]map[string]any)
	if len(tracks) != 2 || tracks[1]["auto_generated"] != true || res.Meta.QuotaUsed != 50 {
		t.Fatalf("tracks %v quota %d", tracks, res.Meta.QuotaUsed)
	}
}

func TestPlaylistRemoveRealRun(t *testing.T) {
	f := setup(t)
	f.signIn()
	res, err := playlistRemove(context.Background(), input("playlist.remove", false, map[string]any{"playlist": "PLtest00000000001", "video": "vid00000001"}))
	if err != nil {
		t.Fatal(err)
	}
	if w := f.writes(); len(w) != 6 || w[0] != "DELETE /youtube/v3/playlistItems" {
		// The fake lists six items for any video, so all six are removed.
		t.Fatalf("writes %v", w)
	}
	if res.Meta.QuotaUsed != 1+6*50 {
		t.Errorf("quota %d", res.Meta.QuotaUsed)
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]int{"PT3M33S": 213, "PT1H2M3S": 3723, "P1DT1S": 86401, "PT0S": 0, "": 0, "PT45S": 45} {
		if got := isoSeconds(in); got != want {
			t.Errorf("isoSeconds(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]int64{"bytes=0-999": 1000, "": 0, "bytes=0-0": 1, "junk": 0} {
		if got := nextByte(in); got != want {
			t.Errorf("nextByte(%q) = %d, want %d", in, got, want)
		}
	}
	if err := checkTags([]string{strings.Repeat("a", 499), "b"}); err == nil {
		t.Error("tags over 500 characters accepted")
	}
}
