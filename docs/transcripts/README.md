# Transcripts

Real headless Claude Code sessions with the dumen plugin, recorded by
[`test/live.mjs`](../../test/live.mjs) on real git repositories, several at once. Temporary paths
are replaced; nothing else is edited.

**Who answers the prompt.** The sessions are headless, so no person is there to see Claude
Code's permission prompt. A `PermissionRequest` hook in each repository's settings answers Yes
in the person's place. Everything up to that answer is the real path: dumen's guard, the words
Claude Code shows, and the one-use ticket. The transcripts mark the answer as
"✔ The person answers Yes." In an interactive session, the same words appear in Claude Code's
own prompt, and the command runs only if the person picks Yes.

| File | What it shows |
| --- | --- |
| [setup.txt](setup.txt) | "Set up dumen for this project", start to finish |
| [frontend-web-app.txt](frontend-web-app.txt) | Sofia's answer becomes a decision that asks work of booking-svc |
| [frontend-booking-svc.txt](frontend-booking-svc.txt) | Ayşe's agent hears it mid-task, with the work for its service |
| [backends-seed.txt](backends-seed.txt) | The refund rule is put in force |
| [backends-booking.txt](backends-booking.txt) | The agent follows the rule instead of asking, then is held back once with the change |
| [backends-billing.txt](backends-billing.txt) | Deniz replaces the rule while Ayşe's agent works |
| [design-source.txt](design-source.txt) | A design becomes a source of truth, and four decisions are proposed from it |
