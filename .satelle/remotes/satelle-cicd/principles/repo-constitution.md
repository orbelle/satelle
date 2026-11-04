---
name: repo-constitution
type: principle
tags: [type:principle]
applies_to: ["*"]
description: orbelle/satelle's constitution, carried in the satelle-cicd bundle for satelle pr approve's cold review. A copy of .satelle/constitution.md; release.sh refuses a release when the two bodies differ.
---

# satelle constitution

satelle runs any repo's process as configuration: the binary is mechanism, and
process, gates and opinions are substrate under `.satelle/`. This repo builds
satelle and dogfoods it.

- Each story is one behaviour whose ACs meet it, provable at their gate
  ([[satelle-story-shape]]), shipped as the smallest slice that proves it
  ([[satelle-start-small]]).
- Binary or substrate: [[satelle-mechanism-vs-substrate]]. Any agent CLI:
  [[satelle-agent-agnostic]]. Remotes: [[satelle-agent-operates-remotes]].
- Filing: [[satelle-chore-category]], [[satelle-group-related-stories]].
  Evidence: [[satelle-agent-telemetry]].
- Working here: [[satelle-agile-increments]], [[satelle-broken-windows]],
  [[satelle-diagnostic-discipline]], [[satelle-parallel-driving]].
