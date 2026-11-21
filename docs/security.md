# Security model

Please read this page before you trust dumen with anything.

- [What an agent sees and can do](#what-an-agent-sees-and-can-do)
- [A guard rail, not a boundary](#a-guard-rail-not-a-boundary)
- [What to protect](#what-to-protect)

## What an agent sees and can do

At session start the agent receives a short block: active directives, the
approved decisions that apply, links to sources of truth and its role. Only ids and one-line
labels are injected. Bodies are fetched on demand through MCP tools, so the context cost stays
small. A directive published while a session is running reaches the agent on its next prompt or tool call.

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
  Code itself started for the session, and that was running when you were asked, can do so.
  A second `dumen mcp` that an agent starts from a shell is not that server.
- The prompt is used only in the permission modes where a person answers it (`default`,
  `acceptEdits`, `plan`). In every other mode the call is refused, and you run the command in
  your own terminal.
- A hook blocks shell commands such as `dumen approve` inside a session, and the CLI refuses
  human actions when it sees that it runs inside Claude Code. These two are a second layer, not a
  wall: an agent that deliberately clears its environment can get past them. See
  [the next section](#a-guard-rail-not-a-boundary).

## A guard rail, not a boundary

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

## What to protect

- Protect the `dumen` branch on your git host: allow pushes from team members, forbid force
  pushes and deletion.
  On GitHub that is a branch protection rule or ruleset for `dumen`; on GitLab it is
  Settings > Repository > Protected branches. Do not require pull requests or status checks on
  it: dumen pushes directly.
- Do not put a CI configuration on the store branch. `dumen init` and `dumen join` refuse a
  branch that holds one (`.gitlab-ci.yml`, `.github/workflows`, `bitbucket-pipelines.yml`,
  `Jenkinsfile` and the like), so that dumen writes never start pipelines.
