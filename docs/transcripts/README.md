# Transcripts

Real Claude Code sessions with the dumen plugin, on real git repositories. Temporary paths are
replaced; nothing else is edited.

**Interactive, a person at the keyboard.** Recorded from the terminal with tmux; the person reads
Claude Code's prompt and presses the key.

| File | What it shows |
| --- | --- |
| [interactive-approve.txt](interactive-approve.txt) | The real permission prompt for `dumen approve`, with the decision's words, and the person's Yes |
| [allow-rule.txt](allow-rule.txt) | With `Bash(dumen:*)` and `Bash(dumen approve:*)` allowed in the settings, the prompt still comes (the person says No), and headless the approval is refused |
| [plan-mode.txt](plan-mode.txt) | A plan approved "with auto mode": dumen refuses to approve in a mode that may not ask, the person switches to manual mode, and is asked |

**Headless, several at once,** recorded by [`test/live.mjs`](../../test/live.mjs), and one more
with a subagent ([subagent.txt](subagent.txt): the subagent gets the decisions in force at its
first tool call).

**Who answers the prompt in the headless ones.** The sessions are headless, so no person is there to see Claude
Code's permission prompt. A `PermissionRequest` hook in each repository's settings answers Yes
in the person's place. Everything up to that answer is the real path: dumen's guard, the words
Claude Code shows, and the one-use ticket. The transcripts mark the answer as
"✔ The person answers Yes." In an interactive session, the same words appear in Claude Code's
own prompt, and the command runs only if the person picks Yes (see the interactive recordings).

| File | What it shows |
| --- | --- |
| [setup.txt](setup.txt) | "Set up dumen for this project", start to finish |
| [frontend-web-app.txt](frontend-web-app.txt) | Sofia's answer becomes a decision that asks work of booking-svc |
| [frontend-booking-svc.txt](frontend-booking-svc.txt) | Ayşe's agent hears it mid-task, with the work for its service |
| [backends-seed.txt](backends-seed.txt) | The refund rule is put in force |
| [backends-booking.txt](backends-booking.txt) | The agent follows the rule instead of asking, then is held back once with the change |
| [backends-billing.txt](backends-billing.txt) | Deniz replaces the rule while Ayşe's agent works |
| [design-source.txt](design-source.txt) | A design becomes a source of truth, and four decisions are proposed from it |
