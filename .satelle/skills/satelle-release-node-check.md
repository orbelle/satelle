---
name: satelle-release-node-check
type: check
description: The check of the release-node workflow. Runs the node's part of the release (.satelle/skills/satelle-release-node/scripts/release-node.sh run) on the release-sprite node and exits with its code; its output is the compact result, naming the step that failed. Deterministic, no agent.
---

# The release's node part

The `release-sprite` remote drives one execution of its task on the
`release-node` workflow (`satelle workflow show release-node --session
<session>`); this check runs on its `backlog → done` move, after
`change-judged`. Its steps and what each refuses are
`.satelle/skills/satelle-release-node/SKILL.md`.

```sh
bash .satelle/skills/satelle-release-node/scripts/release-node.sh run
```
