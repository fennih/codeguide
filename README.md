# ccwatch

Terminal dashboard for many Claude Code sessions running in [cmux](https://cmux.com). One tile per active session shows the current goal, what the agent is doing right now (tool calls, intent, subagents), and what it needs from you. Sessions waiting on you come first. Press Enter to jump to that session's tab.

ccwatch only reads. It never types into a session.

## Data sources

- `cmux current --json`: live sessions, state, tab title, unread flag.
- `~/.cmuxterm/workstream.jsonl`: cmux's Feed log, which records the Claude Code hooks that cmux injects (prompts, tool calls, stops, pending questions and approvals).
- `cmux sessions list --json`: transcript path per session.
- `~/.claude/projects/<project>/<session>.jsonl` and its `subagents/`: what the agent says between actions, full tool calls, errors and edit diffs. Only the transcripts of working sessions, of sessions that recently stopped to wait on you, and of the session open in the detail view are read, starting from the last 3 MB. This is Claude Code's internal format; if it changes, tiles fall back to the Feed and subagent rows to labels.
- Optional: a one-line goal and turn outcome per session written by Haiku through `claude -p`. The call has no tools, no MCP, and no settings or hooks loaded, and secrets are redacted from the input before it is sent. Results are cached in `~/.local/state/ccwatch/`. The default cap is 60 calls per hour, at roughly $0.0003 per call.
- Optional: a live "ora:" line for working sessions, written by the same isolated call at most every 90 seconds and only after 8 new actions. It has its own cap (default 40 per hour) and is not cached.
- `cmux events` (focused tab, window focus) and `cmux identify`: to know when you last looked at each session. A session counts as seen while its tab is in front, when you type a prompt in it, or when you open it from ccwatch. Only session ids and times are stored, in `~/.local/state/ccwatch/seen.json` (mode 0600).

Everything read from transcripts is untrusted: terminal escapes and bidi controls are stripped, and common secret formats are masked before display, including in diffs.

## Build

```sh
go build -o bin/ccwatch ./cmd/ccwatch
```

## Run

Run it inside cmux, either in its own workspace or as a Dock terminal:

```sh
bin/ccwatch                 # dashboard with summaries
bin/ccwatch --no-llm        # no model calls
bin/ccwatch --once          # one JSON snapshot, no model calls
bin/ccwatch --frame 120x40  # print one frame and exit
bin/ccwatch --frame 120x40 --detail <session-id>  # one frame of a detail view
```

Tiles show what happened since you last looked ("novità da 47m: 3 turni · 21 azioni · 2 errori").

List keys: arrows or `j`/`k` to select, `Enter` (or a click on the selected tile) to open the live detail view, `o` to jump to the cmux tab, `/` to filter, `g` to group by project, `Tab` to show old sessions, `r` to show only sessions with news, `s` to regenerate a summary, `?` for help, `q` to quit. The mouse wheel moves the selection.

Detail keys: `Enter` or `o` to jump to the tab, `u` to jump to where you left off ("eri qui"), `t` for the outline of past turns, `d` to show edit diffs, `x` to expand finished subagents, arrows/`PgUp`/`PgDn`/wheel to scroll (scrolling up pauses the live view), `f` to follow again, `n`/`p` for the next or previous session, `Esc` to go back.

Flags: `--stale-after` (default 24h), `--max-per-hour`, `--no-narrate`, `--narrate-per-hour`, `--no-mouse` (keeps the terminal's own text selection), `--cold-start` (sessions summarized at startup), `--model`, `--claude-bin`, `--cmux-bin`, `--state-dir`.
