# codeguide

A terminal dashboard for people who run many [Claude Code](https://www.anthropic.com/claude-code) sessions at once in [cmux](https://cmux.com). Each session gets a tile with what it is trying to do, why, what it is doing right now and whether it is waiting on you. Sessions that need you come first. Enter takes you to the session's tab, and Space opens a live, readable account of it.

With six or ten agents running, tab titles stop helping. You cycle through tabs, scroll back to find the original request, and miss the permission prompt that has been waiting for twenty minutes. codeguide reads the logs that Claude Code and cmux already write and puts the answers on one screen. It only reads: it never types into a session.

```
codeguide  2 working · 1 need you · 2 idle                                                 212 calls today  14:56:05
── NEEDS YOU (1) ───────────────────────────────────────────────────────────────────────────────────────────────────
╭─ ▲ checkout-retries • ───────────── payments-api · 2m ─╮
│ goal   Add idempotent retries to the checkout webhook  │
│        handler                                         │
│ why    Duplicate charges came from retried webhooks.   │
│        The handler now dedupes on event id; the queue  │
│        change needs your approval.                     │
│ ▲ permission: Bash  terraform apply -target=module.qu… │
│ news since 25m: 1 turn · 14 actions                    │
│ ● Edits internal/webhook/handler.go             +42 −7 │
│ ● Runs  Run the webhook tests                          │
│ ● Runs  Apply the queue visibility timeout             │
│ 3 actions · +42 −7                                     │
╰────────────────────────────────────────────────────────╯
── WORKING (2) ─────────────────────────────────────────────────────────────────────────────────────────────────────
╭─ ✽ flaky-e2e ────────────────────── web-app · 12m 00s ─╮╭─ ✽ orders-index ─────────────── payments-api · 4m 00s ─╮
│ goal   Find why the login e2e test fails one run in    ││ goal   Add a composite index for the slow              │
│        five on CI                                      ││        orders-by-customer query                        │
│ why    The failure only shows on CI. A subagent is     ││ why    The query takes 900ms at p95. The migration is  │
│        looking for fixed timeouts while the main       ││        written; it is now checking the plan on a copy  │
│        session reads the trace of a failed run.        ││        of staging.                                     │
│ now    Reading the Playwright trace of the last faile… ││ now    Comparing the query plan before and after the … │
│ ▸ The redirect after login takes 4.8s on CI, close to… ││ ▸ Index created. Running EXPLAIN ANALYZE on the origi… │
│ ● Reads     tests/e2e/login.spec.ts                    ││ ● Writes migrations/0042_orders_customer_idx.sql    +6 │
│ ● Delegates Explore: CI timing                         ││ ● Runs   Apply the migration on the local copy         │
│ ● Runs      Download the trace of run 4812             ││ ● Runs   EXPLAIN ANALYZE the orders query              │
│ ● Reads     trace/network.json                         ││ ✽ 4m 00s · 3 actions · +6                              │
│ ↳ Explore Find every place the login test waits on … ● │╰────────────────────────────────────────────────────────╯
│     ● Searches waitForURL  tests/e2e                   │
│ ✽ 12m 00s · 4 actions · 1 subagent active              │
╰────────────────────────────────────────────────────────╯
── IDLE (2) ────────────────────────────────────────────────────────────────────────────────────────────────────────
  · release-notes                      docs               40m  Release notes drafted from 31 merged PRs, waiting fo…
  ✦ deps-bump                          cli                3h   All modules updated, tests green, one breaking chang…
```

The sessions in this frame are made up.

## What it shows

### The board

Sessions are grouped into three lanes: waiting on you (a permission prompt, a plan to approve, a question), working, and idle. Sessions idle for more than a day fold away. A tile shows:

- the goal of the current request, in one or two lines;
- a short "why" paragraph: the larger aim, what is done and what comes next;
- what you need to do, if anything;
- a "now" line and the agent's latest remark while it works;
- the last few tool calls with lines added and removed;
- running subagents with their mission and current tool, or their result once they finish.

### Live detail

Space (or `v`) opens one session as a running story: your prompts, what the agent says between actions, every tool call with its outcome, errors with their first line, and edit diffs on demand. Subagents sit under the call that started them, with the brief they received and the report they returned. Scrolling up pauses the live view; `f` resumes it. `t` switches to an outline of past turns, one line per turn with its outcome.

### Catching up

codeguide remembers when you last looked at each session: while its tab is in front, when you type a prompt in it, or when you open it from the board. Tiles then show what happened since ("news since 47m: 3 turns · 21 actions · 2 errors"), `r` lists only the sessions with news, and in the detail view `u` jumps to a "you were here" divider.

### Context and plan usage

Each tile shows how full the session's context window was at its latest request (`ctx ▰▰▱▱▱▱▱▱ 24% 240k/1M`), and each running subagent shows its own figure. Idle rows show only the percentage. The detail view puts it in its header and next to each subagent. codeguide guesses the window size from the model name: 1M for the Claude 5 Opus, Sonnet and Fable models, 200k for the rest, and 1M for any session whose requests have gone past 200k.

The header shows your plan limits (the 5-hour session, the week, and any per-model weekly limit) as Claude Code last fetched them for `/usage`. Claude Code refreshes that cache on its own schedule. When the figures are more than 15 minutes old the header says how old they are, and it drops the percentage for any window that has already reset.

### External effects

`e` lists what agents changed outside your machine, newest first: Jira and Confluence writes, Google documents, other MCP tools whose names say they write, browser automation that clicks or types, and shell commands such as `git push`, `gh pr create`, `gh api` with a write method, `aws`, `gcloud`, `kubectl`, `terraform`/`pulumi`, `curl` with a body, and package publishing. It covers closed sessions too, since it is built from the cmux Feed log. For sessions that are still open, it adds targets and outcomes from the transcript.

Treat this list as a floor. A script can write anywhere without matching a rule, and MCP tools whose names do not say whether they read or write are shown as unclassified.

### Summaries

The goal, the "why" paragraph, the outcome of each turn and each subagent's mission are written by Haiku through `claude -p`, using your existing Claude Code login. You do not need an API key. Calls are capped per hour and cached, and the header shows how many have been made and what `claude -p` reports they cost. With `--no-llm` codeguide makes no model calls and falls back to your prompts and the first sentence of each subagent's brief.

## Requirements

- macOS with [cmux](https://cmux.com), running your Claude Code sessions in its tabs
- Claude Code, logged in, for summaries (optional)
- Go 1.24 or newer to build

## Install

```sh
go install github.com/fennih/codeguide/cmd/codeguide@latest
```

Or from a clone:

```sh
git clone https://github.com/fennih/codeguide
cd codeguide
go build -o bin/codeguide ./cmd/codeguide
```

## Run

Run it inside cmux, in its own workspace or as a Dock terminal:

```sh
codeguide               # dashboard with summaries
codeguide --no-llm      # no model calls
codeguide --lang it     # Italian interface
```

For scripts and debugging:

```sh
codeguide --once                                 # one JSON snapshot, no model calls
codeguide --frame 120x40                         # print one frame and exit
codeguide --frame 120x40 --detail <session-id>   # one frame of the detail view
codeguide --frame 160x40 --effects               # one frame of the effects view
```

## Keys

Board:

| Key | Action |
| --- | --- |
| `↑` `↓` / `j` `k` | select a session (the mouse wheel works too) |
| `Enter` / `o`, or a click on the selected tile | jump to the session's cmux tab |
| `Space` / `v` | open the live detail view |
| `r` | show only sessions with news |
| `e` | external effects |
| `/` | filter by title, project or goal |
| `g` | group by project |
| `Tab` | show old sessions |
| `s` | regenerate the summary of the selected session |
| `?` | help |
| `q` | quit |

Detail view:

| Key | Action |
| --- | --- |
| `Enter` / `o` | jump to the session's cmux tab |
| `u` | jump to where you left off |
| `t` | outline of past turns |
| `d` | show edit diffs |
| `x` | expand finished subagents with their full brief and report |
| `↑` `↓`, `PgUp` `PgDn`, wheel | scroll (scrolling up pauses the live view) |
| `f` | follow the live view again |
| `n` / `p` | next or previous session |
| `Esc` | back to the board |

In the effects view, `Enter` opens the session behind a row, `/` filters and `Esc` goes back.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--lang` | `en` | interface language, `en` or `it` |
| `--no-llm` | off | no summaries and no live narration |
| `--no-narrate` | off | keep summaries, skip the live "now" line |
| `--max-per-hour` | 60 | cap on summary calls per hour |
| `--narrate-per-hour` | 40 | cap on live narration calls per hour |
| `--cold-start` | 20 | recent sessions summarized at startup |
| `--stale-after` | 24h | idle time after which a session folds away |
| `--model` | `haiku` | model for summaries |
| `--no-mouse` | off | leave the mouse to the terminal, so text selection works |
| `--claude-bin` | first `claude` in `PATH` | Claude Code binary for summaries |
| `--cmux-bin` | `$CMUX_BUNDLED_CLI_PATH` or `cmux` | cmux CLI |
| `--state-dir` | `~/.local/state/codeguide` | cache directory |
| `--workstream` | `~/.cmuxterm/workstream.jsonl` | cmux Feed log |

## How it works

codeguide refreshes every 0.7 seconds from local sources:

- `cmux current --json` for the open sessions, their state, tab titles and unread flags;
- `~/.cmuxterm/workstream.jsonl`, the cmux Feed log, which records the Claude Code hooks cmux installs: prompts, tool calls, stops, pending questions and approvals;
- `cmux sessions list --json` for the transcript path of each session;
- the Claude Code transcripts in `~/.claude/projects/` and their `subagents/` folders, for what the agent says between actions, full tool calls, errors, diffs, subagent briefs and reports. It reads only working sessions, sessions that recently stopped to wait on you, and the one open in the detail view, starting from the last 3 MB of each file;
- `cmux events` and `cmux identify`, to know which tab is in front;
- `~/.claude.json` (or `$CLAUDE_CONFIG_DIR/.claude.json`), at most every 10 seconds, for the cached plan usage only.

The transcript format is internal to Claude Code and can change. When it cannot be read, tiles fall back to the Feed log.

## Privacy and security

Transcripts can contain anything the agents read, including secrets and text written by third parties, so codeguide treats all of it as untrusted:

- terminal escape sequences and bidirectional control characters are stripped before anything is drawn;
- common secret formats (cloud keys, tokens, private keys, bearer headers) are masked on screen, in diffs and in the text sent for summaries;
- summary calls run `claude -p` with no tools, no MCP servers, no settings or hooks, no session persistence and a JSON schema for the answer; session text is wrapped in delimiters with angle brackets escaped, and the answer is sanitized again before display;
- external commands run with argument vectors, never through a shell;
- the only files written are the summary cache and the list of when you last looked at each session (session ids and times), in `~/.local/state/codeguide/` with mode 0600.

The summary calls send redacted excerpts of your sessions to Anthropic through Claude Code, the same way your sessions already do. Use `--no-llm` if you do not want that.

## License

MIT
