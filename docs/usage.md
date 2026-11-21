# Using dumen

Day-to-day use, for people. Agents do most of this on their own through the MCP tools; every
command here is also something you can simply ask your Claude to do.

- [Set up the store](#set-up-the-store)
- [Connect Claude Code](#connect-claude-code)
- [Directives](#directives)
- [Decisions](#decisions)
- [Sources of truth](#sources-of-truth)
- [Monitoring](#monitoring)
- [Check a change](#check-a-change)
- [Several repositories, one team](#several-repositories-one-team)
- [Privacy](#privacy)
- [claude.ai on the web](#claudeai-on-the-web)

## Set up the store

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

## Connect Claude Code

**Option A, the plugin (recommended).** Each developer runs this once inside Claude Code:

```
/plugin marketplace add nurettincoban/dumen
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
never pre-approved. Claude Code applies these permissions only after you have opened the repository once and
accepted its trust dialog; until then it asks.

Use one option or the other. With both, every hook runs twice and the activity log gets duplicate
events.

## Directives

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
once, on its next tool call. `dumen activity` lists who has not acknowledged yet. For a directive
limited to some repositories, only people whose agents worked there are listed.

## Decisions

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

## Sources of truth

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

## Monitoring

```sh
dumen activity                         # today: counts, sessions, who is stuck, who has not acked
dumen activity --since 7d --person ayse
dumen activity --kind blocked --events
dumen activity --json                  # for scripts
dumen watch                            # live tail
```

## Check a change

A hook can stop Claude Code's file tools, but not a shell command that rewrites a file. A diff
shows every change, whatever made it. `dumen check` fails when a change touches a path under an
active deny directive:

```sh
dumen check                    # the work tree against HEAD, for a pre-commit hook
dumen check origin/main..HEAD  # a commit range, for CI or a pre-push hook
```

```text
src/auth/login.go  Y-PBG4  Auth is frozen until the audit ends (until 2026-11-24 10:51)
dumen: 1 of 3 changed files in origin/main..HEAD is under a deny directive
```

A range is checked the way a pull request shows it: what `HEAD` changed since it forked from
`origin/main`. Commits that `origin/main` gained in the meantime do not count against the
branch. `a..b` and `a...b` mean the same here.

It prints `OK: ...` and exits with 0 when nothing is found, exits with 1 when a change is under
a deny directive, and with 2 when the check could not be made (an unknown commit, no common
ancestor). It applies the deny directives that are active at the moment it runs.

To run it in CI, on any git host, the job needs the `dumen` binary, read access to the store,
a git identity, and enough history to find where the branch forked. Most CI systems make a
shallow clone by default: set the fetch depth to the full history, or run
`git fetch --unshallow` first.

```sh
git config user.email ci@example.com
dumen join
dumen check "$BASE_SHA..$HEAD_SHA"
```

Running sessions do the same check on their own work tree at the end of each turn. A change
under a deny directive shows up in `dumen activity` under "Changes under a deny directive". The
session is not stopped and nothing is undone.

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

## claude.ai on the web

dumen builds nothing for claude.ai, but because the store is plain files on a branch, a web
session can read it through your git host's own connector:

- **GitLab:** add a custom connector in claude.ai with the URL
  `https://<your-gitlab-host>/api/v4/mcp` and sign in with OAuth.
- **GitHub:** connect the GitHub connector in claude.ai and give it access to the repository
  that holds the store.

Claude can then read `DECISIONS.md`, `ACTIVITY.md` and the individual records on the `dumen`
branch. Nothing is enforced in web sessions, and you should not write to the store from there.
