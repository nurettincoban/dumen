# The dumen protocol

**Live spec-driven development.** A team writes the spec and the plan before the work starts,
as in spec-driven development. But most questions only come up while the work is being done:
"slot buttons or a calendar?", "refund to the card or store credit?". dumen keeps those
answers in the same place as the spec, so the spec stays current while the work goes on. The
spec written up front, the sources it rests on (designs, Artifacts, Confluence pages) and the
decisions taken along the way form one **living spec** in git. Every agent on the team works
from its latest version, and hears about a change while it is still working.

This document is the protocol. The transport is git and the records are markdown files.
dumen's own command is one implementation: any tool that follows this document can read and
write a store, such as a script, a CI job, or a future Slack or email adapter for a PM.

Version 1. MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

## 1. The store

The store is one branch of a git repository that the whole team can read and push to.

```text
dumen.txt          "version: 1": the protocol version of the store
.gitattributes     "* text=auto eol=lf"
spec/              the spec written up front, shared by the repositories
  product.md         the product, its services and its words
  plan.md            the plan of the current release
sources/           sources of truth kept with a summary
  checkout-design.md
decisions/         one decision or question per file
  D-7K2P.md
```

**Where it is.** A code repository finds its store in this order:

1. `git config dumen.store` (and `dumen.service`) in that clone;
2. a committed `.dumen` file in its root, with the lines `store: <url>#<branch>` and
   `service: <name>`;
3. a branch named `dumen` on the repository's own `origin`.

So a product in one repository needs no configuration at all: the store is the `dumen` branch
of the code's own remote, and a teammate joins by installing the plugin and fetching. A
product of several repositories shares one store repository, named with `git config` or a
committed `.dumen`. A store is written `<git url>#<branch>`; the branch is `dumen` unless
named. The service is the repository's name unless set.

**Locally**, an implementation keeps its copy of the store apart from the work tree, the index
and the branches of the code repository. dumen clones it into `.git/dumen/store` and keeps its
own state (marks, lock, tickets) in `.git/dumen/state`. It never runs a command that changes the
code repository: it runs git with the `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and other
repository variables of its environment removed, and checks that git really is in the store's
repository before anything that changes files. A branch named `dumen` counts as a store only
when it holds `dumen.txt`.

**Privacy.** The store holds teammates' git email addresses and product plans. It SHOULD be a
repository only the team can read. On a public code repository, use a separate private store.

## 2. Records

A decision, `decisions/D-7K2P.md`:

```markdown
---
dumen: 1
id: D-7K2P
title: How the time picker shows availability
kind: ui
status: approved
services: [web-app]
topics: [booking-flow]
needs:
  booking-svc: Return remaining tables per slot in GET /slots
sources:
  s1: checkout-design
  s2: web-app:docs/booking.md@4f2c9e1
proposed_by: sofia@example.com
approved_by: sofia@example.com
date: 2026-11-21
---

Slot buttons show how many tables are left.

Context: The checkout design groups slots by part of day; each button shows its time.
Why: Guests pick a time faster when they see what is nearly full.
Question: How should the time picker show availability?
Rejected: A calendar heat map
```

### 2.1 Frontmatter

The frontmatter is **not YAML**. It sits between the first two `---` lines and follows these
rules, so that every implementation reads it the same way:

- One `key: value` per line. A value is the rest of the line, trimmed. It holds no newline and
  no control character.
- Lists (`services`, `topics`) are written `[a, b]`. Items match `[A-Za-z0-9][A-Za-z0-9._-]*`.
- `needs` and `sources` are the only nested keys: an indented `name: value` per line.
- A reader takes the first occurrence of a key and ignores keys it does not know.
- `dumen` is the record's protocol version. A reader MAY read a newer version, with a warning;
  a writer MUST NOT change a record or a store of a newer version.

| Key | Meaning |
| --- | --- |
| `id` | `D-` followed by 4 to 8 of `ABCDEFGHJKLMNPQRSTUVWXYZ23456789`. The file name is the id, and the id never changes |
| `title` | a short name, at most 120 characters |
| `kind` | `product`, `ux`, `ui`, `architecture` or `technical` |
| `status` | `open`, `proposed`, `approved`, `rejected` or `superseded` (section 3) |
| `services` | the services it applies to. Empty means the whole product |
| `topics` | words it is about, for search |
| `needs` | work it asks of other services: `service: what to do` |
| `sources` | what it is based on (section 5) |
| `supersedes`, `superseded_by` | the decision it replaces, or the one that replaced it |
| `asked_by`, `asked_of` | on a question: who asked, and the git email of the person who should answer |
| `proposed_by`, `approved_by`, `rejected_by` | git emails of the people involved |
| `date` | of the last change of status, `YYYY-MM-DD` |

### 2.2 Body

The first non-empty line that does not start with `>` is the **rule**: one sentence, present
tense, that others follow. Then, optionally, lines starting with `Context:`, `Why:`,
`Allowed:`, `Not allowed:`, `Question:`, `Answer:` and `Rejected:`. A record that is not
`approved` starts its body with the line
`> Not in force (<status>). Agents do not follow it.`, so that an agent who opens the file
cannot mistake it for a decision. For an open question, the rule line is the question and the
options follow as a list.

## 3. Lifecycle

```text
          ask              answer             approve
 (none) ──────▶ open ────────────▶ proposed ─────────▶ approved ───▶ superseded
    │                                 │                        (supersede, or approving
    └──────────── propose ────────────┘                         a proposal that replaces it)
                         open, proposed ──── reject ───▶ rejected
