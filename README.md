<div align="center">

# dumen

**Live spec-driven development for teams on Claude Code.**

The spec you write up front, and every decision made along the way, in one living spec in
git. Every agent on the team works from its latest version, and hears a change while it is
still working.

[Install](#install) · [Real sessions](#real-sessions) · [How it works](#how-it-works) · [Protocol](docs/protocol.md) · [Site](https://nurettincoban.github.io/dumen/)

</div>

---

Spec-driven development writes the spec before the code. But most of the questions only come
up while the work is going on. Slot buttons or a calendar? Refund to the card or as store
credit? The person answers in one chat, and that answer never reaches the next agent, on the
next laptop, in the next service.

dumen keeps those answers in the spec. When you answer your agent, it records the answer as a
**decision**: one sentence that every agent follows, of any kind (product, UX, UI,
architecture, technical). You confirm it, and from then on it binds every agent on the team.
Agents that are already running get it as news on their next step, along with any work it asks
of their service. Decisions are binding but changeable, and every change is news too.

The spec written up front, the **sources** it rests on (a Claude Design file, an Artifact, a
Confluence page, each kept in git with a summary) and the decisions are one **living spec**. It
lives on a branch of the git remote you already have. There is no server, and nothing to
install besides the plugin.

## A real session

Ayşe's agent is building the cancel endpoint in `booking-svc`. While its tests run, Deniz, on
another laptop and in another service, changes the refund rule. Ayşe's agent is about to tell
her nothing changed when dumen holds it back with the news. This is a real recording,
shortened, from [`test/live.mjs`](test/live.mjs):

```text
> I am building the cancel endpoint in src/cancel.go. Before you ask me whether a cancelled
  booking is refunded to the card or as store credit, check whether the team already decided
  it and tell me which decision you follow. Then run `sh test.sh` once and tell me whether
  anything changed for the cancel endpoint.

  ⎿ dumen: this product has a living spec, shared by the team through git ...
    1. Decisions in force for booking-svc and the whole product ...
       - D-MWEU [product] Cancelled bookings are refunded as store credit
⏺ The team already decided this: D-MWEU: cancelled bookings are refunded as store credit.
  I'll follow that for the cancel endpoint, so I won't ask you. Running the tests now.
⏺ Bash(sh test.sh)
  ⎿ PASS
⏺ sh test.sh finished with PASS. Nothing changed for the cancel endpoint ...
  ⎿ [dumen, before stopping] DECISION NEWS: teammates changed the living spec while you worked ...
    - D-PEEZ [product] Cancelled bookings are refunded to the original card, not as store
      credit (approved by deniz@example.com)
      It replaces D-MWEU, which is no longer in force.
⏺ Correction to my last message: the decision I said I was following is out of date. Deniz
  approved D-PEEZ while the tests ran, and it replaces D-MWEU. Cancelled bookings in
  src/cancel.go are now refunded to the original card, not as store credit.
  I haven't written any cancel code yet, so nothing needs undoing.
```

More real sessions are [below](#real-sessions) and in [docs/transcripts](docs/transcripts).

## Install

Inside Claude Code, once per developer:

```text
/plugin marketplace add nurettincoban/dumen
/plugin install dumen@dumen
```

That is all. The plugin brings a skill, a few hooks and one dependency-free script. It needs
`git` and Node.js 18 or newer, and it uses the SSH keys or credential helper your git already
has. In a repository that is not part of a dumen project, it stays silent.

Then tell your agent:

> Set up dumen for this project.

The agent tells you where the living spec will live, then runs `dumen init`. By default that is
a branch named `dumen` on the repository's own origin, so teammates join by installing the
plugin. A product of several repositories shares one store repository: say which one.

**The store holds your teammates' git email addresses and your product plans.** Keep it in a
repository only the team can read. If your code is public, use a separate private store.

## How it works

You never type a dumen command. You talk to your agent, and you confirm Claude Code's prompt
when a decision is put in force.

| You | Your agent | Everyone else's agents |
| --- | --- | --- |
| answer its "A or B?" | records the answer as a proposed decision, and offers to put it in force | nothing yet: a proposal is not in force |
| say yes, and confirm the prompt | `dumen approve D-7K2P` | get `DECISION NEWS` on their next step, with `NEEDS FROM <their service>` if it asks them for work |
| say "change it" | `dumen supersede D-7K2P ...` | hear "D-9QXA replaces D-7K2P" |
| point at a design or doc | keeps it as a source, with a summary, and proposes the decisions it implies | read the summary from git, and open the link with their own connectors |
| ask "what changed this week?" | `dumen log` | |

Before an agent asks you something, it looks in the living spec first. If a decision in force
already answers it, the agent follows it and tells you which one, instead of asking again.

**The prompt is the approval.** When your agent puts a decision in force, Claude Code shows you
its exact words as they are in the store. This is the real prompt, shortened by two separator
lines, from an interactive session ([interactive-approve](docs/transcripts/interactive-approve.txt)):

```text
 Bash command
 Approve decision D-ZJK2 (asks the user to confirm)
 dumen approve D-ZJK2 --ticket 825b3b7957fba4a727cc660e7cfea94d
 │ Hook PreToolUse:Bash requires confirmation for this command:
 │ dumen: it puts D-ZJK2 "Slot buttons show how many tables are left" [ui] for web-app in force
 │ for every agent on the team. It asks booking-svc to: Return remaining tables per slot in
 │ GET /slots. It is recorded as sofia@example.com's decision. Allow only if you, the person,
 │ agreed to it. [plugin:dumen]
 Do you want to proceed?
 ❯ 1. Yes
   2. No
```

The command runs only after your yes, and it carries a one-use ticket that only this prompt
gives out. An agent cannot approve on its own, chain the command behind another one, or write
the store's files by hand: dumen refuses all of those. In a permission mode that may not ask
you, it refuses to approve at all, and the agent asks you to switch modes
([plan-mode](docs/transcripts/plan-mode.txt): a plan approved "with auto mode", then the switch).
An allow rule does not skip it either: with `Bash(dumen:*)` or even `Bash(dumen approve:*)` in
your settings, Claude Code (seen on v2.1.330) still asks you, and with no one to ask it refuses
([allow-rule](docs/transcripts/allow-rule.txt)). To stop the prompts for reading, allow only
`dumen show`, `list`, `search` and `log`.

## Real sessions

All of these are real headless Claude Code sessions with the plugin, run at the same time on
real git repositories by [`test/live.mjs`](test/live.mjs). They are shortened, and temporary
paths are replaced. Headless sessions have no person to see the prompt, so a hook answers Yes
in the person's place; everything before that answer is the real path
([how](docs/transcripts/README.md)). The prompt itself, answered by a person, is in the
interactive recordings above.

**The frontend needs something from the backend.** Sofia answers her agent's question about
the time picker in `web-app`. Ayşe's agent, in the middle of its own work in `booking-svc`,
gets the decision and the work it asks of her service on its very next step:

```text
web-app
> ... I answered: slot buttons that show how many tables are left. The backend does not
  return that yet ... Save my answer for the team and put it in force, I agree.

⏺ Bash(dumen propose --kind ui --title "How the time picker shows availability"
    --rule "Slot buttons show how many tables are left" --services web-app
    --topics booking-flow --needs "booking-svc: Return remaining tables per slot in GET /slots" ...)
  ⎿ Proposed D-9NWW: Slot buttons show how many tables are left
⏺ Bash(dumen approve D-9NWW)
  ⎿ Claude Code asks the person: dumen: it puts D-9NWW "Slot buttons show how many tables
    are left" [ui] for web-app in force for every agent on the team. It asks booking-svc to:
    Return remaining tables per slot in GET /slots ...
  ⎿ ✔ The person answers Yes.
  ⎿ D-9NWW is in force for every agent on the team

booking-svc, at the same time
> Run `sh check.sh` once and wait for it. Then read src/slots.go. Then tell me whether
  anything from the team changed while you worked.

⏺ Bash(sh check.sh)
  ⎿ go vet: ok
⏺ Read(src/slots.go)
  ⎿ [dumen] DECISION NEWS: teammates changed the living spec while you worked ...
    - D-9NWW [ui · web-app] Slot buttons show how many tables are left
      (approved by sofia@example.com)
      NEEDS FROM booking-svc: Return remaining tables per slot in GET /slots
⏺ Something changed while I worked. A new team decision, D-9NWW ... asks this service to
  return the remaining tables per slot in GET /slots.
  - List() []string only carries slot names, so it can't hold a remaining-tables count ...
  - The decision is binding now, so I'll build GET /slots to include that count rather than
    ask you again.
```

**A design becomes part of the spec.** Sofia's agent keeps a Claude Design file as a source
of truth, with a summary in git, and proposes the four decisions it implies, each pointing at
the source ([design-source](docs/transcripts/design-source.txt)).

Full recordings: [frontend-web-app](docs/transcripts/frontend-web-app.txt),
[frontend-booking-svc](docs/transcripts/frontend-booking-svc.txt),
[backends-booking](docs/transcripts/backends-booking.txt),
[backends-billing](docs/transcripts/backends-billing.txt),
[design-source](docs/transcripts/design-source.txt), [setup](docs/transcripts/setup.txt).

## Sources of truth: designs, Artifacts, docs

Point your agent at the design or document that is the truth for part of the product:

> This Claude Design file is the source of truth for the checkout screens: https://claude.ai/design/...

The agent opens it with the connector it has, writes a short summary of what matters, and keeps
both in git (`sources/checkout-design.md`) with the date it was seen. If it has no connector to
that tool, it writes down what you tell it the design holds, and the summary says so, so nobody
takes it for a read one. Then it proposes the
decisions the design implies, each pointing at the source. Every other agent now has the context
from git alone, even without a connector to that tool, and the history shows what each decision
was based on. When the design changes, the agent updates the summary and offers to supersede the
decisions it changes; everyone hears both. dumen stores links and summaries, and never fetches a
URL itself.

The spec you write up front goes in the same place: `spec/product.md` (the product, its services,
its words) and `spec/plan.md`, when several repositories share them. A spec that belongs to one
repository can stay there; decisions point at it as `repo:path@commit`.

## Several repositories, one product

Teams with services keep one store for all of their repositories. Set dumen up in each one with
the same store and that repository's service name. A decision names the services it applies to,
and the services it needs work from. Agents in a service see that service's decisions and the
product-wide ones.

## Questions for someone else

When only someone else can answer, such as a PM or the owner of another service, the agent asks
them through the living spec: `dumen ask --of pm@example.com ...`. They see the question at
their next session, and you hear the answer as news. A question is a plain file waiting on a
named person, so a Slack, email or web adapter for people who do not use Claude Code can answer
it later without changing the protocol.

## What it guarantees

- **Nothing is lost when people write at once.** Every change is fetch, check on the latest
  state, one commit, fast-forward push, retry. Tested with twelve writers in three clones at
  once.
- **Exactly one wins a race.** When two people replace the same decision at the same moment, one
  push wins. The other is told who won, and nothing of theirs is written.
- **Shared, or not done.** A change reaches the remote, or the person is told that nothing
  changed. There is no "approved on my laptop".
- **Your code is never touched.** The store lives in `.git/dumen/`, apart from your work tree,
  index and branches, even when a git hook or an IDE sets `GIT_DIR`.
- **News is never lost.** A session's mark moves on only after the news was handed out. After a
  resume or a compaction, the news since then comes first.
- **Fresh within about 30 seconds.** Running sessions check at each of your messages, after
  each tool call (in the background, without waiting for the network) and before they stop.

## Honest limits

- **A guard rail, not a security boundary.** dumen stops an agent from deciding on its own. It
  does not stop a person with a shell, or an agent that clones the store elsewhere and pushes
  by hand. Anyone who can push to the store can write any file, and git records who did.
- **Anyone with push access can approve.** There are no roles in this version. The safeguard is
  that every change reaches everyone as news, with a name on it.
- **Matching is by the agent, not by a search engine.** Agents read the decisions in force and
  search by words. A decision phrased very differently from the question can be missed.
- **Needs Node.js 18+.** The plugin cannot install it; the agent tells you when it is
  missing.
- **News waits for a hook.** An agent that is in the middle of one long tool call hears the news
  when that call returns.
- **A subagent's decisions** are not news to its own parent session; the subagent reports them.

## Protocol

dumen is a protocol before it is a tool: markdown files with a small, strict frontmatter on a
git branch, and a few rules for writing them safely. Any tool that follows
[docs/protocol.md](docs/protocol.md) can read and write a store. The plugin's
[`dumen.mjs`](plugin/bin/dumen.mjs) is one implementation, in one file with no dependencies.

## Development

```sh
node --test test/dumen.test.mjs      # the protocol against real git repositories
node test/live.mjs                   # real Claude Code sessions (needs the claude CLI and an account)
```

Every change runs the [gauntlet](docs/gauntlet.md), an adversarial review in six stages.

## License

[Apache-2.0](LICENSE)
