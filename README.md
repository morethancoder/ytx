# ytx: YouTube for agents

`ytx` reads YouTube, researches video ideas, runs your channel and reads your
analytics, one command each. Every command prints one JSON envelope, so an
agent can drive it and a person can read it.

It works in three modes, and each one adds to the last.

## Install

Needs Go to build. `yt-dlp` and `ffmpeg` are only needed for `ytx download`.

```bash
go install github.com/morethancoder/ytx/cmd/ytx@latest   # or, from a clone:
make install     # go install ytx into GOBIN
make build       # or build ./ytx in this folder (VERSION=v1.2.3 stamps a version)
make doctor      # checks go, yt-dlp and ffmpeg, then runs `ytx doctor`
ytx doctor       # shows which modes work on this machine
```

## The three modes

**Keyless (default).** No account and no key. Reading commands work out of the
box: search, videos, channels, comments, transcripts, autocomplete, playlists
and research. Nothing is charged against any quota.

**API key.** Set `YOUTUBE_API_KEY`, or store a key in the Keychain:

```bash
printf %s "$KEY" | ytx key set
ytx key remove
```

With a key, reads use the official YouTube Data API and fall back to keyless
when the quota runs out. `--source keyless` or `--source api` forces one.

**Signed in.** Needed for your own channel: your videos, writes, captions and
analytics.

```bash
ytx login --client client_secret.json   # first time; opens the browser once
ytx login                               # later
ytx logout
```

To make the OAuth client in Google Cloud:

1. Create a project in the Google Cloud console.
2. Under APIs and Services, Library, enable the YouTube Data API v3 and the
   YouTube Analytics API.
3. Set up the OAuth consent screen and add your Google account as a test user.
4. Under Credentials, create an OAuth client ID and choose the application type
   Desktop app.
5. Download the JSON and pass it once with `ytx login --client`.

## Commands

Run `ytx schema` for the full list with every argument. Commands marked
(write) change YouTube and ask for approval first.

| Group | Command | What it does |
| --- | --- | --- |
| Reading | `ytx search <query>` | Search videos (flags: `--limit`, `--type`, `--order`, `--since`, `--lang`, `--region`) |
| | `ytx video <video>` | One video's details |
| | `ytx channel <channel>` | A channel and recent uploads, with their median views (`--shorts`) |
| | `ytx comments <video>` | Comments, most liked first, questions marked (`--questions`, `--order`) |
| | `ytx transcript <video>` | The transcript (`--lang`, `--text`) |
| | `ytx suggest <query>` | Autocomplete, a demand signal (`--expand`) |
| | `ytx trending` | What is trending in a region (`--region`, `--category`) |
| | `ytx playlist <playlist>` | A playlist and its videos |
| Research | `ytx outliers <query>` | Videos ranked by views divided by their channel's median |
| | `ytx gaps <query>` | Questions viewers ask that the top transcripts don't answer |
| | `ytx research <query>` | One Markdown note combining the above (`--out`, `--top`, `--name`) |
| Packaging | `ytx feedcheck <topic> --thumb a.png --title "..."` | Your thumbnail and title among the top five competitors, as review sheets (see below) |
| Your channel | `ytx my videos` | Your uploads, private and scheduled included |
| | `ytx upload <file> --title ...` | Upload a video (write) |
| | `ytx edit <video>` | Change title, description, tags, privacy (write) |
| | `ytx thumbnail <video> <file>` | Set a thumbnail (write) |
| | `ytx rate <video> <rating>` | Like, dislike or clear (write) |
| | `ytx playlist create <title>` | Create a playlist (write) |
| | `ytx playlist add <playlist> <video>` | Add a video (write) |
| | `ytx playlist remove <playlist> <video>` | Remove a video (write) |
| | `ytx comment post <video> <text>` | Post a comment (write) |
| | `ytx comment reply <comment> <text>` | Reply (write) |
| | `ytx comment moderate <comment> <status>` | Publish, hold or reject (write) |
| | `ytx captions <video>` | List caption tracks |
| | `ytx captions upload <video> <file> --lang <code>` | Upload SRT or VTT (write) |
| Analytics | `ytx me` | Channel totals for the last 28 days |
| | `ytx me videos` | Per-video views, watch time, average view duration |
| | `ytx me retention <video>` | One video's retention curve |
| | `ytx me learn` | A Markdown note comparing your best and worst videos |
| Files | `ytx download <video>` | Download video or audio with yt-dlp (`--format`, `--height`, `--out`) |
| Setup | `ytx login`, `ytx logout` | Google sign-in |
| | `ytx key set`, `ytx key remove` | Store or remove the API key |
| | `ytx doctor` | Which modes work |
| | `ytx quota` | Official API units used and left today |
| | `ytx config`, `ytx config set <key> <value>` | Show or set `research_dir`, `region`, `lang`, `quota_limit` |
| | `ytx cache clear` | Delete cached results |
| | `ytx schema` | Every command as JSON, plus SKILL.md, for agents |