```

| Status | In force | Moves on by |
| --- | --- | --- |
| `open` | no: a question waiting on a person | `answer` (anyone), `reject` (a person) |
| `proposed` | no: a draft | `approve` or `reject` (a person) |
| `approved` | **yes** | `supersede` (a person) |
| `rejected`, `superseded` | no | final |

1. **Only `approved` binds.** Agents MUST NOT follow an `open` or `proposed` record as if it
   were settled, and nothing an implementation shows another session presents one as settled.
2. **A person decides.** `approve`, `reject` and `supersede` are a person's actions. Anyone who
   can push to the store may take them, and their git email is recorded. There are no roles in
   version 1; the safeguard is that every change reaches everyone as news, and git keeps the
   history. An implementation driven by an agent MUST make the person confirm the exact words
   of the change, outside the agent's control (section 7).
3. **Supersede is one commit.** The new record has `supersedes: <old>` and `status: approved`;
   the old one gets `status: superseded` and `superseded_by: <new>`. It is only valid while the
   old one is `approved`.
4. **Contest.** To challenge a decision without changing it, propose a record with
   `supersedes: <old>`. Approving it later supersedes the old one, as in rule 3, if the old one
   is still `approved` at that moment.
5. **Nothing is deleted.** Records are never removed or renamed. The git history is the audit
   trail.
6. **Scope.** A record applies to a service when its `services` is empty, names that service,
   or names it under `needs`.

## 4. Writing: one change, one commit, on the latest state

Every change is a transaction:

1. Fetch the store branch and make the local copy exactly the remote.
2. Check the preconditions **on that state**: the decision to approve is still `proposed`, the
   one to supersede still `approved`, the question to answer still `open`, a new id is free,
   and the record the person confirmed is byte for byte the one there now.
3. Write the files, and commit once.
   If nothing is left to commit although the change wrote something, the local copy was
   changed under the transaction: go back to step 1. Only a change that itself finds the
   outcome already there (the same summary again, a decision already approved) succeeds
   without a commit.
4. Push the commit by its id (`<commit>:refs/heads/<branch>`), fast-forward only. If the push
   is rejected because someone pushed first, throw the commit away and go back to step 1.
   Never merge, never force.
5. Fetch, and report success only when the remote branch contains the commit. If the push
   failed without a clear answer (a timeout, a dropped connection), this check also tells
   whether the commit got there, so that a change is never made twice.

A change either reaches the remote or did not happen. There is no local-only state, no outbox,
and no "approved here, not yet shared": when the remote cannot be reached, the person is told
that nothing changed.

**Races.** New records are separate files with random ids, so concurrent proposals never
conflict. When two people change the same record at the same moment, exactly one push wins.
The other's retry finds the new state and fails with what happened, such as
`D-7K2P was already replaced by D-9QXA; nothing was changed`, or succeeds if the outcome is the
same (approving a decision that was just approved reports who approved it).

**Locally,** one process at a time runs git in the local copy, under a lock that names its
holder: pid, where that pid means something (host name and, on Linux, pid namespace), and a
nonce. The lock is created whole (a written file, linked into place). A holder in the same
place is taken over only when its process is gone, however long it has held the lock; a holder
elsewhere (a dev container or WSL sharing the clone with the host) only when the lock has not
been touched for 5 minutes, and a holder touches it at every step of a change. A takeover runs
under a file of its own and removes the lock only if it is still the one judged dead. Before
committing, the staged files must be exactly the ones the change wrote; otherwise the change
was mixed with another and goes back to step 1. Reading never waits for the lock: hooks
fetch without it, news and `dumen show` read the fetched remote branch, and only the local
copy's files wait. When the lock has been held for more than 30 seconds, the prompt hook,
session start, `dumen list` and `dumen search` say so. A process killed in the middle leaves at most an
unpushed commit or loose files, which the next transaction's step 1 throws away.

**Commits** are authored with the person's git `user.email` and `user.name`. The subject names
the change (`approve D-7K2P`, `supersede D-7K2P: ...`). A `Dumen-Session: <id>` trailer names
the agent session that made it.

## 5. Sources and the spec

The spec is never copied into decisions; decisions point at it, and it points at decisions.

- **Spec parts** (`spec/<name>.md`) are what the repositories of a product share: the product,
  its services and its words, the plan of a release. A spec that belongs to one repository can
  stay in that repository instead.
- **Sources** (`sources/<name>.md`) are designs, Artifacts, Confluence pages or any document
  that lives elsewhere. A source file holds the link and a summary of what matters in it, so
  that every agent has the context from git alone, even without a connector to that tool, and
  the history shows what a decision was based on. Updating the summary is news.

```markdown
---
dumen: 1
title: Checkout design
url: https://claude.ai/design/p/abc
about: checkout screens, the slot picker
from: link
seen: 2026-11-21
added_by: sofia@example.com
---

