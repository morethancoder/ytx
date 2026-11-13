package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morethancoder/ytx/internal/ops"
)

type env struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	} `json:"error"`
	Meta     map[string]any `json:"meta"`
	Warnings []string       `json:"warnings"`
}

// run runs ytx with args and returns the exit code and the one envelope it printed.
func runCLI(t *testing.T, args ...string) (int, env) {
	t.Helper()
	t.Setenv("YTX_HOME", t.TempDir())
	t.Setenv("YTX_NO_KEYCHAIN", "1")
	var buf bytes.Buffer
	stdout = &buf
	t.Cleanup(func() { stdout = os.Stdout })
	code := execute(args)
	out := strings.TrimSpace(buf.String())
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("want exactly one JSON line, got:\n%s", out)
	}
	var e env
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return code, e
}

// Fake handlers for one write and one read. The cli test binary imports no area
// package, so these names are free.
var (
	fakeOnce sync.Once
	calls    []bool // DryRun of each call to the fake write
)

func fakes() {
	fakeOnce.Do(func() {
		ops.Handle("video.rate", func(_ context.Context, in ops.Input) (*ops.Result, error) {
			calls = append(calls, in.DryRun)
			if in.DryRun {
				return &ops.Result{Data: map[string]any{"action": "rate", "target": in.Str("video"), "rating": in.Str("rating")}}, nil
			}
			return &ops.Result{Data: map[string]any{"rated": true}, Meta: ops.Meta{Source: "oauth"}}, nil
		})
		ops.Handle("playlist.get", func(_ context.Context, in ops.Input) (*ops.Result, error) {
			return &ops.Result{Data: []map[string]any{{"id": "a", "title": "A", "views": 1}, {"id": "b", "title": "B", "views": 2}},
				Warnings: []string{"w1"}}, nil
		})
	})
	calls = nil
	canAsk = func() bool { return false }
}

func TestUnknownCommandGivesEnvelope(t *testing.T) {
	code, e := runCLI(t, "nope")
	if code != 2 || e.OK || e.Error.Code != "invalid_input" {
		t.Fatalf("code %d env %+v", code, e)
	}
}

func TestMissingPositionalIsInvalidInput(t *testing.T) {
	code, e := runCLI(t, "video")
	if code != 2 || e.Error.Code != "invalid_input" || !strings.Contains(e.Error.Message, "usage: ytx video <video>") {
		t.Fatalf("code %d env %+v", code, e.Error)
	}
}

func TestUnbuiltOpIsNotImplemented(t *testing.T) {
	code, e := runCLI(t, "video", "dQw4w9WgXcQ")
	if code != 1 || e.Error.Code != "not_implemented" {
		t.Fatalf("code %d env %+v", code, e.Error)
	}
}

// Without a terminal and without --yes, a write must not run and must not wait:
// it answers approval_required with the plan, after only the dry-run call.
func TestWriteWithoutTerminalNeedsApproval(t *testing.T) {
	fakes()
	code, e := runCLI(t, "rate", "abc", "like")
	if code != 6 || e.Error == nil || e.Error.Code != "approval_required" {
		t.Fatalf("code %d env %+v", code, e)
	}
	if !strings.Contains(string(e.Error.Detail), `"rating":"like"`) {
		t.Errorf("detail should be the plan, got %s", e.Error.Detail)
	}
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("handler calls (DryRun each) = %v, want [true]", calls)
	}
}

func TestDryRunPrintsPlanOnly(t *testing.T) {
	fakes()
	code, e := runCLI(t, "rate", "abc", "like", "--dry-run", "--yes")
	if code != 0 || !e.OK || !strings.Contains(string(e.Data), `"action":"rate"`) {
		t.Fatalf("code %d env %+v", code, e)
	}
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("calls %v, want only the dry run", calls)
	}
}

func TestYesRunsOnce(t *testing.T) {
	fakes()
	code, e := runCLI(t, "rate", "abc", "like", "--yes")
	if code != 0 || !e.OK || e.Meta["source"] != "oauth" {
		t.Fatalf("code %d env %+v", code, e)
	}
	if len(calls) != 1 || calls[0] {
		t.Fatalf("calls %v, want one real call", calls)
	}
}

// A person at a terminal answering no gets "declined" and nothing runs.
func TestTerminalNoIsDeclined(t *testing.T) {
	fakes()
	canAsk = func() bool { return true }
	promptIn = bufio.NewReader(strings.NewReader("n\n"))
	code, e := runCLI(t, "rate", "abc", "like")
	if code != 6 || e.Error.Code != "declined" {
		t.Fatalf("code %d env %+v", code, e)
	}
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("calls %v, want only the dry run", calls)
	}
}

func TestFieldsAndWarnings(t *testing.T) {
	fakes()
	code, e := runCLI(t, "playlist", "PLx", "--fields", "id,views")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	if string(e.Data) != `[{"id":"a","views":1},{"id":"b","views":2}]` {
		t.Errorf("fields filter: %s", e.Data)
	}
	if len(e.Warnings) != 1 {
		t.Errorf("warnings lost: %v", e.Warnings)
	}
}

func TestSchemaListsEveryOp(t *testing.T) {
	code, e := runCLI(t, "schema", "--no-skill")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var s struct {
		Ops []struct {
			Name string `json:"name"`
		} `json:"ops"`
	}
	if err := json.Unmarshal(e.Data, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Ops) != len(ops.All) {
		t.Fatalf("schema has %d ops, table has %d", len(s.Ops), len(ops.All))
	}
}

// --dry-run on a command that writes local files must refuse, not quietly do the real thing.
func TestDryRunRefusedForLocalOps(t *testing.T) {
	code, e := runCLI(t, "research", "x", "--dry-run")
	if code != 2 || e.Error == nil || e.Error.Code != "invalid_input" {
		t.Fatalf("code %d env %+v", code, e)
	}
}

// A repeatable flag keeps values with commas whole and shows up in the schema.
func TestRepeatableFlagInSchemaAndInput(t *testing.T) {
	code, e := runCLI(t, "schema", "--no-skill")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var s struct {
		Ops []struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			Params []struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Required bool   `json:"required"`
			} `json:"params"`
		} `json:"ops"`
	}
	if err := json.Unmarshal(e.Data, &s); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range s.Ops {
		if o.Name == "feedcheck" {
			found = o.Kind == "local"
			for _, p := range o.Params {
				if (p.Name == "thumb" || p.Name == "title") && (p.Type != "strings" || !p.Required) {
					found = false
				}
			}
		}
	}
	if !found {
		t.Error("schema lacks feedcheck with repeatable thumb and title")
	}
	root := NewRoot()
	c, _, err := root.Find([]string{"feedcheck"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ParseFlags([]string{"--title", "a, b", "--title", "c"}); err != nil {
		t.Fatal(err)
	}
	in, err := input(c, ops.Find("feedcheck"), []string{"topic"})
	if err == nil {
		t.Fatalf("--thumb is required, got %+v", in)
	}
	if err := c.ParseFlags([]string{"--thumb", "x.png"}); err != nil {
		t.Fatal(err)
	}
	in, err = input(c, ops.Find("feedcheck"), []string{"topic"})
	if err != nil {
		t.Fatal(err)
	}
	if got := in.Strings("title"); len(got) != 2 || got[0] != "a, b" {
		t.Errorf("titles %v", got)
	}
	if timeoutFor(ops.Find("feedcheck")) != 5*time.Minute {
		t.Error("feedcheck timeout group")
	}
}
