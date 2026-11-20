# The gauntlet

Every pull request to dumen runs a gauntlet: five independent, adversarial review
passes, each through one lens. A pull request merges when no Critical or High finding
is open. The builder and the critic are different people or different agent sessions.

## Stages

| # | Lens | Passes when |
| --- | --- | --- |
| 1 | Correctness and concurrency | `go vet ./...`, `go test -race -short ./...` and the 1,000-operation concurrency test pass. Every error branch on the write path either queues the operation or rejects it, never both. |
| 2 | Security and agent bypass | For every guard the change touches, at least one bypass was attempted in a sandbox store. Text written by an agent never reaches another agent's context before a human has seen it. |
| 3 | Claude Code conformance | Hook inputs and outputs were checked against the current [hooks reference](https://code.claude.com/docs/en/hooks). `claude plugin validate .` passes. A change to hooks or the MCP server ran the local end-to-end suite (`go test -tags e2e ./test/e2e`). |
| 4 | Cross-platform and host independence | Builds for linux, darwin and windows. No behavior depends on the name of a git host. Changes to paths, locks or renames were reviewed for Windows semantics. |
| 5 | Open source and data model | A change to the store layout or to a record field comes with a version gate and a migration note. Every claim in the README is true of the code. |

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
export DUMEN_HOME=$(mktemp -d) && cd "$(mktemp -d)"
git init -q --bare remote.git
git init -q code && cd code && git remote add origin ../remote.git
env -u CLAUDECODE dumen init
env -u CLAUDECODE dumen direct --deny --paths 'src/auth/**' "frozen"
# drive a hook by hand
echo '{"session_id":"s1","cwd":"'$PWD'","tool_name":"Edit","tool_input":{"file_path":"'$PWD'/src/auth/x.go"}}' \
  | CLAUDE_PROJECT_DIR=$PWD dumen hook pre-tool-use
```

`env -u CLAUDECODE` makes a command act as a human when it is run from inside a Claude
Code session.

## Running it

In Claude Code, `/gauntlet <pull request number or branch>` runs the five stages and
reports findings by severity. Until continuous integration is set up, the automated
parts of stages 1 and 4 are run locally by the reviewer.
