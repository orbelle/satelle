---
name: satelle-parallel-driving
type: principle
tags: [type:principle]
applies_to: ["*"]
description: When stories do run at once, act from each story's own worktree, keep live satelle runs away from a running integration suite, and overlap only what touches no shared host state.
---

# Parallel driving

Prove one slice before running many on the same code ([[satelle-start-small]]);
an epic's children are driven to `approved` and released together, by the
driving session, which fires the release queue ([[satelle-agent-goals]],
*Driving an epic*). When several stories do run at once:

1. **Act from the story's own worktree.** An edit, commit or command is
   attributed by the tree it targets; run each story's work from its worktree,
   one story per command.
2. **Keep live runs away from a running integration suite.** A `satelle update --session <session>`,
   dogfood or scratch-repo run while a suite runs fails it for a reason outside
   the slice. The suite runs alone.
3. **Overlap only what touches no shared host state**: readiness rounds,
   dispatched coders, unit tests and reviews of different stories. Record what
   overlapped when a failure might be interference.
