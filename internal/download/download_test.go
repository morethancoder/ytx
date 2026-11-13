package download

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/ops"
)

const zooID = "jNQXAC9IVRw"

func probeJSON(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/download/probe_video.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// fakeDownload mimics yt-dlp: it writes the file the -o template names and prints the done line.
func fakeDownload(t *testing.T, calls *[][]string) Runner {
	return func(ctx context.Context, bin string, args []string, onLine func(bool, string)) ([]byte, string, error) {
		*calls = append(*calls, args)
		if contains(args, "--dump-single-json") {
			return probeJSON(t), "", nil
		}
		dir := filepath.Dir(argAfter(args, "-o"))
		ext, codec := "mp4", `"acodec":"mp4a.40.2","height":240`
		if !contains(args, "--merge-output-format") {
			ext, codec = "m4a", `"acodec":"mp4a.40.2"`
		}
		path := filepath.Join(dir, "Me at the zoo ["+zooID+"]."+ext)
		if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
		if onLine != nil {
			onLine(false, ProgressPrefix+" 42.0%|2|5|1|1")
		}
		out := DonePrefix + `{"id":"` + zooID + `","title":"Me at the zoo","duration":19,` + codec + `,"filepath":"` + path + `"}` + "\n"
		return []byte(out), "", nil
	}
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func okTool(r Runner) *Tool { return &Tool{Path: "/fake/yt-dlp", FFmpeg: "/fake/ffmpeg", Run: r} }

func TestFormatSelector(t *testing.T) {
	cases := []struct {
		o    Options
		want string
	}{
		{Options{}, "bv*[vcodec^=avc1]+ba[acodec^=mp4a]/bv*+ba/b"},
		{Options{Height: 360}, "bv*[height<=360][vcodec^=avc1]+ba[acodec^=mp4a]/bv*[height<=360]+ba/b[height<=360]/b"},
		{Options{Audio: true}, "ba[ext=m4a]/ba/b"},
	}
	for _, c := range cases {
		if got := FormatSelector(c.o); got != c.want {
			t.Errorf("%+v\n got %s\nwant %s", c.o, got, c.want)
		}
	}
}

func TestOutputTemplateIsSafe(t *testing.T) {
	got := OutputTemplate("/x/")
	if got != "/x/%(title).150B [%(id)s].%(ext)s" {
		t.Fatal(got)
	}
	a := strings.Join(DownloadArgs(Options{}, "u", "/x"), " ")
	for _, want := range []string{"--no-overwrites", "--windows-filenames", "--no-playlist", "--merge-output-format mp4", "-- u"} {
		if !strings.Contains(a, want) {
			t.Errorf("args lack %q: %s", want, a)
		}
	}
	if strings.Contains(strings.Join(DownloadArgs(Options{Audio: true}, "u", "/x"), " "), "--merge-output-format") {
		t.Error("audio must not merge")
	}
}

func TestParseInfoFixture(t *testing.T) {
	in, err := ParseInfo(probeJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != zooID || in.Title != "Me at the zoo" || in.Duration != 19 {
		t.Fatalf("%+v", in)
	}
}

func TestParseProgressAndDone(t *testing.T) {
	p, ok := ParseProgress(ProgressPrefix + " 12.5%|100|800|50|3")
	if !ok || p.Percent != 12.5 || p.Downloaded != 100 || p.Total != 800 {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := ParseProgress("hello"); ok {
		t.Fatal("plain line parsed as progress")
	}
	if _, ok := ParseDone([]byte("noise\n")); ok {
		t.Fatal("no done line expected")
	}
}

func TestClassify(t *testing.T) {
	bg := context.Background()
	cases := []struct {
		stderr, code string
	}{
		{"ERROR: [youtube] x: Sign in to confirm your age", apperr.Forbidden},
		{"ERROR: Private video. Sign in if you've been granted access", apperr.Forbidden},
		{"ERROR: [youtube] x: Video unavailable", apperr.NotFound},
		{"ERROR: This video has been removed by the uploader", apperr.NotFound},
		{"ERROR: Unable to download webpage: getaddrinfo failed", apperr.NetworkError},
		{"ERROR: something odd", apperr.YouTubeError},
		{"ERROR: This live event will begin in 2 hours", apperr.YouTubeError},
	}
	for _, c := range cases {
		got := apperr.From(Classify(bg, errors.New("exit 1"), c.stderr))
		if got.Code != c.code {
			t.Errorf("%q: code %s, want %s", c.stderr, got.Code, c.code)
		}
	}
	if got := apperr.From(Classify(bg, &exec.Error{Err: exec.ErrNotFound}, "")); got.Code != apperr.ToolMissing {
		t.Errorf("not found: %s", got.Code)
	}
	tctx, cancel := context.WithTimeout(bg, time.Nanosecond)
	defer cancel()
	<-tctx.Done()
	if got := apperr.From(Classify(tctx, errors.New("killed"), "")); got.Code != apperr.Timeout {
		t.Errorf("deadline: %s", got.Code)
	}
}

func TestToolMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	old := extraBinDirs
	extraBinDirs = nil
	defer func() { extraBinDirs = old }()
	_, err := (&Tool{}).Download(context.Background(), zooID, Options{}, t.TempDir())
	e := apperr.From(err)
	if e.Code != apperr.ToolMissing || !strings.Contains(e.Message, `uv tool install "yt-dlp[default]"`) {
		t.Fatalf("%v", e)
	}
	// ffmpeg missing only matters for video.
	tool := &Tool{Path: "/fake/yt-dlp"}
	if e := apperr.From(func() error { _, err := tool.Download(context.Background(), zooID, Options{}, t.TempDir()); return err }()); e.Code != apperr.ToolMissing || !strings.Contains(e.Message, "ffmpeg") {
		t.Fatalf("%v", e)
	}
}

func TestDownloadVideoAndAudio(t *testing.T) {
	var calls [][]string
	tool := okTool(fakeDownload(t, &calls))
	dir := filepath.Join(t.TempDir(), "new", "sub") // created when missing

	res, err := tool.Download(context.Background(), zooID, Options{Height: 360}, dir)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(Data)
	if d.Format != "video" || d.Height != 240 || d.Bytes != 5 || d.DurationS != 19 || d.Title != "Me at the zoo" || d.AudioCodec != "" {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(argAfter(calls[0], "-f"), "height<=360") {
		t.Fatalf("height not in selector: %v", calls[0])
	}

	res, err = tool.Download(context.Background(), zooID, Options{Audio: true}, dir)
	if err != nil {
		t.Fatal(err)
	}
	d = res.Data.(Data)
	if d.Format != "audio" || d.AudioCodec != "mp4a.40.2" || d.Height != 0 || !strings.HasSuffix(d.Path, ".m4a") {
		t.Fatalf("%+v", d)
	}
}

func TestExistingFileIsKept(t *testing.T) {
	var calls [][]string
	tool := okTool(fakeDownload(t, &calls))
	dir := t.TempDir()
	path := filepath.Join(dir, "Me at the zoo ["+zooID+"].mp4")
	os.WriteFile(path, []byte("abc"), 0o644)
	// yt-dlp's temporary parts must not count as a finished file.
	os.WriteFile(filepath.Join(dir, "x ["+zooID+"].f137.mp4"), []byte("z"), 0o644)

	res, err := tool.Download(context.Background(), zooID, Options{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(Data)
	if d.Path != path || d.Bytes != 3 || d.Title != "Me at the zoo" || d.DurationS != 19 {
		t.Fatalf("%+v", d)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "already downloaded") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	for _, c := range calls {
		if !contains(c, "--dump-single-json") {
			t.Fatalf("a download ran for an existing file: %v", c)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "abc" {
		t.Fatal("existing file was changed")
	}
}

func TestOnlyTempFilesMeansDownload(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x ["+zooID+"].mp4.part"), nil, 0o644)
	if p := existing(dir, zooID, false); p != "" {
		t.Fatalf("part file taken as done: %s", p)
	}
}

func TestHandlerRejectsBadInput(t *testing.T) {
	op := ops.Find("download")
	for name, vals := range map[string]map[string]any{
		"bad id":     {"video": "nope"},
		"bad format": {"video": zooID, "format": "gif"},
		"neg height": {"video": zooID, "height": -3},
	} {
		_, err := ops.HandlerFor("download")(context.Background(), ops.Input{Op: op, Values: vals})
		if e := apperr.From(err); e == nil || e.Code != apperr.InvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCancelStopsTheRun(t *testing.T) {
	started := make(chan struct{})
	tool := okTool(func(ctx context.Context, _ string, _ []string, _ func(bool, string)) ([]byte, string, error) {
		close(started)
		<-ctx.Done()
		return nil, "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := tool.Download(ctx, zooID, Options{}, t.TempDir()); errc <- err }()
	<-started
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download did not stop on cancel")
	}
}

func TestProbeTimesOut(t *testing.T) {
	tool := okTool(func(ctx context.Context, _ string, _ []string, _ func(bool, string)) ([]byte, string, error) {
		<-ctx.Done()
		return nil, "", ctx.Err()
	})
	// The caller's context expiring is a timeout too; the probe's own 45s limit is a const.
	pctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := tool.probe(pctx, "/fake", "u")
	if err == nil {
		t.Fatal("want an error")
	}
	if e := apperr.From(err); e.Code != apperr.Timeout {
		t.Fatalf("code %s", e.Code)
	}
}

func TestProgressOnlyOnTerminal(t *testing.T) {
	var calls [][]string
	tool := okTool(fakeDownload(t, &calls))
	var buf bytes.Buffer
	oldOut, oldTTY := progressOut, stderrIsTerminal
	defer func() { progressOut, stderrIsTerminal = oldOut, oldTTY }()
	progressOut = &buf

	stderrIsTerminal = func() bool { return false }
	if _, err := tool.Download(context.Background(), zooID, Options{}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("progress written without a terminal: %q", buf.String())
	}
	stderrIsTerminal = func() bool { return true }
	if _, err := tool.Download(context.Background(), zooID, Options{}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "42.0%") {
		t.Fatalf("no progress on a terminal: %q", buf.String())
	}
}

func TestExecRunStreamsAndKills(t *testing.T) {
	var lines []string
	out, _, err := execRun(context.Background(), "/bin/sh", []string{"-c", "echo a; echo b >&2"}, func(isErr bool, l string) { lines = append(lines, l) })
	if err != nil || string(out) != "a\n" || len(lines) != 2 {
		t.Fatalf("%q %v %v", out, lines, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = execRun(ctx, "/bin/sh", []string{"-c", "sleep 30 & sleep 30"}, nil)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("child not killed: %v after %s", err, time.Since(start))
	}
}

func TestOutputTemplateEscapesPercentInFolder(t *testing.T) {
	got := OutputTemplate("/v/100%(id)s/")
	want := "/v/100%%(id)s/%(title).150B [%(id)s].%(ext)s"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
