---
name: satelle-start-small
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Ship the smallest change that proves the behaviour, then grow it in increments: prove, validate, scale.
---

# Start small

Make **the smallest change that proves the behaviour**, then grow it
incrementally: **prove -> validate -> scale**. Scope, parallelism or rules grow
only after the ledger shows the smaller step holds.

Operator, 2026-11-07: 'agile – smallest change, incremental, prove -> validate -> scale is a principle'.

Examples:

- **Docker before sprites.** Prove a run in a local container before running it
  on sprites.
- **Splitting oversized stories at groom.** A card too big for one behaviour is
  split before it is driven.
- **Narrowing a criterion no wired gate can judge.** The criterion shrinks to
  what a gate on the path can prove.

See [[satelle-story-shape]], [[satelle-agile-increments]].
