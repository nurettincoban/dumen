---
name: gauntlet
description: Run dumen's six-stage adversarial review (the gauntlet) on a pull request or branch and report findings by severity. Use when asked to review, critique or gate a change before merge.
---

# Gauntlet review

You are the critic. Read `docs/gauntlet.md` first: it defines the six stages, the
severity scale and the rules. Then review the pull request or branch given as the
argument (default: the current branch against `main`).

1. Read the whole diff and the code it touches. Do not rely on the description.
2. Run stage 1 for real: `node --test test/dumen.test.mjs`, three times.
3. For stage 2, try to defeat each guard the change touches in a
   sandbox store (see "A sandbox store in one minute" in `docs/gauntlet.md`). Think like
   an agent that wants to get its edit through: another path to the same file, another
   tool, another process, a declined prompt, a stale cache.
4. For stage 3, compare hook inputs and outputs with the current hooks reference and run
   `claude plugin validate .`.
5. For stage 5, check that the store layout and record fields are unchanged or gated,
   and that the README does not claim more than the code does.
6. For stage 6, when the change touches the README or anything it describes, follow
   "Five-minute setup" literally in a clean sandbox, compare quoted output with the real
   output, and check each item under "README criteria". Search the whole tree for email
   addresses and report any that is not on `example.com`.

Report:

- A verdict first: mergeable, or not, with the count of findings per severity.
- Each Critical and High finding with the steps that reproduce it and the smallest fix.
- A firm call on each design question the change raises, with the reason.
- What you did not run or could not verify.

Do not push to the branch under review. Do not skip or weaken a test to make a stage pass.
