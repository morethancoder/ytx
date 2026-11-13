# ytx: rules for anyone working here

ytx is YouTube for agents, a Go CLI modelled on `~/Projects/CLI/igx`. Read `internal/ops/ops.go`
and `internal/ops/list.go` first: the ops table is the single source of truth for every command.

## How the code is laid out

- `internal/ops`: the table of commands (`list.go`), `Input`, `Result`, `Meta`, `Handle()`.
- `internal/cli`: cobra commands built from the table, the JSON envelope, the write approval flow,
  `schema`, `doctor`, `key`, `config`, `quota`, `cache`.
- `internal/yt`: the `Reader` interface (public reads), `Get()` which picks keyless or the
  official API and falls back, the disk cache wrapper, the trace (which source answered),
  and the id/URL parsers.
- `internal/reads`: the read commands, all through `yt.Get`.
- `internal/analyze`: pure helpers (median, outlier, question detection, key terms).
- `internal/apperr`: error codes. Return `apperr.New(code, ...)`; never invent a code
  without adding it there with an exit status.
- An area attaches its handlers with `ops.Handle("<op name>", fn)` in an `init()` in its own
  package; `cmd/ytx/main.go` blank-imports that package. Do not edit another area's files.

## Rules

- Every command prints one envelope (`internal/cli/output.go`). Handlers return `*ops.Result`
  or an `apperr` error. They never print to stdout. Progress text goes to stderr only
  when stderr is a terminal.
- Writes (`Kind: Write`) get `in.DryRun == true` first. The handler then returns the plan
  (what it will change, as data) without calling YouTube's write endpoint. The CLI shows the
  plan, asks, and calls the handler again with `DryRun == false`. Never prompt inside a handler.
- Nothing may wait for input without a terminal. Secrets go in the Keychain via
  `internal/config` (`YTX_NO_KEYCHAIN=1` turns it off in tests), or come from env. Never write
  them to a file or log them.
- Official API calls spend quota with `quota.Tracker.Spend(units)` before the call, and call
  `Exhaust()` when Google answers `quotaExceeded`. Return `apperr.QuotaExceeded` so `yt.Get`
  falls back to keyless.
- HTTP: one shared `*http.Client` with timeouts. Respect `ctx`. No retries beyond one retry
  on 429/5xx with a short backoff.
- Tests: offline by default. Use recorded responses in `testdata/<area>/` and an
  `httptest.Server` or a swappable base URL. Live tests carry the build tag `live`, have names
  starting `TestLive`, and run with `make live`.
- Keep `gofmt` and `go vet` clean (`make lint`). Run `make test` before reporting.
- Write code comments as plain sentences explaining why.
- No new dependencies without a reason in the report. Prefer the standard library.
  `golang.org/x/oauth2` is allowed for the OAuth area.
- Don't commit. The lead merges and commits.
