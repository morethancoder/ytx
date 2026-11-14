// Package cli turns the ops table into cobra commands and prints every result
// in one JSON envelope (or readable text on a terminal).
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/config"
	"github.com/morethancoder/ytx/internal/ops"
	"github.com/morethancoder/ytx/internal/quota"
	"github.com/morethancoder/ytx/internal/yt"
)

// Version is set at build time with -ldflags "-X .../cli.Version=...".
var Version = "dev"

var (
	gJSON, gHuman, gYes, gDryRun, gNoCache bool
	gFields, gSource                       string
	gTimeout                               time.Duration
)

// Execute runs ytx and returns the process exit code.
func Execute() int { return execute(os.Args[1:]) }

func execute(args []string) int {
	// Ctrl-C and SIGTERM cancel the context, so subprocesses (yt-dlp runs in its own
	// process group and never sees the terminal's Ctrl-C) are killed and cleaned up.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := NewRoot()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	var ee *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	default:
		// cobra's own errors (unknown command, bad flag) still get the envelope.
		_ = emitErr(apperr.New(apperr.InvalidInput, "%s", err.Error()))
		return apperr.ExitCode(apperr.InvalidInput)
	}
}

// NewRoot builds the whole command tree from ops.All.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "ytx",
		Short:         "YouTube for agents: research, your channel and analytics, keyless or with the official API",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&gJSON, "json", false, "JSON envelope output (default when stdout is not a terminal; also YTX_JSON=1)")
	pf.BoolVar(&gHuman, "human", false, "readable output even when piped")
	pf.StringVar(&gFields, "fields", "", "keep only these comma-separated (dotted) fields of each result")
	pf.BoolVar(&gYes, "yes", false, "approve a write without asking")
	pf.BoolVar(&gDryRun, "dry-run", false, "show what a write would do, without doing it")
	pf.BoolVar(&gNoCache, "no-cache", false, "don't use cached results")
	pf.StringVar(&gSource, "source", "auto", "where reads come from: auto (API when a key is set, else keyless), keyless or api")
	pf.DurationVar(&gTimeout, "timeout", 0, "give up after this long (default: 90s reads, 5m research, none for uploads and downloads)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return emitErr(apperr.New(apperr.InvalidInput, "%s", err.Error()))
	})

	for i := range ops.All {
		addOp(root, &ops.All[i])
	}
	root.AddCommand(schemaCmd(), serveCmd())
	sortCommands(root)
	return root
}

// addOp creates the command for an op, and any group commands above it.
func addOp(root *cobra.Command, op *ops.Op) {
	words := strings.Fields(op.Command)
	parent := root
	for _, w := range words[:len(words)-1] {
		parent = child(parent, w)
	}
	name := words[len(words)-1]
	cmd := findChild(parent, name)
	if cmd == nil {
		cmd = &cobra.Command{Use: name}
		parent.AddCommand(cmd)
	}
	var posNames []string
	minArgs := 0
	for _, p := range op.Params {
		if p.Positional {
			posNames = append(posNames, "<"+p.Name+">")
			if p.Required {
				minArgs++
			}
		}
	}
	cmd.Use = strings.Join(append([]string{name}, posNames...), " ")
	cmd.Short = op.Short
	if op.Kind == ops.Write {
		cmd.Short += " [write: asks first]"
	}
	maxArgs := len(posNames)
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) < minArgs || len(args) > maxArgs {
			return emitErr(apperr.New(apperr.InvalidInput, "usage: ytx %s", strings.TrimPrefix(c.CommandPath(), "ytx ")+usageTail(posNames)))
		}
		return nil
	}
	for _, p := range op.Params {
		if p.Positional {
			continue
		}
		f := cmd.Flags()
		switch p.Type {
		case ops.TInt:
			d, _ := p.Default.(int)
			f.Int(p.Name, d, p.Help)
		case ops.TBool:
			f.Bool(p.Name, false, p.Help)
		case ops.TStrings:
			f.StringArray(p.Name, nil, p.Help)
		default:
			d, _ := p.Default.(string)
			f.String(p.Name, d, p.Help)
		}
	}
	cmd.RunE = func(c *cobra.Command, args []string) error { return run(c, op, args) }
}

