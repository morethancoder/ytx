---
name: open-findings
description: Log of bugs, stale docs and fixes found outside the current task; the next session checks this list
metadata:
  type: project
level: notes
updated: 2026-11-12
---

Open items found while building ytx (2026-11-12). Remove an item once it is fixed.

1. **Cancelling a download reports internal_error.** `internal/download` returns plain
   `context.Canceled`, which `apperr.From` maps to internal_error (exit 1). Fix: map
   `context.Canceled` to a `cancelled` or `timeout` code in apperr. Seen in review of the code, not run.
2. **Keyless comments have no publish date.** `internal/keyless/comments.go` leaves `published`
   at the zero value (commentEntityPayload gives only "3 weeks ago"), and like counts are rounded
   ("1.2K"). Fix: parse the relative time into an approximate date and mark it approximate.
3. **`ytx trending` is a substitute.** YouTube removed the FEtrending feed. `music` reads YouTube
   Charts weekly; `now`, `gaming` and `movies` search today's uploads sorted by views. With an API
   key, `videos.list chart=mostPopular` still works. Fix: document it in README and SKILL.md
   (done there) and revisit if YouTube adds an "Explore" feed back.
4. **The search bucket limit isn't configurable.** `quota.BucketLimit` is fixed at 100 calls/day
   for search and uploads. Projects that got a quota raise from Google can't use it. Fix: config
   keys `search_limit` and `upload_limit`.
5. **Analytics ops have no cache.** `me.*` results are fetched every time (the false
   `Cache: "1h"` was removed from list.go). Fix: wrap analytics reads in `internal/cache` for 1 h.
6. **ideacheck `--context` takes a string, not a file.** Passing a research note means
   `--context "$(cat note.md)"`, which hits shell argument limits on very large notes. Fix (in
   ideacheck): accept `--context @file.md`. Also, a note starts with `---` front matter, so
   `--context "$(cat note.md)"` fails with "unknown flag"; `--context="$(cat note.md)"` works.
   Logged here because ytx notes are its input (ideacheck has no findings log yet).
7. **ideacheck's video rubric still asks business follow-ups.** With `-r video`, `missing` lists
   problem, audience, solution, differentiation and founder background. Fix (in ideacheck): give
   the video rubric its own questions (hook, payoff, proof footage, format, packaging). Verified
   on a real run 2026-11-12.

8. **The demand group "vs" can be off topic.** `internal/research` asks autocomplete for
   "<topic> vs"; YouTube answers with "visual studio claude code tutorial", so the "vs" group
   holds a non-comparison. Evidence: the 2026-11-12 note on "claude code tutorial". Fix: keep a
   "vs" completion only when it contains " vs " or " versus ". Not fixed.
9. **Spam reaches "What viewers ask".** A self-promotion comment ("I posted a phased rollout
   guide ... on my profile", 0 likes) was listed as a viewer question in the same note. Fix:
   drop 0-like comments that point to a profile or link, or rank questions by likes with a
   floor. Not fixed.
10. **The installed agd is stale.** `~/go/bin/agd` (built 2026-11-08) has no `y4-feed-preview`
    template (`agd new y4-feed-preview` answers `template_not_found`), so `ytx feedcheck` fails at
    `agd new` with it on PATH. Evidence: ran it 2026-11-12; a build of `~/Projects/CLI/agd` works.
    Fix: `make install` in the agd repo. Verified. Not fixed (agd is out of scope).
11. **No exported shared HTTP client in ytx.** `keyless` has an unexported `sharedHTTP`, `channel`
    has its own `baseClient`; feedcheck added a third (`httpClient`, 30s timeout) for image
    downloads. Fix: one exported client in a small package. Not fixed.
12. **Signed-in channel lookup is duplicated.** `internal/channel/api.go` has `myChannel` but it
    is unexported and returns no avatar, so feedcheck has its own `channels?mine=true` call
    (`internal/feedcheck/mine.go`). Fix: export a `channel.Mine(ctx)` with name, id and avatar.
