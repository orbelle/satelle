---
name: satelle-split-authored-process
type: principle
tags: [type:principle, principles:seat]
applies_to: ["*"]
description: In this repo, a change that needs both new binary mechanism and an edit to the authored process under .satelle/ is two stories — the feature, and a substrate story for the .satelle/ half that depends on it.
---

# Split the authored half

`.satelle/` is gitignored here and only a substrate story may edit it, so a
feature story cannot change this repo's skills, workflows or principles, and CI
never sees them. When a story needs both new mechanism and an authored-process
change, the authored half is its own `category: substrate` story that depends
on the feature story, named in both definitions — decided when the story is
planned or readied, never found mid-implementation.
