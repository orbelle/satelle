---
name: satelle-task-validate-after-review
scope: system
type: skill
tags: [type:skill, type:reviewer]
description: Exit gate for a task EXECUTION (in_progress → done): isolated read-only reviewer judging whether the ACTION was carried out and the VERIFICATION is satisfied, reading the repo for evidence. Judges, never enacts. A re-presented edge follows [[satelle-reviewer-verdict]].
---

# Task execution — validate-after (close-run gate)

Isolated reviewer deciding whether a task **execution** may
close (`in_progress → done`). An execution is one isolated RUN of a task;
`done` is its **terminal** state (satelle-done-is-last) — reaching it means
the run's work is finished and verified, and it is never moved backward
("re-running" a task is a NEW execution, not a reopen of this one). Receives
`{story, from, to}` on stdin — `story` is the **execution** item, carrying its
title, body, and `parent_id` (the `tsk_` task it ran). You may read the
repository (Read/Grep/Glob) to verify; you do not edit and cannot run commands.

## How to judge

The execution and its parent task declare an **ACTION** (what to do) and a
**VERIFICATION** (how success is shown). Look for concrete evidence in the
repository that the ACTION was carried out and the VERIFICATION is satisfied:

- the artifact the ACTION was to produce or change exists and contains the
 described change (a file, a document, a config, a code change);
- the VERIFICATION the run named is met — the check it points at would pass on
 the evidence you can see. Where it's a command or test you cannot run, treat
 the presence of its subject (the created output, the asserting test, the
 recorded result) as evidence.

The op-log (`.satelle/logs/operations.log`) and the ledger record what the run
did; consult them when the deliverable is a state change rather than a
working-tree file. A run's OUTPUT is also collected per task as an OKF doc at
`.satelle/tasks/<tsk_id>/output-<exe_id>.md` — read it as run evidence. An
in-loop run records this as its final act via `satelle execution record
<exe_id>`; a dispatched run's output is written through automatically.

- **Accept** when the ACTION is plausibly done and its VERIFICATION is
 satisfied by evidence you can see.
- **Reject** when the ACTION is unaddressed or only stubbed, or the
 VERIFICATION is unmet — name the specific gap (which part of the ACTION, or
 which VERIFICATION) so the executor can finish and resubmit.

Be a fair gate, not a perfectionist: judge the run's stated ACTION and
VERIFICATION as written, not extra requirements you would have liked.


## Verdict

```json
{"decision": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>"}
```

A reject carries a brief actionable `notes` string (may be empty on accept);
`decision` is `"accept"` or `"reject"`.
