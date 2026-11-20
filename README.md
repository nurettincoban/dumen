# dumen

**Steer and monitor every Claude Code agent on your team, through the git remote you already have.**

Each developer on a team now runs several Claude Code agents. Those agents do not know what the
team decided yesterday, that `src/auth` is frozen for the audit, or that another agent is stuck on
the same problem. dumen (Turkish for *rudder*) gives the team three things:

- **Directives.** "Do not touch `src/auth/**` until Friday." Published by a lead, delivered to
  running sessions within about 30 seconds, acknowledged by each agent, and, for `deny`
  directives, enforced by a hook that blocks the edit.
- **Shared decisions.** "We use Postgres for the order service, because we need joins." Agents see
  the approved decisions that govern the files they work on. Agents can draft a decision; only a
  human can approve one.
- **An activity log.** Who started what, who finished, who is blocked, who is waiting for a human,
  and who has not yet acknowledged a directive.

There is no server, no account and no token. dumen is one static Go binary that shells out to your
system `git` and keeps its records on an orphan branch (`dumen`) of a repository your team can
already push to: GitHub, GitLab, Gitea, Bitbucket or a bare repository on a server. dumen only
ever speaks plain git to it, so the host makes no difference. If `git push` works, dumen works.

```
$ dumen direct --deny --paths 'src/auth/**' --for 3d "Auth is frozen until the audit ends"
Published Y-G300 [deny] paths src/auth/**, until 2026-11-23 23:32
```

Within about thirty seconds, a teammate's running Claude Code session that tries to edit
`src/auth/login.go` is stopped, and the agent is told why:

```
Blocked by team directive Y-G300 ("Auth is frozen until the audit ends", active until
2026-11-23). This path is frozen for every agent. Do not work around it with another tool.
Ask your human.
```

And on your side:

```
$ dumen activity
Since 2026-11-20 00:00
started 2  finished 0  blocked 1  asking_human 0  progress 1  directive_ack 0  draft_written 0  decision_read 0  private 0

Sessions (2)
  ayse@example.com                   3f9a1c2e   blocked       23:32-23:32  edit blocked: src/auth/login.go
  mehmet@example.com                 8c41d07b   active        23:32-23:33  I added pagination to the orders endpoint and its tests pass.

Stuck (1)
  ayse@example.com         3f9a1c2e   blocked       edit blocked: src/auth/login.go

Not yet acknowledged
  Y-G300 Auth is frozen until the audit ends
    ayse@example.com, mehmet@example.com
```

