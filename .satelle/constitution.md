---
type: constitution
title: Project constitution
description: satelle runs any repo's process as configuration; the binary is mechanism only. This repo is its dogfood. Work is behaviour-driven and ships in small proven slices.
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
  Judges: [[satelle-agent-alt-reviewer]].
- Filing: [[satelle-chore-category]], [[satelle-group-related-stories]].
  Evidence: [[satelle-agent-telemetry]].
- Working here: [[satelle-agile-increments]], [[satelle-broken-windows]],
  [[satelle-diagnostic-discipline]], [[satelle-parallel-driving]].
