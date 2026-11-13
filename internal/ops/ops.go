// Package ops is the one table of everything ytx can do. The CLI builds its
// commands from it, `ytx schema` prints it, and an agent (or an Arshafa CLI
// plugin) reads it to know each command's arguments and whether it writes.
//
// Each area of ytx (reads, research, channel writes, analytics, download)
// lives in its own package and attaches its handlers with Handle() in an
// init(); cmd/ytx imports those packages for that side effect. An op with no
// handler answers not_implemented.
package ops

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/morethancoder/ytx/internal/apperr"
)

// Kind says whether an op only reads or changes something on YouTube or disk.
type Kind string

const (
	Read  Kind = "read"
	Write Kind = "write" // changes YouTube: asks for approval unless --yes
	Local Kind = "local" // changes only this machine (settings, cache, files): never asks
)

// Auth is what an op needs to run.
type Auth string

const (
	AuthNone  Auth = "none"  // works keyless; uses the API key when there is one
	AuthKey   Auth = "key"   // needs YOUTUBE_API_KEY
	AuthLogin Auth = "login" // needs `ytx login` (OAuth, your channel)
	AuthLocal Auth = "local" // touches only this machine
)

// Param types.
const (
	TString = "string"
	TInt    = "int"
	TBool   = "bool"
	TFile   = "file" // a local path
	TList   = "list" // comma-separated
	// TStrings is a flag that repeats (--title a --title b); values may contain commas.
	TStrings = "strings"
)

// Param is one argument of an op.
type Param struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Required   bool   `json:"required,omitempty"`
	Positional bool   `json:"positional,omitempty"`
	Help       string `json:"help"`
	Default    any    `json:"default,omitempty"`
}

// Op is one ytx operation.
type Op struct {
	Name    string  `json:"name"`    // dotted id, e.g. "video.get"
	Command string  `json:"command"` // CLI words, e.g. "video"
	Short   string  `json:"summary"`
	Kind    Kind    `json:"kind"`
	Auth    Auth    `json:"auth"`
	Params  []Param `json:"params"`
	// Quota is the official API's cost in units when it runs on the API (0 = free or keyless only).
	Quota int `json:"quota_units,omitempty"`
	// Cache is how long a read's result is reused ("" = never cached).
	Cache string `json:"cache,omitempty"`
}

// Input is the parsed arguments of one call.
type Input struct {
	Op     *Op
	Values map[string]any
	// Given marks the arguments the caller actually passed (Values also holds defaults).
	Given map[string]bool
	// Source is the reader the caller asked for: "auto" (default), "keyless" or "api".
	Source string
	// DryRun asks a write handler to return its plan (what it would do) without doing it.
	DryRun  bool
	NoCache bool
}

func (in Input) has(name string) bool { _, ok := in.Values[name]; return ok }

// Has reports whether the argument was given.
func (in Input) Has(name string) bool { return in.has(name) }

// Str returns a string argument ("" when absent).
func (in Input) Str(name string) string {
	switch v := in.Values[name].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// Int returns an int argument, or def when absent or unreadable.
func (in Input) Int(name string, def int) int {
	switch v := in.Values[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Bool returns a bool argument.
func (in Input) Bool(name string) bool {
	switch v := in.Values[name].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	}
	return false
}

// List returns a comma-separated argument as trimmed, non-empty items.
func (in Input) List(name string) []string {
	var out []string
	for _, s := range strings.Split(in.Str(name), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Strings returns a repeatable argument as its values in the order given.
func (in Input) Strings(name string) []string {
	switch v := in.Values[name].(type) {
	case []string:
		return append([]string(nil), v...)
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}

// Meta travels next to the data in the envelope.
type Meta struct {
	// Source is where the data came from: "keyless", "api", "oauth", "yt-dlp", "local" or "cache".
	Source string `json:"source,omitempty"`
	// SourceReason says why that source was used ("no API key", "quota used up", ...).
	SourceReason string `json:"source_reason,omitempty"`
	QuotaUsed    int    `json:"quota_used,omitempty"`
	QuotaLeft    int    `json:"quota_left,omitempty"`
	Cached       bool   `json:"cached,omitempty"`
}

// Result is what a handler returns.
type Result struct {
	Data       any
	Meta       Meta
	Warnings   []string
	NextCursor string
}

// Handler runs one op.
type Handler func(ctx context.Context, in Input) (*Result, error)

var (
	mu       sync.RWMutex
	handlers = map[string]Handler{}
)

// Handle attaches the handler for an op. It panics on an unknown name so a typo
// fails at startup, not when an agent calls the command.
func Handle(name string, h Handler) {
	if Find(name) == nil {
		panic("ops.Handle: unknown op " + name)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := handlers[name]; dup {
		panic("ops.Handle: two handlers for " + name)
	}
	handlers[name] = h
}

// HandlerFor returns the op's handler, or one that answers not_implemented.
func HandlerFor(name string) Handler {
	mu.RLock()
	h := handlers[name]
	mu.RUnlock()
	if h != nil {
		return h
	}
	return func(context.Context, Input) (*Result, error) {
		return nil, apperr.New(apperr.NotImplemented, "%s is not built yet", name)
	}
}

// Implemented reports whether an op has a handler.
func Implemented(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return handlers[name] != nil
}

// Find returns the op with this name, or nil.
func Find(name string) *Op {
	for i := range All {
		if All[i].Name == name {
			return &All[i]
		}
	}
	return nil
}

// Names returns every op name, sorted.
func Names() []string {
	n := make([]string, 0, len(All))
	for _, o := range All {
		n = append(n, o.Name)
	}
	sort.Strings(n)
	return n
}
