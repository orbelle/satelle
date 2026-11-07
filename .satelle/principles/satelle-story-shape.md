---
name: satelle-story-shape
type: principle
tags: [type:principle]
applies_to: ["*"]
description: A story is one behaviour; its acceptance criteria meet that behaviour, and each criterion is provable at the gate that judges it.
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

Operator ruling, 2026-11-07.