> Summary written by sofia@example.com's agent from the link on 2026-11-21. The link is the source of truth.

The slot step shows three groups: lunch, afternoon, dinner. Each slot is a button with its time.
```

- A decision's `sources` names what it rests on: a source (`checkout-design`), a file of a code
  repository at a commit (`web-app:docs/booking.md@4f2c9e1`), or an `https` link.
- `from` says what the summary rests on: `link` (the agent opened the link and read it) or
  `person` (the person told the agent what it holds). The banner says which, so a relayed
  summary is never taken for a read one: "Summary written by <email>'s agent from what <email>
  said the link holds, on <date>. The agent did not read the link, ..."
- `url` is an `https` link. Implementations never fetch it; an agent opens it with the
  connectors it has (Claude Design, Artifacts, Confluence and so on).

**What an agent reads first:** the spec parts its task touches, then the sources' summaries,
then the decisions in force. Decisions are newer than the spec and win where they differ; when
a decision changes what the spec says, the spec part SHOULD be updated to match, and that
update is news too.

## 6. News

Each agent session keeps a **mark**: the store commit it last looked at. A subagent has its own
mark. News is every record, source and spec part changed between the mark and the latest
fetched commit, read as it is at that commit, leaving out the commits the session made itself
(found by their `Dumen-Session` trailer):

- an `approved` decision, with `It replaces <old>` when it supersedes one, and
  `NEEDS FROM <service>: <work>` when it asks this service for work;
- a `superseded` decision not already covered by its replacement;
- for the person who asked: their question has an answer (it is shown as waiting for approval,
  not as settled);
- for the person asked: a question waits for them;
- an updated source or spec part.

The mark moves on only after the news was handed to the agent, so a failure can show news twice
but never lose it. After a resume or a compaction, the news since the mark comes first, then the
whole living spec again. A session whose session start never ran gets the living spec at its
first hook, and a subagent the decisions in force, once. The living spec given at session start
stays within about 8,000 characters and says what it left out. A subagent's own changes count as
its session's, so its parent is not told them as news; the subagent reports them itself.

**Freshness.** With no server, the hooks fetch:

| When | How | Time limit |
| --- | --- | --- |
| session start, resume, compaction | fetch, then show the living spec | 15 s |
| each message of the person | fetch, then news | 3 s |
| after each tool call | news from what was fetched; a background fetch when the last one is older than 20 s | none (no network) |
| before the agent stops | fetch, then news; the agent is held once to read it | 3 s |

So a decision reaches a working session within about 30 seconds of being pushed. When the
remote is slow or down, the hooks stay silent and fast, and the next message of the person says
since when the data is stale.

Text from the store is team data, written by teammates and their agents. An implementation
shows it labelled as such, without control characters and bounded in length, and never as
instructions.

## 7. The person's yes, in Claude Code

dumen runs inside Claude Code without a server. `approve`, `reject` and `supersede` are shell
commands the agent runs, and they need the person's yes:

1. A PreToolUse hook sees the command. It accepts it only as a command of its own, with nothing
   chained before or after it. It fetches the store, reads the record **as it is on the remote**,
   and answers `ask`, with the exact words of the change as the reason: *"dumen: it puts D-7K2P
   "Slot buttons show how many tables are left" in force for every agent on the team. It asks
   booking-svc to: ... Allow only if you, the person, agreed to it."*
2. With that answer, the hook adds a **one-use ticket** to the command. The ticket is bound to
   the command's arguments, to the session, and to the hash of the record the person was shown.
   Claude Code runs the command only if the person says yes.
3. The command refuses to run without a valid ticket, and uses it up. A ticket is good for ten
   minutes. If the record changed after the person was asked, the command refuses and nothing
   changes. dumen's state keeps only a hash of each ticket, and the hook refuses any command or
   file an agent writes that contains `--ticket`: only the prompt adds one.
4. It asks only in the permission modes known to put the question in front of the person
   (`default`, `acceptEdits`, `plan`) and refuses in every other mode, including ones it does not
   know.
5. A dumen command that approves, rejects or supersedes in any other form (chained, wrapped in
   another command, inside `sh -c "..."`) is refused with the reason, and so is writing into the
   store or dumen's state with a file tool or a shell command.
6. Every write is recorded with the person's git `user.email`; without one, dumen writes nothing.

This is a guard rail for a team that trusts each other, not a security boundary: it stops an
agent from deciding on its own, not a person with a shell. Anyone who can push to the store can
write any file; git records who did.

## 8. Questions for people outside Claude Code

An `open` record with `asked_of` is a question waiting on a named person. The protocol does not
care how they answer: any channel that turns it into a `proposed` record, with the answer as
its rule, in one commit that follows section 4, is a valid answerer. In Claude Code, that is
`dumen answer`. A Slack, email or web adapter for a non-technical PM needs nothing new from
this document.
