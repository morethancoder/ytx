package feedcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/morethancoder/ytx/internal/apperr"
)

const template = "y4-feed-preview"

// agdPath is the agd binary: $YTX_AGD (tests), else agd on PATH, else "".
func agdPath() string {
	if p := os.Getenv("YTX_AGD"); p != "" {
		return p
	}
	if p, err := exec.LookPath("agd"); err == nil {
		return p
	}
	return ""
}

// design is one agd document to build: its input file, its design file and the PNG it should end as.
type design struct {
	label string // "v1" or "v1-blind", used in messages
	input string // file names, relative to the folder
	agd   string
	png   string
}

// commands are the agd calls for one design, as argument lists (agd itself not included).
func (d design) commands(theme string) [][]string {
	cmds := [][]string{{"new", template, "-i", d.input, "-o", d.agd, "--json"}}
	if theme != "" && theme != "light" {
		cmds = append(cmds, []string{"theme", d.agd, theme, "--json"})
	}
	cmds = append(cmds,
		[]string{"render", d.agd, "--no-sheet", "-o", d.label + ".out", "--json"},
		[]string{"validate", d.agd, "--json"})
	return cmds
}

// manual is what to run by hand when agd is not installed.
func manual(folder string, ds []design, theme string) string {
	var parts []string
	for _, d := range ds {
		for _, c := range d.commands(theme) {
			parts = append(parts, "agd "+strings.Join(c, " "))
		}
	}
	return "agd was not found (install it or set YTX_AGD), so the sheets are not rendered; inputs are written. Render with: cd " + folder + " && " + strings.Join(parts, " && ")
}

func runAgd(ctx context.Context, bin, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.WaitDelay = time.Second // a killed agd may leave a child holding the pipes
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return out.Bytes(), ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return out.Bytes(), fmt.Errorf("%v: %s", err, msg)
	}
	return out.Bytes(), nil
}

// build runs agd for one design: new, theme, render, validate. A failing new,
// theme or render is an error; validate errors come back as warnings.
func build(ctx context.Context, bin, folder, theme string, d design) (warnings []string, err error) {
	fail := func(step string, e error) error {
		if ctx.Err() != nil {
			return ctxError(ctx, "agd "+step+" for "+d.label)
		}
		return apperr.Wrap(apperr.Internal, e, "agd %s failed for %s (files are in %s)", step, d.label, folder)
	}
	for _, c := range d.commands(theme) {
		out, e := runAgd(ctx, bin, folder, c)
		switch c[0] {
		case "validate":
			warnings = append(warnings, validateWarnings(d.label, out)...)
			if e != nil && len(out) == 0 {
				warnings = append(warnings, fmt.Sprintf("sheet %s: validate did not run: %v", d.label, e))
			}
		case "render":
			if e != nil {
				return warnings, fail("render", e)
			}
			if e := collectPNG(folder, d, out); e != nil {
				return warnings, fail("render", e)
			}
		default:
			if e != nil {
				return warnings, fail(c[0], e)
			}
		}
	}
	return warnings, nil
}

// validateWarnings turns agd validate's error issues into warnings naming the sheet.
func validateWarnings(label string, out []byte) []string {
	var v struct {
		Issues []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Layer    string `json:"layer"`
			Message  string `json:"message"`
		} `json:"issues"`
	}
	if json.Unmarshal(out, &v) != nil {
		return nil
	}
	var w []string
	for _, i := range v.Issues {
		if i.Severity == "error" {
			w = append(w, fmt.Sprintf("sheet %s: agd validate: %s %s: %s", label, i.Code, i.Layer, i.Message))
		}
	}
	return w
}

// collectPNG moves the rendered slide to its neutral name and removes agd's
// output folder. agd names the slide itself ("01-s1.png"); its JSON says where.
func collectPNG(folder string, d design, out []byte) error {
	var r struct {
		Result struct {
			Slides []struct {
				File string `json:"file"`
			} `json:"slides"`
		} `json:"result"`
	}
	src := ""
	if json.Unmarshal(out, &r) == nil && len(r.Result.Slides) > 0 {
		src = r.Result.Slides[0].File
	}
	if src == "" {
		m, _ := filepath.Glob(filepath.Join(folder, d.label+".out", "*.png"))
		if len(m) == 0 {
			return fmt.Errorf("agd wrote no PNG")
		}
		src = m[0]
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(folder, src)
	}
	if err := os.Rename(src, filepath.Join(folder, d.png)); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(folder, d.label+".out"))
	return nil
}

// ctxError is the timeout error for a cancelled or expired context, so a slow
// run is not reported as an internal or network fault.
func ctxError(ctx context.Context, what string) error {
	return apperr.Wrap(apperr.Timeout, ctx.Err(), "%s was stopped", what)
}
