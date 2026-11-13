package download

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/yt"
)

func init() { ops.Handle("download", handle) }

// stderrIsTerminal says whether progress may be drawn; a variable so tests can flip it.
var stderrIsTerminal = func() bool {
	st, err := os.Stderr.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

var progressOut io.Writer = os.Stderr

// defaultTool is what the handler uses; tests replace it.
var defaultTool = &Tool{}

// Data is the result of a download.
type Data struct {
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Title      string  `json:"title"`
	Format     string  `json:"format"` // video or audio
	Height     int     `json:"height,omitempty"`
	AudioCodec string  `json:"audio_codec,omitempty"`
	Bytes      int64   `json:"bytes"`
	DurationS  float64 `json:"duration_s"`
}

func handle(ctx context.Context, in ops.Input) (*ops.Result, error) {
	id, err := yt.VideoID(in.Str("video"))
	if err != nil {
		return nil, err
	}
	o := Options{}
	switch f := in.Str("format"); f {
	case "", "video":
	case "audio":
		o.Audio = true
	default:
		return nil, apperr.New(apperr.InvalidInput, "--format must be video or audio, not %q", f)
	}
	if h := in.Int("height", 0); h < 0 {
		return nil, apperr.New(apperr.InvalidInput, "--height must be positive")
	} else if !o.Audio {
		o.Height = h
	}
	dir := in.Str("out")
	if dir == "" {
		dir = "."
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "bad --out folder")
	}
	return defaultTool.Download(ctx, id, o, dir)
}

// Download fetches one video (or its audio) into dir. A file that is already
// there is returned with a warning instead of being fetched or overwritten.
func (t *Tool) Download(ctx context.Context, id string, o Options, dir string) (*ops.Result, error) {
	bin := t.bin()
	if bin == "" {
		return nil, toolMissing("yt-dlp", InstallHint)
	}
	if !o.Audio && !t.hasFFmpeg() {
		return nil, toolMissing("ffmpeg", FFmpegHint)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "cannot create %s", dir)
	}
	url := yt.VideoURL(id, false)
	format := "video"
	if o.Audio {
		format = "audio"
	}
	meta := ops.Meta{Source: "yt-dlp"}

	if path := existing(dir, id, o.Audio); path != "" {
		d := Data{ID: id, Path: path, Format: format, Title: titleFromName(path, id)}
		if st, err := os.Stat(path); err == nil {
			d.Bytes = st.Size()
		}
		// Title and length come from a probe; the file is still returned if it fails.
		if info, perr := t.probe(ctx, bin, url); perr == nil {
			d.Title, d.DurationS = info.Title, info.Duration
		} else if ctx.Err() != nil {
			return nil, perr
		}
		return &ops.Result{Data: d, Meta: meta,
			Warnings: []string{"already downloaded, kept the existing file: " + path}}, nil
	}

	show := stderrIsTerminal()
	onLine := func(isErr bool, line string) {
		if isErr || !show {
			return
		}
		if p, ok := ParseProgress(line); ok {
			fmt.Fprintf(progressOut, "\rdownloading %5.1f%%", p.Percent)
		}
	}
	stdout, stderr, err := t.run(ctx, bin, DownloadArgs(o, url, dir), onLine)
	if show {
		fmt.Fprint(progressOut, "\r\033[K")
	}
	if err != nil {
		return nil, Classify(ctx, err, stderr)
	}
	info, ok := ParseDone(stdout)
	if !ok {
		// --no-overwrites prints nothing when the file appeared meanwhile.
		if path := existing(dir, id, o.Audio); path != "" {
			return &ops.Result{Data: Data{ID: id, Path: path, Format: format, Title: titleFromName(path, id)}, Meta: meta,
				Warnings: []string{"already downloaded, kept the existing file: " + path}}, nil
		}
		return nil, apperr.New(apperr.YouTubeError, "yt-dlp finished but reported no file: %s", stderrTail(stderr, 300))
	}
	d := Data{ID: id, Path: info.FilePath, Title: info.Title, Format: format, DurationS: info.Duration}
	if o.Audio {
		d.AudioCodec = info.ACodec
	} else {
		d.Height = info.Height
	}
	if st, err := os.Stat(info.FilePath); err == nil {
		d.Bytes = st.Size()
	}
	return &ops.Result{Data: d, Meta: meta}, nil
}

// probe asks yt-dlp for one video's metadata, with ProbeTimeout.
func (t *Tool) probe(ctx context.Context, bin, url string) (*Info, error) {
	cctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	out, stderr, err := t.run(cctx, bin, ProbeArgs(url), nil)
	if err != nil {
		if ctx.Err() == nil && cctx.Err() != nil {
			return nil, apperr.New(apperr.Timeout, "yt-dlp took too long to answer")
		}
		return nil, Classify(ctx, err, stderr)
	}
	info, perr := ParseInfo(out)
	if perr != nil {
		return nil, apperr.Wrap(apperr.YouTubeError, perr, "yt-dlp returned unreadable info")
	}
	return info, nil
}

var (
	videoExts = map[string]bool{"mp4": true, "mkv": true, "webm": true}
	audioExts = map[string]bool{"m4a": true, "mp3": true, "opus": true, "ogg": true}
)

// existing finds a finished file for this id in dir. The name ends in "[id].ext", and
// anything with a further dot (".f137.mp4", ".part") is yt-dlp's temporary file.
func existing(dir, id string, audio bool) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	exts := videoExts
	if audio {
		exts = audioExts
	}
	marker := "[" + id + "]."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		i := strings.LastIndex(e.Name(), marker)
		if i < 0 {
			continue
		}
		ext := e.Name()[i+len(marker):]
		if !exts[ext] {
			continue
		}
		return filepath.Join(dir, e.Name())
	}
	return ""
}

func titleFromName(path, id string) string {
	n := filepath.Base(path)
	if i := strings.LastIndex(n, " ["+id+"]."); i >= 0 {
		return n[:i]
	}
	return strings.TrimSuffix(n, filepath.Ext(n))
}