func usageTail(pos []string) string {
	if len(pos) == 0 {
		return ""
	}
	return " " + strings.Join(pos, " ")
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// child returns (or creates) a group command.
func child(parent *cobra.Command, name string) *cobra.Command {
	if c := findChild(parent, name); c != nil {
		return c
	}
	c := &cobra.Command{Use: name, Short: name + " commands"}
	parent.AddCommand(c)
	return c
}

func sortCommands(c *cobra.Command) {
	cobra.EnableCommandSorting = true
	for _, s := range c.Commands() {
		sortCommands(s)
	}
}

// input turns args and flags into an ops.Input.
func input(c *cobra.Command, op *ops.Op, args []string) (ops.Input, error) {
	in := ops.Input{Op: op, Values: map[string]any{}, Given: map[string]bool{}, Source: gSource, NoCache: gNoCache}
	i := 0
	for _, p := range op.Params {
		if p.Positional {
			if i < len(args) {
				in.Values[p.Name] = args[i]
				in.Given[p.Name] = true
				i++
			}
			continue
		}
		f := c.Flags().Lookup(p.Name)
		if f == nil {
			continue
		}
		in.Given[p.Name] = f.Changed
		if !f.Changed {
			if p.Required {
				return in, apperr.New(apperr.InvalidInput, "--%s is required", p.Name)
			}
			if p.Default == nil {
				continue
			}
		}
		switch p.Type {
		case ops.TInt:
			v, _ := c.Flags().GetInt(p.Name)
			in.Values[p.Name] = v
		case ops.TBool:
			v, _ := c.Flags().GetBool(p.Name)
			in.Values[p.Name] = v
		case ops.TStrings:
			v, _ := c.Flags().GetStringArray(p.Name)
			in.Values[p.Name] = v
		default:
			v, _ := c.Flags().GetString(p.Name)
			in.Values[p.Name] = v
		}
	}
	return in, nil
}

func timeoutFor(op *ops.Op) time.Duration {
	if gTimeout > 0 {
		return gTimeout
	}
	switch {
	case op.Name == "video.upload" || op.Name == "download" || op.Name == "captions.upload" || op.Name == "auth.login":
		return 0
	case strings.HasPrefix(op.Name, "research.") || op.Name == "me.learn" || op.Name == "feedcheck":
		return 5 * time.Minute
	}
	return 90 * time.Second
}

// run executes one op: writes go through the approval flow, then the result is printed.
func run(c *cobra.Command, op *ops.Op, args []string) error {
	in, err := input(c, op, args)
	if err != nil {
		return emitErr(err)
	}
	if gDryRun && op.Kind != ops.Write && op.Kind != ops.Read {
		// Local ops write files or settings; silently ignoring --dry-run would do the real thing.
		return emitErr(apperr.New(apperr.InvalidInput, "--dry-run only applies to commands that change YouTube; %s changes files or settings on this machine", op.Command))
	}
	base := c.Context()
	if base == nil {
		base = context.Background()
	}
	base, tr := yt.WithTrace(base)
	// withTimeout starts the op's clock; it is called again after the prompt, so time a
	// person spends reading the plan doesn't count against the write.
	withTimeout := func() (context.Context, context.CancelFunc) {
		if d := timeoutFor(op); d > 0 {
			return context.WithTimeout(base, d)
		}
		return context.WithCancel(base)
	}
	h := ops.HandlerFor(op.Name)

	if op.Kind == ops.Write && (!gYes || gDryRun) {
		in.DryRun = true
		pctx, pcancel := withTimeout()
		plan, err := h(pctx, in)
		pcancel()
		if err != nil {
			return emitErr(err)
		}
		if gDryRun {
			return emitResult(plan)
		}
		if !canAsk() {
			return emitErr(&apperr.Error{Code: apperr.ApprovalRequired,
				Message: "this changes your YouTube channel; rerun with --yes to approve (detail is the plan)",
				Detail:  planData(plan)})
		}
		if !ask(op, plan) {
			return emitErr(apperr.New(apperr.Declined, "not done"))
		}
		in.DryRun = false
	}

	ctx, cancel := withTimeout()
	defer cancel()
	res, err := h(ctx, in)
	if err != nil {
		return emitErr(err)
	}
	if res == nil {
		res = &ops.Result{}
	}
	fillMeta(&res.Meta, tr)
	return emitResult(res)
}

func planData(r *ops.Result) any {
	if r == nil {
		return nil
	}
	return r.Data
}

// fillMeta adds the trace (source, quota, cache) when the handler didn't set it.
func fillMeta(m *ops.Meta, tr *yt.Trace) {
	if m.Source == "" {
		m.Source = tr.Source()
	}
	if m.SourceReason == "" {
		m.SourceReason = tr.Reason
	}
	if m.QuotaUsed == 0 {
		m.QuotaUsed = tr.QuotaUsed
	}
	if m.QuotaUsed > 0 && m.QuotaLeft == 0 {
		cfg, _ := config.Load()
		if cfg != nil {
			m.QuotaLeft = quota.New(cfg.Limit()).Left()
		}
	}
	if tr.Cached > 0 {
		m.Cached = true
	}
}

// canAsk is true only when a person is at a terminal: never prompt a pipe or an agent.
// (a var so tests can pin it).
var canAsk = func() bool { return isTTY(os.Stdin) && isTTY(os.Stderr) }

// stdin reader for prompts (a var so tests can swap it).
var promptIn = bufio.NewReader(os.Stdin)

func ask(op *ops.Op, plan *ops.Result) bool {
	fmt.Fprintf(os.Stderr, "ytx %s will:\n", op.Command)
	if plan != nil && plan.Data != nil {
		printHuman(os.Stderr, &Envelope{OK: true, Data: plan.Data})
	}
	fmt.Fprint(os.Stderr, "Go ahead? [y/N] ")
	line, _ := promptIn.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// Commands lists every leaf command path, sorted (for tests and docs).
func Commands() []string {
	var out []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.RunE != nil && c.HasParent() {
			out = append(out, strings.TrimPrefix(c.CommandPath(), "ytx "))
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(NewRoot())
	sort.Strings(out)
	return out
}
