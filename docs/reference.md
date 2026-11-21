# Reference

- [Commands](#commands)
- [Environment variables](#environment-variables)
- [MCP tools](#mcp-tools)
- [Hooks](#hooks)
- [Data layout](#data-layout)
- [Troubleshooting](#troubleshooting)
- [Development](#development)

## Commands

Every command accepts `--help`. Ids look like `Y-2APW` (directive), `D-PG18` (decision) and
`R-7GE1` (link).

### Setup

| Command | What it does |
| --- | --- |
| `dumen init [--store URL#BRANCH] [--project NAME] [--repo NAME]` | Create the store and write `.dumen`. The default store is `origin#dumen`. `--repo` names this repository when several share one store. |
| `dumen join` | Connect this machine to the store named in `.dumen`. |
| `dumen join URL` | Create a workspace without code, for a PM or designer, under `~/dumen`. |
| `dumen install [--binary PATH]` | Write the hooks, MCP registration and skill into the repository, instead of using the plugin. |
| `dumen role set EMAIL ROLE`, `role rm EMAIL`, `role list` | Manage who is on the team. |
| `dumen status` | Sync state, your role, queued operations, unreadable records. |
| `dumen sync [--forget] [--quiet] [--project NAME]` | Push queued operations and refresh the cache now. `--forget` clears the list of rejected operations. |
| `dumen gc` | Archive stale drafts and compact activity older than 30 days. |
| `dumen version`, `dumen help` | The version, the command list. |

### Directives

| Command | What it does |
| --- | --- |
| `dumen direct [scope] [--deny] [--for 7d \| --until DATE] "text"` | Publish a directive. Scope is one of `--all`, `--role ROLE`, `--area AREA`, `--paths a,b`. `--deny` blocks edits and needs `--paths` or `--area`. |
| `dumen direct --propose ... "text"` | Propose a directive. Any role, and agents, may do this. |
| `dumen direct --publish ID` | Put a proposal in force, as it is. |
| `dumen directives [--all]` | List active directives. `--all` adds expired, withdrawn and proposed ones. |
| `dumen withdraw ID` | Withdraw a directive. |

### Decisions and links

| Command | What it does |
| --- | --- |
| `dumen decide --title T [-m BODY \| --file F] [--paths a,b] [--area AREA] [--sources R-1,R-2] [--approve]` | Record a decision. It is a draft unless `--approve` is given by someone who may approve. |
| `dumen decisions [PATH] [--drafts] [--all]` | List approved decisions, optionally those that govern a path. |
| `dumen show ID` | Show a decision, directive or link in full. |
| `dumen approve ID`, `dumen reject ID` | Approve or reject a draft. |
| `dumen supersede ID --title T [-m BODY \| --file F]` | Replace an approved decision with a new one. |
| `dumen ref add URL [--kind design\|artifact\|mr\|doc] --title T [--governs TEXT] [--paths a,b] [--area AREA]` | Record a link to a source of truth. |
| `dumen ref check ID`, `dumen ref list` | Confirm a link, list links. |

### Monitoring

| Command | What it does |
| --- | --- |
| `dumen activity [--since WHEN] [--person P] [--repo R] [--kind K] [--events] [--json]` | Counts, sessions, who is stuck, who has not acknowledged. `--since` takes `today`, `yesterday`, `2h`, `7d`, a date or an RFC 3339 time. |
| `dumen watch [--since 10m] [--interval 5s] [--once]` | Live tail of activity. |
| `dumen check [BASE..HEAD]` | Fail (exit 1) when a change touches a path under an active deny directive. Without a range it checks the work tree against HEAD. `BASE HEAD` as two arguments also works. |
| `dumen private [on\|off]` | Stop or resume activity from your new sessions. |

### For Claude Code, not for people

| Command | What it does |
| --- | --- |
| `dumen mcp` | The MCP stdio server. It also runs the background sync. |
| `dumen hook EVENT` | The hook entry point. It reads the hook JSON on stdin. |

Human actions (`approve`, `reject`, `supersede`, `direct`, `withdraw`, `private`, `role set`,
`role rm`, `ref check`) are refused when the command runs inside a Claude Code session.

## Environment variables

| Variable | Effect |
| --- | --- |
| `DUMEN_HOME` | Where local state lives. Default `~/.dumen`. |
| `DUMEN_POLL_INTERVAL` | How often a session checks the remote, for example `10s`. Default 30 seconds, minimum 1 second. |
| `DUMEN_WORKSPACES` | Where `dumen join URL` creates a workspace. Default `~/dumen`. |

dumen also reads two variables that Claude Code sets: `CLAUDECODE` (the command runs inside a
session, so the actor is an agent) and `CLAUDE_PROJECT_DIR`.

## MCP tools

Claude Code starts `dumen mcp` for each session. Through the plugin the tools are named
`mcp__plugin_dumen_dumen__<tool>`; through `dumen install` they are `mcp__dumen__<tool>`.

| Tool | What it does | Who confirms |
| --- | --- | --- |
| `directives` | List the directives in force for this session, with full text. | nobody |
| `ack_directive` | Record that this session received a directive. | nobody |
| `propose_directive` | Propose a directive for a PM or lead to publish. | nobody |
| `publish_directive` | Publish a directive to the team's agents. PM and lead only. | **the person** |
| `search_decisions` | Search decisions by words in the title, area or paths. | nobody |
| `get_decision` | Get one decision in full. Given a link id (`R-...`), it returns that link, once a person has confirmed it. | nobody |
| `decisions_for_path` | Approved decisions, blocking directives and links for a file path. | nobody |
| `draft_decision` | Record a draft decision for a lasting choice. | nobody |
| `approve` | Approve a draft decision. | **the person** |
| `supersede` | Replace an approved decision with a new approved one. | **the person** |
| `add_ref` | Record a link. It stays unverified until a person runs `dumen ref check`. | nobody |
| `log_event` | Log where the session stands: `asking_human`, `blocked`, `progress`, `finished` or `decision_read`. | nobody |
| `activity` | Summarise activity: counts, sessions, who is stuck, who has not acknowledged. | nobody |

"The person" means Claude Code's permission prompt. It is used only in the permission modes
where a person answers it (`default`, `acceptEdits`, `plan`); in every other mode the call is
refused. See [Security model](security.md).

## Hooks

| Claude Code event | What dumen does |
| --- | --- |
| `SessionStart` | Injects the block of team records: active directives first, then the approved decisions that apply, links and the role. Logs `started`. |
| `UserPromptSubmit` | Injects directives that arrived since the last prompt. |
| `PreToolUse` (every tool) | Hands over new directives, blocks `Edit`, `Write`, `MultiEdit` and `NotebookEdit` under a `deny` directive, sends human actions to the permission prompt, and blocks shell commands that would take a human action. Reminds the agent once about an unacknowledged directive. |
| `Stop` | Logs `progress` with a one-line summary of the agent's last message. When a deny directive is active, also looks at the work tree (`git status`, cut off after 2 seconds) and logs a `violation` for changes made under it since the session began. |
| `SessionEnd` | Logs `finished`. |

Hooks answer with `hookSpecificOutput`. A blocked edit looks like this:

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Blocked by team directive Y-2APW (\"Auth is frozen until the audit ends\", active until 2026-11-24 00:34). This path is frozen for every agent. Do not work around it with another tool. Ask your human."}}
```

Hooks read a local cache, so they answer in about 4 ms (p95, budget 50 ms). When the cache is
missing or older than 90 seconds, a hook tries one fetch that is cut off after 2 seconds, at
most once every 30 seconds. A hook that fails for any reason lets the tool call through.

## Data layout

**The store** is a branch (default `dumen`) that shares no history with your code:

```
dumen.yml                     roles, areas, settings, schema version
directives/Y-2APW-<slug>.md   one file per directive
decisions/D-PG18-<slug>.md    one file per decision
refs/                         one file per link
activity/<date>/<session>.jsonl   one line per event
DECISIONS.md, ACTIVITY.md     generated, readable in your git host's web UI
```

Every write is fetch, apply, commit, fast-forward-only push. If somebody else pushed first,
dumen fetches again and re-applies the operation on the new head, up to five times. Text is
never merged.

**Local state** lives in `~/.dumen/<project>/`, never inside your working tree:

```
store/           a clone of the store branch
cache.json       the snapshot hooks read
outbox/          operations waiting for the next sync (offline work)
outbox-failed/   operations the store rejected
sessions/        per-session state
```

**`.dumen`** in your code repository tells teammates where the store is. It holds no secrets:

```
project = shop
store = origin#dumen
```

## Troubleshooting

Start with `dumen status`. It tells you whether the remote is reachable and what git said if
not, which records are unreadable, and what is still queued. Then run `dumen sync`: if your git
host refuses the push (a protected branch, a missing permission, a rule that commits must be
signed), it prints the host's own message.

If `dumen status` keeps reporting an error that mentions `index.lock`, a dumen process was
killed in the middle of a write. Delete `~/.dumen/<project>/store/.git/index.lock`.

## Development

```sh
git clone https://github.com/nurettincoban/dumen && cd dumen
go build ./cmd/dumen
go test ./...                       # unit, concurrency (6 writers, 1,000 operations), latency
go test ./... -short                # the same, faster
go test -tags e2e ./test/e2e        # real `claude -p` sessions; needs Claude Code signed in
```

The layout:

```
cmd/dumen            entry point
internal/model       record types, ids, validation
internal/store       files on the orphan branch, git plumbing
internal/op          every write as a replayable operation, and the push/retry engine
internal/outbox      queued operations for offline work
internal/policy      scopes, path matching, supersede graph
internal/cache       the local snapshot hooks read
internal/hook        Claude Code hook handlers
internal/mcpserver   MCP tools and the background sync loop
internal/cli         commands
plugin/              the Claude Code plugin
test/                concurrency, latency and end-to-end suites
```
