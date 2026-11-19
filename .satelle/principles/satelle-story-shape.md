---
name: satelle-story-shape
type: principle
tags: [type:principle]
applies_to: ["*"]
commands: story create
description: A story is one behaviour, written as a Feature (As a / I want / so that) with a Background; its acceptance criteria are Given/When/Then scenarios that meet that behaviour, each provable at the gate that judges it.
---

# Story shape

A story is **one behaviour whose ACs meet it, each provable at its gate**. The
behaviour names a role, a context and an outcome; it is not a mechanism, a file
or a list of tasks.

Its acceptance criteria together **meet** that behaviour, no narrower and no
wider. Each criterion is one requirement, **traceable** to the behaviour and
**provable at its gate**: the check's script or the reviewer's evidence can
judge it at that step. Evidence that exists only after release belongs on the
release step, not on a code-step criterion.

A card that carries two behaviours is split into two stories. A criterion no
wired gate can judge is narrowed until one can ([[satelle-start-small]]).

## The card is BDD

The behaviour is written as a **Feature**: *As a* role, *I want* a capability,
*so that* an outcome. A **Background** gives the context its scenarios share:
what exists, the evidence that raised it, a decision already made. Each
acceptance criterion is a **scenario**: *Given* a context, *When* an action,
*Then* an observable outcome. The card is judged in that shape before any plan
(`satelle-story-review`).

```text
Feature: a held story names its next move
  As an operator
  I want a story held at its round cap to show the command that resumes it
  So that I can act on the hold without reading the ledger

Background:
  A state's hold moves an item to blocked after its cap of rejected rounds.

Scenario: the held story prints its resume command
  Given a delivery story held at blocked from plan
  When I show the story
  Then the output names the move back to plan
```

Operator ruling, 2026-11-07.
