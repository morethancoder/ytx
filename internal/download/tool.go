package download

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// InstallHint is what a person should run when yt-dlp is missing.
const InstallHint = `uv tool install "yt-dlp[default]"`

// FFmpegHint is the install hint for the merge step.
const FFmpegHint = "brew install ffmpeg"

// ProbeTimeout is the hard limit for one yt-dlp info call. Downloads have none.
const ProbeTimeout = 45 * time.Second

// Runner runs a binary, calling onLine for every stdout and stderr line as it
// arrives (nil is allowed), and returns all of stdout, the stderr text and the
// run error. Tests swap it so no network or yt-dlp is touched.
type Runner func(ctx context.Context, bin string, args []string, onLine func(stderr bool, line string)) (stdout []byte, stderr string, err error)

// Tool is the yt-dlp installation. Zero values mean the real thing.
type Tool struct {
	Path   string // yt-dlp binary; empty = DefaultPath()
	FFmpeg string // ffmpeg binary; empty = FFmpegPath()
	Run    Runner // nil = run the real process
}

// DefaultPath is ~/.local/bin/yt-dlp (the uv tool install), else yt-dlp from PATH, else "".
func DefaultPath() string { return findBin("yt-dlp") }

// extraBinDirs are searched after PATH; a variable so tests can empty it.
var extraBinDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// FFmpegPath finds ffmpeg the same way, then in Homebrew's folders (a GUI-less
// shell may lack them on PATH).
func FFmpegPath() string {
	if p := findBin("ffmpeg"); p != "" {
		return p
	}
	for _, d := range extraBinDirs {
		if p := filepath.Join(d, "ffmpeg"); fileExists(p) {
			return p
		}
	}
	return ""
}

func findBin(name string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".local", "bin", name); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (t *Tool) bin() string {
	if t.Path != "" {
		return t.Path
	}
	return DefaultPath()
}

func (t *Tool) hasFFmpeg() bool {
	if t.FFmpeg != "" {
		return true
	}
	return FFmpegPath() != ""
}

func (t *Tool) run(ctx context.Context, bin string, args []string, onLine func(bool, string)) ([]byte, string, error) {
	if t.Run != nil {
		return t.Run(ctx, bin, args, onLine)
	}
	return execRun(ctx, bin, args, onLine)
}

// execRun runs the process in its own process group so that cancelling ctx
// stops yt-dlp and the ffmpeg it started, not just the parent.
func execRun(ctx context.Context, bin string, args []string, onLine func(bool, string)) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	so, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	se, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	var (
		wg        sync.WaitGroup
		out, errb []byte
		// lineMu serializes onLine: stdout and stderr are pumped in parallel,
		// and callers should not need their own lock.
		lineMu sync.Mutex
	)
	pump := func(r interface{ Read([]byte) (int, error) }, isErr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if isErr {
				errb = append(errb, line...)
				errb = append(errb, '\n')
			} else {
				out = append(out, line...)
				out = append(out, '\n')
			}
			if onLine != nil {
				lineMu.Lock()
				onLine(isErr, line)
				lineMu.Unlock()
			}
		}
	}
	wg.Add(2)
	go pump(so, false)
	go pump(se, true)
	wg.Wait()
	err = cmd.Wait()
	return out, string(errb), err
}
