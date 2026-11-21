# The gauntlet

Every pull request to dumen runs a gauntlet: six independent, adversarial review
passes, each through one lens. A pull request merges when no Critical or High finding
is open. The builder and the critic are different people or different agent sessions.

## Stages

| # | Lens | Passes when |
| --- | --- | --- |
| 1 | Correctness and concurrency | `node --test test/dumen.test.mjs` passes, three runs in a row, including the parallel-writer and same-decision race tests. Every error branch on the write path either reaches the remote or leaves nothing changed, and says which. |
| 2 | Security and agent bypass | For every guard the change touches, at least one bypass was attempted in a sandbox store. Text written by an agent never reaches another agent's context before a human has seen it. |
| 3 | Claude Code conformance | Hook inputs and outputs were checked against the current [hooks reference](https://code.claude.com/docs/en/hooks). `claude plugin validate .` passes. A change to hooks, the skill or the guard ran the live sessions (`node test/live.mjs`). |
| 4 | Cross-platform and host independence | The tests pass on linux, macOS and windows in CI. No behavior depends on the name of a git host. Changes to paths, locks or renames were reviewed for Windows semantics. |
| 5 | Open source and data model | A change to the store layout or to a record field comes with a version gate and a migration note. Every claim in the README is true of the code. |
| 6 | README and first run | See [README criteria](#readme-criteria). Runs when the change touches the README, a command, a flag, an install step or any output the README quotes. |

## README criteria

The README is the product page of an open source tool. Stage 6 passes when all of these hold:

1. **Ten-second pitch.** The first screen says what dumen is, who it is for and why they
   would want it, with one real example of input and output. No badges or history first.
2. **One-sentence setup.** A reviewer installs the plugin in a clean container with a new
   bare remote, says what the README says to the agent, and reaches a working living spec
   without reading the source. No step asks a person to type a command.
3. **True output.** Every quoted session is a real recording from `test/live.mjs`.
4. **Honest limits.** Each known limit and each open Medium or higher finding that a user
   can hit is in "Honest limits" or "Security model". The README never claims more than
   the code does.
5. **No personal data.** Examples, tests and fixtures use `example.com` addresses and
   invented names only. No real email address, token, internal host name or private
   repository URL appears anywhere in the repository.
6. **Complete reference.** Every command and flag of `dumen help` is in the protocol or the
   skill, and nothing described there is missing from `dumen.mjs`.
7. **Links work.** Every relative link and anchor resolves; install commands point at the
   current repository and module path.
8. **Reads well.** Plain language, short sentences, one idea per paragraph, consistent
   terms (living spec, decision, source, spec part, store). A newcomer to Claude Code hooks can follow it.

A broken setup step or a false claim is High. Personal data in the repository is Critical.

## Severity

| Level | Meaning | Blocks merge |
| --- | --- | --- |
| Critical | A human-only action can be taken without the human, or data is lost. | Yes |
| High | A guard can be bypassed, or one fault stops the whole team. | Yes |
| Medium | Wrong message, degraded behavior, unbounded growth. | No, but blocks the next release |
| Low | Everything else. | No |

## Rules

1. **Evidence.** A Critical or High finding is opened with steps that reproduce it and is
   closed with a regression test that failed before the fix.
2. **Separation.** The critic does not push to the builder's branch. The builder says a
   finding is fixed; the critic verifies it with the original steps.
3. **Decisions.** The critic makes a firm call on each design question and writes down
   the reason. Changes of scope and steps that cannot be undone go to the maintainer.
4. **Honesty.** A stage that was not run is reported as not run. "Compiles for Windows"
   is not "works on Windows".

## A sandbox store in one minute

```sh
cd "$(mktemp -d)" && git init -q --bare store.git && git init -q code && cd code
git commit -q --allow-empty -m init
D=/path/to/dumen/plugin/bin/dumen.mjs
node $D init --store "$OLDPWD/store.git" --service web-app
node $D propose --kind ui --rule "Slot buttons show how many tables are left"
# drive a hook by hand, as Claude Code would
echo '{"session_id":"s1","cwd":"'$PWD'","tool_name":"Bash","tool_input":{"command":"dumen approve D-XXXX"}}' \
  | node $D hook guard
```

## Running it

In Claude Code, `/gauntlet <pull request number or branch>` runs the six stages and
reports findings by severity. Continuous integration runs the automated parts of stages
1 and 4 on every push; the live sessions of stage 3 need an account and run locally.
