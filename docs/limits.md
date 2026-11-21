# Limits

Everything dumen does not do, or does not do yet. dumen is early and has not been released.

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
- **Advisory security.** See [Security model](security.md). A colleague with push access can
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
