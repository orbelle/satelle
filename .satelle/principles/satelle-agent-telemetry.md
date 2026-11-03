---
name: satelle-agent-telemetry
type: principle
tags: [type:principle]
applies_to: ["*"]
description: With the satelle CLI grant, record what only you can judge (a retry, abandoned approach or wall in your own work; a step that fought you) via `satelle story log <id> --kind <kind> --data k=v`, and see every log, attach and message land.
---

# Self-report what only you can judge

The binary already records dispatch retries, failures and timeouts; do not log
those. With the CLI grant, log what it cannot see: a step you retried by hand,
an approach you abandoned, a tool or permission wall, a plan or rubric gap.

`satelle story log <id> --kind <kind> --data key=value`: a short kind
(`step-quality`, `retry`, `plan-gap`), numbers as numbers, never a secret.

Run `story log`, `attach` and `message` with their output visible and check the
record was reported; a gate judges only evidence that landed.
