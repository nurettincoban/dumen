# Using dumen

You use dumen by talking to Claude Code. Your agent runs the `dumen` commands and calls the
dumen tools; you answer its questions and confirm the permission prompts of the human
actions. Each section below shows what you say, what the agent does, and what you confirm.
The commands and tools themselves are listed in the [Reference](reference.md), as the
agent's interface.

- [Set up the store](#set-up-the-store)
- [Connect Claude Code](#connect-claude-code)
- [The team](#the-team)
- [Decisions](#decisions)
- [Directives](#directives)
- [Sources of truth](#sources-of-truth)
- [Monitoring](#monitoring)
- [Several repositories, one team](#several-repositories-one-team)
- [Privacy](#privacy)
- [Check a change in CI](#check-a-change-in-ci)
- [claude.ai on the web](#claudeai-on-the-web)

## Set up the store

The store is where the records live. We recommend a small, private repository of its own.
Create an empty one on your git host, then, in your code repository:

> Set up dumen for our team. The store is git@git.example.com:team/dumen-store.git#dumen and
> the project is called shop.

The agent runs `dumen init` from its shell, which creates the branch `dumen` in the store
with you as lead and writes a small `.dumen` file telling teammates where the store is (it
holds no secrets). It then offers to commit `.dumen`. Teammates, in their clone:

> Join our dumen project.

The agent runs `dumen join`. To try dumen quickly you can leave the store out; the records
then go to an orphan branch named `dumen` in the code repository itself. Two reasons to
prefer a separate repository: every activity event is a small commit, which a busy team
would otherwise carry in every `git fetch` of the code; and the store holds your teammates'
email addresses and one-line summaries of what their agents did. **Never keep the store in
a public repository.**

## Connect Claude Code

**The plugin (recommended).** Each developer, once, inside Claude Code:

```text
/plugin marketplace add nurettincoban/dumen
/plugin install dumen@dumen
```

The plugin brings the hooks, the MCP server and the skill; it needs the `dumen` binary on
`PATH`. It stays silent in directories that have nothing to do with a dumen project. The
first time an agent uses a dumen tool, Claude Code asks for permission: choose "always
allow" for the reading, acknowledging and drafting tools. Never pre-approve the human
actions (`approve`, `reject`, `supersede`, `publish_directive`, `withdraw_directive`,
`set_role`, `set_product`, `check_ref`, `set_private`): their prompt is your confirmation.

**Files in the repository, instead.** Ask your agent to "install dumen into this
repository". It runs `dumen install`, which writes `.claude/settings.json`, `.mcp.json` and
`.claude/skills/dumen/`, and offers to commit them. Nobody installs a plugin then, but every
developer still needs the binary. Use one option or the other: with both, every hook runs
twice.

## The team

> Add ayse@example.com to the team as a dev, and mehmet@example.com as a lead.

The agent calls `set_role` once per person. The prompt says what happens, for example
`It gives ayse@example.com the role "dev"`. People are named by their git `user.email`.

Everybody with a role may approve and change decisions: they are binding but changeable,
and every change reaches the other agents as news. People with the role `lead` or `pm` may
also publish directives. Agents may read, draft and propose, never approve.

## Decisions

A decision is one sentence that every agent follows, with a kind and a scope. The examples
below are from a table-reservation product with `booking-svc`, `billing-svc` and `web-app`.

| Kind | Example rule |
| --- | --- |
| `product` | A booking is held for 10 minutes while the guest pays |
| `ux` | A guest can cancel without signing in, from the link in the confirmation email |
| `ui` | Guests pick a time from slot buttons grouped by part of day |
| `architecture` | Only billing-svc talks to the payment provider |
| `technical` | Creating a booking is idempotent with an Idempotency-Key header |

### Most decisions start as a question

Your agent asks you "A or B?" while it works, you answer, and the agent saves the answer
with `record_answer`: the question, the option you picked, a one-sentence rule, its kind,
services and topics. It then offers to put it in force. Say yes and confirm the prompt, which
quotes the rule; from then on it binds every agent. Agents in the services it concerns get it
as `DECISION NEWS` on their next step, in the session they are running: each session checks
the store about every 10 seconds, and an agent about to end its turn is held once to read the
news.

When an agent is about to ask something an approved decision already answers, dumen holds
the question back once and shows the decision. If it does not fit, the agent asks again and
the question goes through. Agents also look on their own before asking (`decisions_for_topic`).

### Decisions you state yourself

> Record an architecture decision: only billing-svc talks to the payment provider. Why: one
> place for PCI scope and refunds. Not allowed: calling the provider's SDK from booking-svc.
> Put it in force.

The agent drafts it with `draft_decision` and calls `approve`; you confirm. A lasting choice
the agent made without asking you (a library, an API shape) is drafted the same way and stays
proposed until you approve it.

### When a decision needs work from another service

> The slot buttons should show how many tables are left. The backend needs to return that
> per slot in GET /slots.

The agent adds `needs` (`{"booking-svc": "Return remaining tables per slot in GET /slots"}`).
The approve prompt says `It asks booking-svc to: ...`, and the agents working on `booking-svc`
get `NEEDS FROM booking-svc: ...` in the session they are running. A real session of exactly
this is in the [README](../README.md#real-sessions).

### Changing a decision

> D-7K2P is wrong for our accounting: refunds go back to the card. Change it.

The agent calls `supersede`; the prompt says `It retires D-7K2P ... and puts in force ...`.
Every agent on the topic hears "D-9QXA ... replaces D-7K2P".

### What changed, what is open

> What decisions changed this week?
> Which proposed decisions are waiting for an approval?

The agent calls `news` and `search_decisions` with status `proposed`. It can show any
decision in full with `get_decision`, and approve or reject one when you say so.

### Who may approve

Anyone with a role. The safeguard is that everyone hears about every change. A team that
wants a gate for a kind asks its agent to set an owner role, for example "only leads approve
architecture decisions" (`set_product` with `kinds`). Proposed decisions are never shown to
other agents as facts, only as a count, and the ones nobody approves are archived after seven
days.

### Products, services and topics

> Our product is Table reservations. Services: booking-svc creates, changes and cancels
> bookings; billing-svc does payments and refunds; web-app is the guest-facing site. Topics:
> payments (refund, charge, card, deposit) and booking-flow (booking page, time picker, slot).

The agent calls `set_product`; the prompt lists every name, service and topic it sets. These
words let dumen match decisions to prompts, plans and questions. A topic need not be
declared: its own name is a keyword. Matching is by words, not meaning.

## Directives

A directive is a short-lived instruction to agents, either `context` (agents are told) or
`deny` (agents are told, and edits under its scope are blocked). It is scoped to everyone, a
role, an area or path patterns, and it always expires, after seven days unless you say
otherwise.

> Tell every agent: auth is frozen until the audit ends, for a week. Block edits in src/auth.

A PM or lead says this; the agent calls `publish_directive`, and the prompt says
`It tells paths src/auth/** [deny]: "..."`. Anyone's agent can *propose* a directive with
`propose_directive`; a PM or lead puts it in force. "End the auth freeze" withdraws it
(`withdraw_directive`).

Each agent acknowledges a directive once per session; an agent that has not is reminded once.
Ask your agent who has not acknowledged yet.

## Sources of truth

Designs usually live in Claude Design and documents in Claude Artifacts. Point at them
instead of copying them:

> Link the checkout design (paste the Claude Design link) as the source of truth for the
> checkout screens.

The agent calls `add_ref` with the link, a one-line title and what it governs. A link added
by an agent is unverified and not shown to any agent until a person confirms it: the agent
offers `check_ref`, and the prompt shows the link's title and URL. dumen stores the link and a
one-line label, never the content, and never fetches a URL.

## Monitoring

> What did the team's agents do today? Who is stuck?

The agent calls `activity`: counts, finished sessions with their last note, sessions blocked
or waiting for a person, and who has not acknowledged each directive.

## Several repositories, one team

Teams with microservices keep one store for all of their repositories. In each service, ask
the agent to set dumen up with the same store and project; it runs `dumen init --store ...`
there, and the repository's name comes from its `origin` remote. Paths can then name a
repository:

| Pattern | Matches |
| --- | --- |
| `src/auth/**` | that path in every repository |
| `orders:src/db/**` | that path in the `orders` repository only |
| `orders:**` | everything in `orders` |

A decision can name the services it applies to; agents in a service see that service's
decisions and the product-wide ones. The rules that apply to an edit are those of the
repository the file is in, so a session opened in `gateway` cannot edit a frozen path in
`../orders`.

## Privacy

> Don't monitor my sessions on this machine for now.

The agent calls `set_private`; the prompt says it turns monitoring off. The fact that a
session is private is itself recorded, so the team sees "a private session ran", and nothing
else. Directives and decisions still apply.

Activity events hold a kind, a one-line summary, file paths, the repository name and the
commit the session started from. They never hold prompts or tool output. The summary is the
first paragraph of the agent's last message with code removed, cut to one line. Every write
is scanned for common secret formats and is rejected when one matches.

## Check a change in CI

A hook can stop Claude Code's file tools, but not a shell command that rewrites a file. A
diff shows every change, whatever made it. In CI, which is a machine and not a person,
`dumen check` fails when a change touches a path under an active deny directive:

```sh
git config user.email ci@example.com
dumen join
dumen check "$BASE_SHA..$HEAD_SHA"
```

```text
src/auth/login.go  Y-PBG4  Auth is frozen until the audit ends (until 2026-11-24 10:51)
dumen: 1 of 3 changed files in origin/main..HEAD is under a deny directive
```

A range is checked the way a pull request shows it. It exits with 0 when nothing is found,
1 when a change is under a deny directive, and 2 when the check could not be made. The job
needs the binary, read access to the store and enough history to find where the branch
forked (most CI systems make a shallow clone by default). Running sessions do the same check
at the end of each turn and report a change under a deny directive in the activity log.

## claude.ai on the web

dumen builds nothing for claude.ai, but because the store is plain files on a branch, a web
session can read it through your git host's own connector:

- **GitLab:** add a custom connector in claude.ai with the URL
  `https://<your-gitlab-host>/api/v4/mcp` and sign in with OAuth.
- **GitHub:** connect the GitHub connector in claude.ai and give it access to the repository
  that holds the store.

Claude can then read `DECISIONS.md`, `ACTIVITY.md` and the individual records on the `dumen`
branch. Nothing is enforced in web sessions, and you should not write to the store from there.
