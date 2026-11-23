# ccwatch

Terminal dashboard for many Claude Code sessions running in [cmux](https://cmux.com). One tile per active session shows the current goal, what the agent is doing right now (tool calls, intent, subagents), and what it needs from you. Sessions waiting on you come first. Press Enter to jump to that session's tab.

ccwatch only reads. It never types into a session.

## Data sources

- `cmux current --json`: live sessions, state, tab title, unread flag.
- `~/.cmuxterm/workstream.jsonl`: cmux's Feed log, which records the Claude Code hooks that cmux injects (prompts, tool calls, stops, pending questions and approvals).
- `cmux sessions list --json`: transcript path per session.
- `~/.claude/projects/<project>/<session>/subagents/`: subagent type, task, and current tool. This is Claude Code's internal format; if it changes, subagent rows fall back to labels only.
- Optional: a one-line goal and turn outcome per session written by Haiku through `claude -p`. The call has no tools, no MCP, and no settings or hooks loaded, and secrets are redacted from the input before it is sent. Results are cached in `~/.local/state/ccwatch/`. The default cap is 60 calls per hour, at roughly $0.0003 per call.

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
```

Keys: arrows or `j`/`k` to select, `Enter` to jump to the tab, `/` to filter, `g` to group by project, `Tab` to show old sessions, `s` to regenerate a summary, `?` for help, `q` to quit.

Flags: `--stale-after` (default 24h), `--max-per-hour`, `--cold-start` (sessions summarized at startup), `--model`, `--claude-bin`, `--cmux-bin`, `--state-dir`.
