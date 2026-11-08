---
name: satelle-workflow-change-trigger
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: The trigger of satelle-workflow-change-review. Exits 0 when the story's diff touches a workflow file, exits 1 when it touches none, so the review is not dispatched only to answer workflow:n/a. Deterministic and self-contained; the path pattern is the script's first line.
---

# Workflow-change trigger

Not a verdict. A gate that declares `trigger = "satelle-workflow-change-trigger"`
runs this script before it is enqueued: **exit 0 runs the gate, exit 1 skips
it** and its output is noted, and any other exit means the trigger is broken,
so the gate runs anyway.

The story's change set is the `paths` of `satelle story diff <id> --json`, the
id being `$SATELLE_ITEM`. When any path in it matches `WORKFLOW_PATHS`, the
review runs. Otherwise it is skipped. `WORKFLOW_PATHS` names the two places
workflows are loaded from (`internal/kernel/config.go`, `WorkflowsDir`): this
repo's route files, `.satelle/workflows/`, and the default route `satelle init`
writes into a new repo, embedded from `internal/kernel/template/workflows/`.
An item that is not a story (an execution, `exe_`) has no story diff: it is
skipped, exit 1, with a line saying so.

```check
#!/usr/bin/env bash
set -uo pipefail

# THE CONFIGURATION — the paths whose change is a route edit.
WORKFLOW_PATHS='^\.satelle/workflows/|^internal/kernel/template/workflows/'

command -v jq >/dev/null 2>&1 || { echo "jq is not installed"; exit 2; }
item=${SATELLE_ITEM:-}
if ! printf '%s' "$item" | grep -qE '^[a-z]+_[A-Za-z0-9]+$'; then
  echo "SATELLE_ITEM (${item:-unset}) is not an item id"
  exit 2
fi
case $item in
  sto_*) sid=$item ;;
  *) echo "$item is not a story: it has no story diff to judge; workflow-change review not needed"; exit 1 ;;
esac
if ! diff=$(satelle story diff "$sid" --json 2>&1); then
  echo "could not read the diff of $sid: $diff"
  exit 2
fi
if ! all=$(printf '%s' "$diff" | jq -r '.paths // [] | .[]'); then
  echo "could not read the paths of $sid's diff"
  exit 2
fi

files=$(printf '%s\n' "$all" | grep -E "$WORKFLOW_PATHS" || true)
if [ -z "$files" ]; then
  echo "no workflow file changed for $sid; workflow-change review not needed"
  exit 1
fi
echo "workflow files changed for $sid: $(printf '%s' "$files" | tr '\n' ' ')"
exit 0
```
