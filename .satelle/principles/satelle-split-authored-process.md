---
name: satelle-split-authored-process
type: principle
tags: [type:principle, principles:seat]
applies_to: ["*"]
description: In this repo, a change that needs both new binary mechanism and an edit to the authored process under .satelle/ is two stories — the feature, and a substrate story for the .satelle/ half that depends on it. A story carries its own test changes: the tests under tests/ that its change makes assert the wrong behaviour change in the same story.
---

# Split the authored half

`.satelle/` is gitignored here and only a substrate story may edit it, so a
feature story cannot change this repo's skills, workflows or principles, and CI
never sees them. When a story needs both new mechanism and an authored-process
change, the authored half is its own `category: substrate` story that depends
on the feature story, named in both definitions — decided when the story is
planned or readied, never found mid-implementation.

**A story carries its own test changes** (operator, 2026-11-25). The split is
binary mechanism against authored process, not code against tests. When a
change makes an existing test under `tests/` assert the wrong behaviour, that
test changes in the same story and branch: a substrate story may change the
tests that pin the substrate it changes, beside `.satelle/`, and a feature
story changes the tests its code needs. Binary source outside `tests/` never
rides on a substrate story. Updating or removing a test is part of the plan or
of coding, with its reason, and the judged review refuses a test weakened
without one. Where the substrate route checks it is in
`satelle workflow show substrate --session <session>`.
