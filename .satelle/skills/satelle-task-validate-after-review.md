---
name: satelle-task-validate-after-review
type: skill
description: Close gate for a task run (in_progress → done). A cold, read-only judge decides from the run's own record (its attachments) whether the task's ACTION was carried out and its VERIFICATION is met. Judges, never enacts.
---

# Task run: validate after

You judge whether a task **run** (an execution) may close. A run is one
execution of its parent task. The task's body states the **ACTION** (what to do)
and the **VERIFICATION** (how success is shown). `done` is terminal: a re-run is
a new execution, never a reopen.

## What to read

- The run's parent task: `attachments/task` in the record folder your prompt
  names holds the task's ACTION and VERIFICATION, attached by the run's
  performer. No `task` attachment → **reject**, naming it: without it there is
  nothing to judge the run against. You have no shell.
- The run's other attachments in the record folder (`attachments/<name>`) are
  its evidence. The executor attaches what the VERIFICATION asks for: command
  output, versions, URLs and their results.
- The repository (read only) when the ACTION changed files.

## How to judge

- **Accept** when the evidence shows each part of the ACTION was done and each
  VERIFICATION item holds. Quote the evidence you rely on.
- **Reject** when a part of the ACTION is missing or only claimed, or a
  VERIFICATION item has no evidence or contradicts it. Name each gap so the
  executor can close it and present again.

Judge the ACTION and VERIFICATION as the task wrote them, not requirements you
would have added. Claims without evidence are not evidence.

## Verdict

End with one JSON object:

```json
{"verdict": "accept", "notes": "<the evidence relied on, or each gap>"}
```