Global flags: `--json`, `--human`, `--fields`, `--yes`, `--dry-run`,
`--no-cache`, `--source`, `--timeout`.

## Output

Output is JSON when stdout is not a terminal, or with `--json` or `YTX_JSON=1`.
`--human` forces readable text.

```json
{"ok": true, "data": {}, "next_cursor": "", "warnings": [],
 "meta": {"source": "keyless", "quota_used": 0, "quota_left": 0, "cached": false}}
```

A failure has `"ok": false` and `"error": {"code", "message", "detail"}`.
The exit code says what kind of failure it was:

| Exit | Codes |
| --- | --- |
| 0 | success |
| 1 | internal_error, youtube_error, not_implemented |
| 2 | invalid_input |
| 3 | not_found, no_transcript, comments_disabled |
| 4 | login_required, needs_api_key, forbidden |
| 5 | quota_exceeded |
| 6 | approval_required, declined |
| 7 | network_error, timeout |
| 8 | tool_missing |

## Writes

A command that changes your channel never runs unasked.

- `--dry-run` prints the plan (what would change) and does nothing.
- At a terminal, ytx shows the plan and asks `Go ahead? [y/N]`.
- `--yes` approves without asking.
- With no terminal and no `--yes`, it fails with `approval_required` (exit 6) and
  puts the plan in `error.detail`.

## Quota

The official API has a pool of 10,000 units a day (`quota_limit` in config
changes the count ytx uses). Search and upload each also have their own bucket of
100 calls a day. `ytx quota` shows what is used and left, and every answer
reports `meta.quota_used` and `meta.quota_left`. Keyless reads cost nothing.
When the pool runs out, reads fall back to keyless; commands that need the API
fail with `quota_exceeded`.

## Caching

Reads are cached on disk, so repeating a question is free: search and autocomplete
for 24 hours, comments 6 hours, videos, channels, playlists and trending 1 hour,
transcripts 30 days. `--no-cache` skips the cache for one call, and
`ytx cache clear` empties it. `meta.cached` says when an answer came from it.

## Feed check

`ytx feedcheck "<topic>" --thumb a.png --title "My title"` shows your thumbnail and title
among the five first long videos YouTube returns for the topic (Shorts, live streams, premieres
and your own channel left out), in three mocked feeds at real size: desktop home grid, up-next
sidebar and phone. `--thumb` and `--title` repeat (titles may contain commas); every thumbnail
is paired with every title, up to 12 sheets. Each pair gets a marked sheet (`v1.png`, a ring
around your video at `--position`, 1 to 6, default 2) and a blind sheet (`v1-blind.png`, no
ring, your video at a random place; that place is only in the JSON as `blind_position`; the phone feed shows a random three-card window that holds your video; a blind sheet's heading is the topic, its
note says "Blind check", and your card shows the competitors' median views and "2 days ago").
Other flags: `--out`, `--lang`, `--region`, `--theme` (an agd palette: light, dark, oled,
studio, ash), `--duration 12:04` (your duration badge; none without it), `--channel` and
`--avatar` (used when you are not signed in; with `ytx login` your channel's name and avatar
are read, and nothing starts a login).

Everything goes in a new folder, `<--out or research_dir or the current folder>/feedcheck/<topic>-<date>/`
(`-2`, `-3` when it exists; nothing is overwritten): the competitors' thumbnails and
avatars, `v1.input.json`, `v1.agd.json` and the PNGs. Rendering uses the
[agd](../agd) CLI (template `y4-feed-preview`); set `YTX_AGD` to its path, or have `agd` on PATH.
Without agd the input files are still written and a warning gives the exact commands to render.
Problems agd's validate finds come back as warnings naming the sheet. Warnings also say when a
thumbnail is not 16:9, is under 1280x720 or is over 2 MB. The data has `folder`, `competitors`,
`sheets` and `yours_position`.

## Where research notes go

`ytx research` and `ytx me learn` write a Markdown note. The folder is
`--out`, else `research_dir` from config (`ytx config set research_dir ~/vault/research`),
else the current folder.

## Known limits

- `ytx trending` is a substitute. YouTube removed its trending feed. `music` reads
  YouTube Charts weekly; `now`, `gaming` and `movies` search today's uploads sorted
  by views. With an API key, the real most-popular chart is used.
- Impressions and click-through rate are not in the YouTube Analytics API, so
  `ytx me videos` cannot show them.
- Keyless comments have no publish date, and like counts are rounded.
- A cached research note says `source: youtube cache` and hides whether the data
  was keyless or API.

## Development

```bash
make test    # gofmt check, go vet and the offline tests (RACE=1 for -race)
make live    # live tests against real YouTube (API ones too when YOUTUBE_API_KEY is set)
make lint    # gofmt check and go vet only
make fmt
make clean
```

`internal/ops/list.go` is the single source of truth for every command. See
`CLAUDE.md` for the code layout and rules.

## License

MIT, see [LICENSE](LICENSE).
