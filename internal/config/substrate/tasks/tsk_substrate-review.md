---
id: tsk_substrate-review
type: task
status: done
priority: medium
category: substrate
tags: substrate-review, quality
created: 2026-10-31T00:00:00Z
updated: 2026-10-31T00:00:00Z
---

# Review this repo's substrate against its defaults, the code and the BDD baseline

The run of `satelle substrate review`. Each invocation creates a fresh execution
of this task, so a review in flight shows in the local UI's Activity tab beside
the stories; you do not create one by hand. Detail: `satelle help substrate`.

## Action
Run `satelle substrate review` over this repo's substrate: gather the inventory,
judge each area batch, and write the report. The run records each stage (gather,
judge per batch, report) on its execution's ledger and attaches the report to the
execution as `substrate-review-report-<exe_id>`.

## Verification
The execution carries the `substrate-review-report-<exe_id>` attachment — the
JSON report, with one disposition (or "not judged") per gathered item — and its
ledger holds a `gather`, a `judge` entry per batch and a `report` stage entry.

## Acceptance Criteria
1. The execution's report attachment holds a disposition or "not judged" for every
 gathered item.
2. The execution's ledger holds the gather, judge and report stages.
3. Re-runnable from `done` (a new execution per review).
