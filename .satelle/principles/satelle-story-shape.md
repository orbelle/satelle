---
name: satelle-story-shape
type: principle
tags: [type:principle]
applies_to: ["*"]
commands: story create
description: A story is one behaviour, written as a Feature (As a / I want / so that) with a Background; its acceptance criteria are Given/When/Then scenarios that meet that behaviour, each provable at the gate that judges it. Before filing, check the draft against each property satelle-story-review judges and file it only once it meets them.
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

A story that carries two behaviours is narrowed to one; the other is dropped, and becomes its own story only when the operator asks for it. A criterion no
wired gate can judge is narrowed until one can ([[satelle-start-small]]).

## The story is BDD

The behaviour is written as a **Feature**: *As a* role, *I want* a capability,
*so that* an outcome. A **Background** gives the context its scenarios share:
what exists, the evidence that raised it, a decision already made. Each
acceptance criterion is a **scenario**: *Given* a context, *When* an action,
*Then* an observable outcome. The story is judged in that shape before any plan
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

## Check the draft before you file it

Before `satelle story create --session <session>`, read the draft against each property that
story review judges ([[satelle-story-review]],
`.satelle/skills/satelle-story-review.md`), as that skill states them, and
fix every one the draft fails. File only a draft that meets them all. The
gate stays the judge; this check is so that it judges a shaped story.

Operator ruling, 2026-11-07.
