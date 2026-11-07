---
name: satelle-drift-run-trigger
type: check
description: Trigger of the drift-constitution and drift-claims gates on runbook's in_progress → done. Relevant (exit 0) only for a run of the drift review task; every other run (release, cleanup) skips both gates (exit 1). Deterministic, no agent.
---

# Drift run trigger

The drift gates judge only runs of the drift review task (its procedure is
`.satelle/skills/drift-review.md`, started by release.md step 11). Any other
task's run skips them.

```sh
set -u
task=tas_DRIFT_REVIEW
command -v jq >/dev/null 2>&1 || { echo "jq is not installed"; exit 2; }
parent=$(satelle execution get "$SATELLE_ITEM" --json | jq -r '.parent // empty') || { echo "cannot read $SATELLE_ITEM"; exit 2; }
if [ "$parent" = "$task" ]; then
  echo "$SATELLE_ITEM is a run of the drift review task $task"
  exit 0
fi
echo "$SATELLE_ITEM is a run of ${parent:-no task}, not the drift review task $task"
exit 1
```
