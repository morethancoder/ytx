package download

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Options is what one download asks for.
type Options struct {
	Audio  bool // false = video
	Height int  // max video height; 0 = best
}

// FormatSelector is yt-dlp's -f value. Video prefers H.264 + AAC so the merged file
// is a plain MP4; "+ba" keeps video-only formats from coming without sound.
func FormatSelector(o Options) string {
	if o.Audio {
		return "ba[ext=m4a]/ba/b"
	}
	h := ""
	if o.Height > 0 {
		h = "[height<=" + strconv.Itoa(o.Height) + "]"
	}
	tail := "/b" + h
	if h != "" {
		tail += "/b" // a pre-merged file at the height, else any pre-merged file
	}
	return "bv*" + h + "[vcodec^=avc1]+ba[acodec^=mp4a]/bv*" + h + "+ba" + tail
}

// OutputTemplate is the yt-dlp -o value: a length-capped title plus " [id]". The id
// in the name is what lets a repeat download find the file it already has.
func OutputTemplate(dir string) string {
	// The folder is literal text inside a yt-dlp template, so its % signs are escaped.
	return strings.ReplaceAll(strings.TrimRight(dir, "/"), "%", "%%") + "/%(title).150B [%(id)s].%(ext)s"
}

// DonePrefix marks the final metadata line yt-dlp prints once the file is in place.
const DonePrefix = "__ytx_done__"

// ProgressPrefix marks a progress line on stdout.
const ProgressPrefix = "__ytx_progress__"

const progressTemplate = "download:" + ProgressPrefix + "%(progress._percent_str)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.speed)s|%(progress.eta)s"

func baseArgs() []string { return []string{"--js-runtimes", "node"} }

// DownloadArgs builds the yt-dlp flags for one video.
func DownloadArgs(o Options, url, dir string) []string {
	args := append(baseArgs(),
		"--newline", "--progress", "--no-warnings", "--no-colors", "--windows-filenames", "--no-mtime",
		"--no-playlist", "--no-overwrites", "--trim-filenames", "200",
		"--progress-template", progressTemplate,
		"--print", "after_move:"+DonePrefix+`%(.{id,title,duration,height,vcodec,acodec,ext,filepath})j`, "--no-simulate",
		"-o", OutputTemplate(dir),
		"-f", FormatSelector(o))
	if !o.Audio {
		args = append(args, "--merge-output-format", "mp4", "--remux-video", "mp4")
	}
	return append(args, "--", url)
}

// ProbeArgs asks for one video's metadata without downloading.
func ProbeArgs(url string) []string {
	return append(baseArgs(), "--dump-single-json", "--skip-download", "--no-playlist", "--no-warnings", "--", url)
}

// Info is the part of yt-dlp's metadata ytx uses.
type Info struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Duration   float64 `json:"duration"`
	Height     int     `json:"height"`
	VCodec     string  `json:"vcodec"`
	ACodec     string  `json:"acodec"`
	Ext        string  `json:"ext"`
	FilePath   string  `json:"filepath"`
	LiveStatus string  `json:"live_status"`
}

// ParseInfo reads a yt-dlp info JSON object.
func ParseInfo(raw []byte) (*Info, error) {
	var in Info
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// ParseDone finds the done line in yt-dlp's stdout and parses it.
func ParseDone(stdout []byte) (*Info, bool) {
	for _, line := range strings.Split(string(stdout), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), DonePrefix); ok {
			if in, err := ParseInfo([]byte(rest)); err == nil && in.FilePath != "" {
				return in, true
			}
		}
	}
	return nil, false
}

// Progress is one tick of a running download.
type Progress struct {
	Percent    float64
	Downloaded int64
	Total      int64
}

// ParseProgress parses a stdout line that starts with ProgressPrefix.
func ParseProgress(line string) (Progress, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), ProgressPrefix)
	if !ok {
		return Progress{}, false
	}
	parts := strings.Split(rest, "|")
	if len(parts) < 3 {
		return Progress{}, false
	}
	f := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}
	return Progress{
		Percent:    f(strings.TrimSuffix(strings.TrimSpace(parts[0]), "%")),
		Downloaded: int64(f(parts[1])),
		Total:      int64(f(parts[2])),
	}, true
}
