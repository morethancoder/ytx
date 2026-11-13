---
name: ytx
description: Research YouTube and run a YouTube channel with the ytx CLI. Use to validate a video idea, find outlier videos, content gaps and search demand, read comments and transcripts, read your own channel analytics, or upload and edit videos. Triggers on YouTube, video idea, outliers, transcript, autocomplete, channel analytics, upload.
---

# Using YouTube with ytx

ytx reads YouTube and runs a channel, one command each. Every command prints one
JSON envelope.

## Start here

Run `ytx schema` first. It lists every command, its arguments, whether it is a
read, a write or local, what auth it needs, and the exit codes. Use `--no-skill`
to leave this file out of that output.

## Ground rules

1. Parse the envelope on every call and check `ok`. A success is
   `{"ok":true,"data":...,"meta":{...}}`. A failure is
   `{"ok":false,"error":{"code","message","detail"}}`. Branch on `error.code`,
   never on the message text.
2. Use `--fields a,b` to keep answers small.
3. Reads are cached. Pass `--no-cache` only when you need fresh data.

## Which command answers which question

- Is this video idea worth making? Run `ytx research "<topic>"`. It writes a
  Markdown note (to `--out`, else `research_dir` from config, else the current
  folder). Then give the note to ideacheck:
  `ideacheck --context="$(cat note.md)"`. Keep the `=`: the note starts with
  `---`, and without the `=` the shell passes it as a flag and it fails.
- Is there demand? `ytx suggest "<phrase>" --expand` for autocomplete.
  `ytx outliers "<topic>"` for videos that beat their channel's median, which
  shows what the audience wants. `ytx gaps "<topic>"` for questions viewers ask
  that the top videos don't answer.
- What do the top videos say? `ytx search`, then `ytx transcript <video> --text`
  and `ytx comments <video> --questions`.
- How is my channel doing? `ytx me` for totals, `ytx me videos` per video,
  `ytx me retention <video>` for one video's curve, and `ytx me learn` for a
  note comparing my best and worst videos. These need `ytx login`.
- Will my thumbnail and title stand out? `ytx feedcheck "<topic>" --thumb a.png --title "..."`
  (repeat `--thumb` and `--title` for versions; titles may contain commas). It writes
  review sheets to a new folder (`--out`, else `research_dir`, else the current folder, under
  `feedcheck/`): `v1.png` has a ring round your video, `v1-blind.png` hides it and moves your
  video to a random place, given only as `blind_position` in the data. Judge each blind sheet
  first: look at it and say where you think yours is and how it stands out, without reading
  `blind_position` from the result. Only then read `blind_position` and compare. It renders with the agd CLI (`YTX_AGD` or PATH);
  without
  agd it still writes the inputs and a warning gives the commands to render. A thumbnail that is
  not a JPG, PNG or GIF, or a title over 100 characters, is `invalid_input`; thumbnail shape and
  size problems are warnings.
- What are my uploads? `ytx my videos`.
- `ytx trending` is a substitute for a feed YouTube removed; treat it as a rough
  signal. Impressions and click-through rate are not available anywhere.
- Keyless comments have no publish date.

## Writes

These change the channel: `upload`, `edit`, `thumbnail`, `rate`, `playlist
create`, `playlist add`, `playlist remove`, `comment post`, `comment reply`,
`comment moderate`, `captions upload`.

- Run the write without `--yes` (or with `--dry-run`). With no terminal it fails
  with `approval_required` and the plan in `error.detail`.
- Show that plan to the user and wait for their answer.
- Pass `--yes` only when the user approved that exact plan. If the plan changes,
  ask again. Never add `--yes` on your own to get past the error.
- `declined` means the person said no. Do not retry.

## Quota

- Every answer has `meta.quota_used` and `meta.quota_left`. `ytx quota` shows the
  day's totals.
- The official API has 10,000 units a day, and search and upload each have a
  separate bucket of 100 calls.
- Prefer keyless for bulk reads (`--source keyless`); it costs nothing. Check
  `meta.quota_left` before a long batch, and avoid `--source api` unless needed.

## Exit codes

| Exit | Codes |
| --- | --- |
| 1 | internal_error, youtube_error, not_implemented |
| 2 | invalid_input: fix the arguments |
| 3 | not_found, no_transcript, comments_disabled: skip this video |
| 4 | login_required, needs_api_key, forbidden |
| 5 | quota_exceeded |
| 6 | approval_required, declined |
| 7 | network_error, timeout: may retry once |
| 8 | tool_missing: yt-dlp or ffmpeg is not installed |

What to do:

- `login_required`: tell the user to run `ytx login --client client_secret.json`
  (first time) or `ytx login`. You cannot sign in for them. Do not work around it.
- `needs_api_key`: tell the user to set `YOUTUBE_API_KEY` or run
  `printf %s "$KEY" | ytx key set`. Do not ask them to paste the key into the chat.
- `quota_exceeded`: stop using the API. Retry the read keyless with
  `--source keyless`, or wait for tomorrow. Writes cannot fall back.
- `forbidden`: the video is not theirs or is private; do not retry.
- `ytx doctor` shows which modes work.
