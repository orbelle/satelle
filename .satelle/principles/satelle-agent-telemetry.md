---
name: satelle-agent-telemetry
type: principle
tags: [type:principle]
applies_to: ["*"]
description: With the satelle CLI grant, record what only you can judge (a retry, abandoned approach or wall in your own work; a step that fought you) via `satelle ledger append <id> --kind <kind> --payload '<json>'`, and see every append and attach land.
---

# Self-report what only you can judge

The binary already records dispatch retries, failures and timeouts; do not log
those. With the CLI grant, log what it cannot see: a step you retried by hand,
an approach you abandoned, a tool or permission wall, a plan or rubric gap.

`satelle ledger append <id> --kind <kind> --payload '{"key": value}'`: a short
kind (`step-quality`, `retry`, `plan-gap`), numbers as JSON numbers, never a
secret. satelle carries the event on the item's chain and never reads it; the
kinds satelle reads itself are refused.

Run `ledger append` and `attach` with their output visible and check the
record was reported; a gate judges only evidence that landed.