> **Status: early, not released yet.** The MVP is feature complete and tested on Linux, including
> end-to-end tests against the real `claude` CLI. It has not been run on macOS or Windows and has
> not been used by many teams. Read [Honest limits](#honest-limits) before you rely on it.

## Contents

- [How it works](#how-it-works)
- [Five-minute setup](#five-minute-setup)
- [Daily use](#daily-use)
- [Several repositories, one team](#several-repositories-one-team)
- [What an agent sees and can do](#what-an-agent-sees-and-can-do)
- [Privacy](#privacy)
- [Security model](#security-model)
- [Honest limits](#honest-limits)
- [claude.ai on the web](#claudeai-on-the-web)
- [Command reference](#command-reference)
- [Development](#development)

## How it works

```
   lead's laptop                 your git remote                 developer's laptop
 ┌───────────────┐           ┌────────────────────┐           ┌──────────────────────┐
 │ dumen direct  │──push────▶│  branch: dumen     │◀──fetch───│ dumen mcp (30 s loop)│
 │ dumen activity│◀──fetch───│  (records only,    │──push────▶│ hooks read a local   │
 └───────────────┘           │   never your code) │           │ cache, in about 4 ms │
                             └────────────────────┘           └──────────────────────┘
```

- **Storage** is an orphan branch named `dumen`. It shares no history with your code and holds
  only small YAML, Markdown and JSON Lines files: `dumen.yml` (roles, areas), `decisions/`,
  `directives/`, `activity/`, `refs/`, plus generated `DECISIONS.md` and `ACTIVITY.md` that
  anyone can read in the web UI of your git host.
- **Writes never merge text.** Every write is fetch, apply, commit, fast-forward-only push. If
  somebody else pushed first, dumen fetches again and re-applies the operation on the new head, up
  to five times. If you are offline, the operation waits in a local outbox and is replayed by the
  next sync.
- **No daemon.** Claude Code starts `dumen mcp` as an MCP server for each session. While the
  session lives, that process checks the remote every 30 seconds with `git ls-remote` and fetches
  only when something changed.
- **Hooks read a local cache.** That keeps them fast (p95 about 4 ms, budget 50 ms). The one
  exception: when the cache is missing or older than 90 seconds, a hook tries a single fetch that
  is cut off after 2 seconds, at most once every 30 seconds. A hook that fails for any reason lets the
  tool call through.
- **Your code stays on your machine.** dumen never copies code. It records paths, one-line labels
  and links. Local state lives in `~/.dumen/<project>/`, never inside your working tree.

The full design, with the reasons behind each choice, is in [docs/design.md](docs/design.md).

## Five-minute setup

You need `git` and Claude Code. dumen uses your existing SSH keys or credential helper.

### 1. Install the binary (everyone)

```sh
go install github.com/nurettincoban/comm-pro/cmd/dumen@latest
```

Prebuilt binaries for macOS, Linux and Windows, a Homebrew cask and a Scoop manifest are produced
by [goreleaser](.goreleaser.yaml) and will be published with the first tagged release.

### 2. Create the store (one person, once)

The store is where the records live. We recommend a small, private repository of its own: create
an empty one on your git host, then point your code repository at it.

```sh
cd your-repo
dumen init --store git@git.example.com:team/dumen-store.git#dumen
git add .dumen && git commit -m "Add dumen" && git push
```

To try dumen quickly you can leave `--store` out. The records then go to an orphan branch named
`dumen` in the code repository itself:

```sh
dumen init              # creates the orphan branch "dumen" on origin, with you as lead
```

Two reasons to prefer a separate repository. Every activity event is a small commit, so a busy
team adds thousands of commits a day that every `git fetch` of the code repository would carry.
And the store holds your teammates' email addresses and one-line summaries of what their agents
did: **never keep the store in a public repository.**

`.dumen` is a small text file that tells teammates where the store is. It holds no secrets.

Add your teammates by their git `user.email`:

```sh
dumen role set ayse@example.com dev
dumen role set mehmet@example.com lead
dumen role list
```

Out of the box, people with the role `lead` or `pm` may publish directives and approve decisions.
Everybody else, and every agent, may read, draft and propose.

### 3. Connect Claude Code (pick one)

**Option A, the plugin (recommended).** Each developer runs this once inside Claude Code:

```
/plugin marketplace add nurettincoban/comm-pro
/plugin install dumen@dumen
```

The plugin brings the hooks, the MCP server and the skill. It stays silent in directories that
have nothing to do with a dumen project. The first time an agent uses a dumen tool, Claude Code asks for permission;
choose "always allow" for the reading and acknowledging tools. Never pre-approve `approve`,
`supersede` or `publish_directive`: those are your confirmation step.

**Option B, files in the repository.** One person runs this and commits the result. Nobody has to
install a plugin, but every developer still needs the binary on `PATH`.

```sh
dumen install           # writes .claude/settings.json, .mcp.json, .claude/skills/dumen/
git add .claude .mcp.json && git commit -m "Wire dumen into Claude Code" && git push
```

`dumen install` also pre-approves the dumen tools an agent may always use (reading, acknowledging,
drafting), so sessions are not interrupted by permission prompts. The three human actions are
never pre-approved.

Use one option or the other. With both, every hook runs twice and the activity log gets duplicate
events.

### 4. Join (every other developer, once per machine)

```sh
git pull
dumen join
dumen status
```

That is all. Start Claude Code as usual.

## Daily use

### Directives

```sh
dumen direct --all "Prefer small pull requests this week"
dumen direct --role dev --for 12h "Run the integration tests before you finish"
dumen direct --deny --paths 'src/auth/**,migrations/**' --until 2026-12-01 "Frozen for the audit"
dumen direct --area payments "The payments API contract is frozen, see R-N3WS"
dumen directives            # what is active
dumen withdraw Y-MXE3
```

A directive is either `context` (agents are told) or `--deny` (agents are told, and edits under
the scope are blocked). It is scoped to everyone, a role, an area or path patterns, and it always
expires (seven days unless you say otherwise). Anyone, including an agent, can *propose* a
directive with `--propose`; a publisher puts it in force with `dumen direct --publish <id>`.

Each agent acknowledges a directive once per session. An agent that has not done so is reminded
once, on its next tool call. `dumen activity` lists who has not acknowledged yet.

### Decisions

```sh
dumen decide --title "Use Postgres for the order service" --paths 'src/orders/**' \
  -m "We need joins and transactions. Rejected: DynamoDB (no joins), SQLite (single writer)."
dumen decisions --drafts
dumen approve D-PG18        # or: dumen reject D-PG18
dumen supersede D-PG18 --title "Move orders to CockroachDB" -m "..."
dumen decisions src/orders/handler.go     # which decisions govern this file?
dumen show D-PG18
```

Decisions start as drafts. Drafts are never injected into agent context as facts; agents only see
how many are waiting. Drafts nobody approves are archived after seven days by `dumen gc`.

### Sources of truth

Designs usually live in Claude Design and documents in Claude Artifacts. Point at them instead of
copying them:

```sh
# a design, from Claude Design
dumen ref add "<Claude Design link>" --kind design --title "Checkout flow v3" \
  --governs "checkout screens"

# a document (spec, plan, API contract), from a Claude Artifact
dumen ref add https://claude.ai/artifact/abc123 --kind artifact --title "Payments API contract" \
  --governs "request and response shapes of the payments API" --paths 'src/payments/**'

dumen ref list
dumen ref check R-7GE1      # "I looked, the link is right"
```

An agent can add a link too, but a link added by an agent is marked unverified and is not shown
to any agent until a person confirms it with `dumen ref check`. Text written by one agent never
reaches the others without a person in between.

Other kinds are `mr` (a merge or pull request) and `doc` (any other document). An agent that
edits a file under `--paths` is shown the link and its label. dumen stores the link and a one-line
label, never the content, and never fetches a URL on its own.

### Monitoring

```sh
dumen activity                         # today: counts, sessions, who is stuck, who has not acked
dumen activity --since 7d --person ayse
dumen activity --kind blocked --events
dumen activity --json                  # for scripts
dumen watch                            # live tail
```

## Several repositories, one team

Teams with microservices keep one store for all of their repositories. Create it once, in any
repository you like (a small dedicated one works well), and point every service at it:

```sh
# in each service repository
dumen init --store git@git.example.com:team/dumen-store.git#dumen --repo orders
git add .dumen && git commit -m "Add dumen" && git push
```

The repository name defaults to the name of the `origin` remote. Paths in directives, decisions
and refs can then name a repository:

| Pattern | Matches |
| --- | --- |
| `src/auth/**` | that path in every repository |
| `orders:src/db/**` | that path in the `orders` repository only |
| `orders:**` | everything in `orders` |

```sh
dumen direct --deny --paths 'orders:migrations/**' "Schema is frozen until the release"
dumen activity --repo orders
```

One `dumen activity` shows the sessions of every service. The rules that apply to an edit are
those of the repository the file is in, so a session opened in `gateway` cannot edit a frozen
path in `../orders`, and a session opened in the directory that holds all the repositories gets
the directives of all of them.

## What an agent sees and can do

At session start and on each prompt, the agent receives a short block: active directives, the
approved decisions that apply, links to sources of truth and its role. Only ids and one-line
labels are injected. Bodies are fetched on demand through MCP tools, so the context cost stays
small. A directive published while a session is running reaches the agent on its next tool call.

| An agent can | An agent cannot |
| --- | --- |
| read directives, decisions, refs and activity | approve, reject or supersede a decision |
| acknowledge a directive | publish or withdraw a directive |
| draft a decision | change roles |
| propose a directive | mark a session private |
| add a link (unverified until a person checks it) | confirm a link |
| log progress or a blocker | edit the store or the `.dumen` file directly |

How the human-only actions are kept human:

- When an agent calls `approve`, `supersede` or `publish_directive`, a hook sends the call to
  Claude Code's permission prompt. The action happens only if you accept it there. The dumen
  server acts on such a call only if it came through that hook, and only the server that Claude
  Code itself started for the session can do so; a second `dumen mcp` started from a shell
  cannot.
- The prompt is used only in the permission modes where a person answers it (`default`,
  `acceptEdits`, `plan`). In every other mode the call is refused, and you run the command in
  your own terminal.
- A hook blocks shell commands such as `dumen approve` inside a session, and the CLI refuses
  human actions when it sees that it runs inside Claude Code. These two are a second layer, not a
  wall: an agent that deliberately clears its environment can get past them. See
  [Security model](#security-model).

## Privacy

Monitoring is visible and optional per session.

```sh
dumen private on      # your new sessions stop sending activity
dumen private off
```

The fact that a session is private is itself recorded, so the team sees "a private session ran",
and nothing else. Directives still apply to private sessions.

Activity events hold a kind, a one-line summary, file paths, the repository name and the commit
the session started from. They never hold prompts or tool output. The summary is the first
paragraph of the agent's last message with code blocks and long code spans removed, cut to one
line. Every write is scanned for common secret formats (cloud keys, git host tokens, private keys
and others) and is rejected when one matches.

## Security model

Please read this part before you trust dumen with anything.

**dumen is a guard rail, not a security boundary. It guards against agents, not against
colleagues.** Anyone who can `git push` to the store
branch can write any record, including one that says somebody else approved it. dumen cannot
prevent that, because there is no server to check identities. It is the same trust you already
place in people who can push to your repository.

The same is true, to a lesser degree, of a determined agent. It runs on your machine with your
credentials, so nothing local can tell it from you with certainty. dumen makes the wrong thing
hard to do by accident and visible when it happens; it does not make it impossible.

What you do get:

- Roles are checked in the CLI, the MCP server and the hooks, so an agent that plays by the
  rules, or that is merely confused or misled by something it read, cannot promote its own ideas
  into team decisions or publish instructions to other agents.
- Text from the store is injected into agent context as data with a clear header, as ids and
  sanitized one-line labels, never as free-form instructions.
- Every record is a git commit. `git log` on the `dumen` branch is the audit trail.

What we recommend:

- Protect the `dumen` branch on your git host: allow pushes from team members, forbid force
  pushes and deletion.
  On GitHub that is a branch protection rule or ruleset for `dumen`; on GitLab it is
  Settings > Repository > Protected branches. Do not require pull requests or status checks on
  it: dumen pushes directly.
- Do not put a CI configuration on the store branch. `dumen init` and `dumen join` refuse a
  branch that holds one (`.gitlab-ci.yml`, `.github/workflows`, `bitbucket-pipelines.yml`,
  `Jenkinsfile` and the like), so that dumen writes never start pipelines.

## Honest limits

- **30-second sync window.** A new directive reaches running sessions within about 30 seconds, not
  instantly. An agent can start an edit in that window.
- **`deny` covers Claude Code's file tools, not everything.** The hook blocks `Edit`, `Write`,
  `MultiEdit` and `NotebookEdit` under a denied path, following symlinks. An agent that runs
  `sed -i`, a build script or a code generator through Bash, or that writes through another MCP
  server's file tool, can still change those files. The directive text tells the agent not to,
  and agents nearly always comply, but it is not a sandbox. Use branch protection and code review
  for hard guarantees.
- **Not run on macOS or Windows yet.** It compiles for both, and nothing more has been checked.
  Expect rough edges there, for example a credential manager window opening during a background
  sync on Windows.
- **Activity is one commit per event.** The store grows by roughly 1 KB per event, and its local
  clone under `~/.dumen` is never compacted automatically. Run `git gc` there now and then, and
  keep the store in its own repository.
- **A crashed dumen can leave a lock behind.** If a `dumen` process is killed in the middle of a
  write, other dumen commands on that machine wait up to two minutes. If `dumen status` then
  keeps reporting an error that mentions `index.lock`, delete
  `~/.dumen/<project>/store/.git/index.lock`.
- **Subagents share their parent's session.** A directive that arrives mid-session is shown once
  per session. If a subagent's tool call happens to receive it, the main agent only sees it at
  its next prompt.
- **Large teams of records.** The block injected at session start has no size cap yet. With many
  directives, decisions and links it can pass the size at which Claude Code shows the agent only
  a preview. Keep directives few (the default limit is 10) and time-boxed.
- **Two unrelated repositories with the same project name** in their `.dumen` files are treated
  as one project on your machine. Choose distinct project names.
- **Advisory security.** See [Security model](#security-model). A colleague with push access can
  forge records.
- **Hooks fail open.** If the binary is missing, the cache is unreadable or anything else goes
  wrong, the tool call proceeds. dumen should never be the reason an agent cannot work.
- **Claude Code only.** Hooks and local sessions are what make delivery and enforcement work.
  Other tools can read the store, but nothing is enforced there.
- **Small teams.** The design targets about 2 to 20 people. Every write is a git push; that is
  fine at this scale and would not be at 500 people.
- **No claims or locks yet.** "I am working on this file" is planned, not built.
- **Non-technical teammates.** Today everyone needs git access to the store. A friendlier path for
  product managers without a git account is not built yet. The generated `DECISIONS.md` and
  `ACTIVITY.md` can be read in the web UI of your git host.
- **Old and new versions.** A store upgraded by a newer dumen becomes read-only for older ones
  until they upgrade; they say so and write nothing.
- **Continuous integration** for this repository is not set up yet. Tests run locally, on Linux.

## claude.ai on the web

dumen builds nothing for claude.ai, but because the store is plain files on a branch, a web
session can read it through your git host's own connector:

- **GitLab:** add a custom connector in claude.ai with the URL
  `https://<your-gitlab-host>/api/v4/mcp` and sign in with OAuth.
- **GitHub:** connect the GitHub connector in claude.ai and give it access to the repository
  that holds the store.

Claude can then read `DECISIONS.md`, `ACTIVITY.md` and the individual records on the `dumen`
branch. Nothing is enforced in web sessions, and you should not write to the store from there.

## Command reference

| Command | What it does |
| --- | --- |
| `dumen init [--store URL#branch] [--repo NAME]` | create the store, write `.dumen` |
| `dumen join [URL]` | connect this machine to the store |
| `dumen status` | sync state, your role, queued operations |
| `dumen sync [--forget]` | push queued operations and refresh the cache now; `--forget` clears the list of rejected ones |
| `dumen role set\|rm\|list` | manage who is on the team |
| `dumen direct ...`, `direct --propose ...`, `direct --publish ID` | publish a directive, propose one, put a proposal in force |
| `dumen directives [--all]`, `withdraw ID` | list and withdraw directives |
| `dumen decide`, `decisions`, `show`, `approve`, `reject`, `supersede` | work with decisions |
| `dumen ref add\|check\|list` | links to sources of truth |
| `dumen activity`, `watch` | what agents did, and a live tail |
| `dumen private [on\|off]` | stop or resume monitoring of your new sessions |
| `dumen gc` | archive stale drafts, compact activity older than 30 days |
| `dumen install` | write hooks, MCP registration and skill into a repository |
| `dumen mcp`, `dumen hook <event>` | entry points for Claude Code, not for people |
| `dumen version`, `dumen help` | the version, the command list |

Every command accepts `--help`.

Environment variables: `DUMEN_HOME` moves local state away from `~/.dumen`. `DUMEN_POLL_INTERVAL`
changes the 30-second sync interval (for example `10s`). `DUMEN_WORKSPACES` is where
`dumen join <url>` creates a workspace (default `~/dumen`).

When something goes wrong, start with `dumen status`: it tells you whether the remote is
reachable and what git said if not, which records are unreadable, and what is still queued. Then
run `dumen sync`: if your git host refuses the push (a protected branch, a missing permission, a
rule that commits must be signed), it prints the host's own message.

## Development

```sh
git clone https://github.com/nurettincoban/comm-pro && cd comm-pro
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

Contributions are welcome. Please keep three rules in mind: hooks stay fast and fail open, a
write must never merge text, and nothing may copy code or secrets into the store. Open an issue
before a large change so we can agree on the direction first.

## License

[Apache-2.0](LICENSE)
