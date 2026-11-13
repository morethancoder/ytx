package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
)

// chunkSize is how much of the file each PUT sends. Google requires a
// multiple of 256 KiB; 8 MiB keeps a lost chunk cheap to resend.
var chunkSize int64 = 8 << 20

func videoType(path string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); strings.HasPrefix(t, "video/") {
		return t
	}
	return "application/octet-stream"
}

// resumableUpload follows Google's resumable protocol: start a session with
// the metadata, then PUT the file in chunks. A 308 says how much arrived; a
// dropped connection or 5xx asks the server where it stopped and resumes once.
// The start call spends 1 from the uploads bucket (videos.insert has its own).
func (s *session) resumableUpload(ctx context.Context, path string, size int64, ctype string, meta any) (*videoItem, error) {
	body, err := json.Marshal(meta)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "encode metadata")
	}
	hdr := http.Header{
		"Content-Type":            {"application/json; charset=UTF-8"},
		"X-Upload-Content-Length": {strconv.FormatInt(size, 10)},
		"X-Upload-Content-Type":   {ctype},
	}
	u := endpointURL(uploadBase, "/videos", url.Values{"uploadType": {"resumable"}, "part": {"snippet,status"}})
	resp, _, err := s.send(ctx, request{method: http.MethodPost, url: u, header: hdr, body: body, units: 1, bucket: s.uploads})
	if err != nil {
		return nil, err
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil, apperr.New(apperr.YouTubeError, "YouTube started no upload session (no Location header)")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "can't open the video: %v", errText(err))
	}
	defer f.Close()
	prog := newProgress(size, stderrIsTTY())
	defer prog.finish()

	var off int64
	resumed := false
	stuck := 0
	for {
		n := min(chunkSize, size-off)
		start := off
		prog.set(off)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, loc, prog.reader(io.NewSectionReader(f, off, n)))
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, hideURL(err), "bad upload request")
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, size))
		resp, err := s.hc.Do(req)
		var code int
		var rb []byte
		if err == nil {
			code = resp.StatusCode
			rb, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		switch {
		case err != nil || code == http.StatusTooManyRequests || code >= 500:
			if ctx.Err() != nil {
				return nil, apperr.From(ctx.Err())
			}
			if resumed {
				if err != nil {
					return nil, apperr.Wrap(apperr.NetworkError, hideURL(err), "the upload failed twice; nothing was published, run the upload again")
				}
				return nil, s.googleError(code, rb, s.uploads, false)
			}
			resumed = true
			if werr := wait(ctx, retryWait); werr != nil {
				return nil, apperr.From(werr)
			}
			next, v, serr := s.uploadStatus(ctx, loc, size)
			if serr != nil {
				return nil, serr
			}
			if v != nil {
				return v, nil
			}
			off = next
		case code == 308:
			off = nextByte(resp.Header.Get("Range"))
			resumed = false
		case code == http.StatusOK || code == http.StatusCreated:
			prog.set(size)
			v := &videoItem{}
			if err := decode(rb, v); err != nil {
				return nil, err
			}
			if v.ID == "" {
				return nil, apperr.New(apperr.YouTubeError, "the upload finished but YouTube returned no video id")
			}
			return v, nil
		default:
			return nil, s.googleError(code, rb, s.uploads, false)
		}
		// A server that keeps accepting nothing would loop forever.
		if off <= start {
			if stuck++; stuck > 2 {
				return nil, apperr.New(apperr.YouTubeError, "the upload stopped moving at byte %d", off)
			}
		} else {
			stuck = 0
		}
	}
}

// uploadStatus asks where an interrupted upload stands: the next byte to
// send, or the finished video.
func (s *session) uploadStatus(ctx context.Context, loc string, size int64) (int64, *videoItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, loc, http.NoBody)
	if err != nil {
		return 0, nil, apperr.Wrap(apperr.Internal, hideURL(err), "bad status request")
	}
	req.ContentLength = 0
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, nil, apperr.Wrap(apperr.NetworkError, hideURL(err), "lost the upload and couldn't ask where it stopped; run the upload again")
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case 308:
		return nextByte(resp.Header.Get("Range")), nil, nil
	case http.StatusOK, http.StatusCreated:
		v := &videoItem{}
		if err := decode(rb, v); err != nil {
			return 0, nil, err
		}
		return size, v, nil
	}
	return 0, nil, s.googleError(resp.StatusCode, rb, s.uploads, false)
}

// nextByte reads a 308's "Range: bytes=0-999" (no header means nothing arrived).
func nextByte(r string) int64 {
	i := strings.LastIndex(r, "-")
	if i < 0 {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(r[i+1:]), 10, 64)
	if err != nil {
		return 0
	}
	return n + 1
}

// uploadCaption sends a caption track as multipart/related: the snippet JSON, then the file.
func (s *session) uploadCaption(ctx context.Context, meta any, file []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mj, _ := json.Marshal(meta)
	p, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	_, _ = p.Write(mj)
	p, _ = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/octet-stream"}})
	_, _ = p.Write(file)
	_ = mw.Close()
	u := endpointURL(uploadBase, "/captions", url.Values{"uploadType": {"multipart"}, "part": {"snippet"}})
	_, b, err := s.send(ctx, request{method: http.MethodPost, url: u, units: costCaptns, body: buf.Bytes(),
		header: http.Header{"Content-Type": {"multipart/related; boundary=" + mw.Boundary()}}})
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := decode(b, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// ---- progress on stderr (only on a terminal)

type progress struct {
	mu    sync.Mutex
	total int64
	done  int64
	on    bool
	last  time.Time
	shown bool
}

func newProgress(total int64, on bool) *progress { return &progress{total: total, on: on} }

func (p *progress) set(n int64) {
	p.mu.Lock()
	p.done = n
	p.mu.Unlock()
	p.draw(false)
}

func (p *progress) reader(r io.Reader) io.Reader { return &countReader{r: r, p: p} }

func (p *progress) draw(force bool) {
	if !p.on {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !force && time.Since(p.last) < 200*time.Millisecond {
		return
	}
	p.last = time.Now()
	p.shown = true
	pct := float64(p.done) * 100 / float64(max(p.total, 1))
	fmt.Fprintf(stderr, "\rUploading %5.1f%%  %.1f of %.1f MB", pct, float64(p.done)/(1<<20), float64(p.total)/(1<<20))
}

func (p *progress) finish() {
	if p.on && p.shown {
		p.draw(true)
		fmt.Fprintln(stderr)
	}
}

type countReader struct {
	r io.Reader
	p *progress
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.p.mu.Lock()
		c.p.done += int64(n)
		c.p.mu.Unlock()
		c.p.draw(false)
	}
	return n, err
}

// hideURL drops the session URL from a net/http error. The URL carries the
// upload_id, and anyone holding it can keep writing to the upload, so it must
// not reach logs or the JSON envelope. Only the method and the cause stay.
func hideURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
