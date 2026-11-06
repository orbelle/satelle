---
name: task-run
scope: project
type: skill
tags: [type:skill]
description: In-loop (or binding-dispatched) executor skill for the task/execution route's in_progress step. Perform the execution's declared ACTION and leave VERIFICATION evidence; do not advance status — satelle-task-validate-after-review gates the close.
---

# Task run (executor step)

You are the **executor** for a task **execution** at `in_progress`. The work item
carries the ACTION and how success is VERIFIED. Perform the action; leave evidence
the after-review gate can judge. You do **not** advance status.

## Do

1. Read the execution / parent task (title, body, action, verification).
2. Carry out the **ACTION** exactly — no scope expansion into unrelated story work.
3. Leave **VERIFICATION** evidence the after-gate expects (output, ledger note, or
   files the task named).
4. Stop. The `in_progress → done` edge is gated by
   `satelle-task-validate-after-review`.

## Do not

- Self-enact `done` or reopen a completed run (re-run = new execution).
- Do work the task's ACTION does not name. A version bump, a push to main or a
  CI release is in scope only when the ACTION names it, as the release task's
  ACTION does (`.satelle/skills/release.md`).

See [[satelle-agent-model]] and the task-workflow prose.
