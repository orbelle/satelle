---
name: satelle-workflow-change-trigger
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: The `when` precondition for satelle-workflow-change-review. Exits 0 when the story's change set touches a workflow file, exits 1 when it touches none, so the review is not dispatched only to answer workflow:n/a. Deterministic and self-contained; the path pattern is the script's first line.
---

# Workflow-change trigger (`when` precondition)

Not a verdict. A gate that declares `when = "satelle-workflow-change-trigger"`
runs this script before it is enqueued: **exit 0 runs the gate, exit 1 skips
it**, any other outcome runs it (a broken precondition never costs a gate).

The story's change set is the union of `satelle story diff <id> --recorded` and
`satelle story diff <id> --include-substrate`. When any path in it matches
`WORKFLOW_PATHS` (this repo's route files, and the embedded default route it
keeps in-tree), the review runs. Otherwise it is skipped.

```check
#!/usr/bin/env bash
set -uo pipefail

# THE CONFIGURATION — the paths whose change is a route edit.
WORKFLOW_PATHS='^\.satelle/workflows/|^internal/config/substrate/workflows/'

payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload"
  exit 2
fi

extract_files() {
  local raw
  raw=$(cat)
  [ -z "$raw" ] && return 0
  printf '%s' "$raw" | tr -d '\n' | sed -n 's/.*"files":[[:space:]]*\[\([^]]*\)\].*/\1/p' | tr ',' '\n' | sed 's/^[[:space:]]*"//; s/"[[:space:]]*$//' | grep -v '^$' || true
}

ok=0
rec=""
if recorded=$(satelle story diff "$sid" --recorded --json 2>/dev/null); then
  rec=$(printf '%s' "$recorded" | extract_files); ok=1
fi
liv=""
if live=$(satelle story diff "$sid" --include-substrate --json 2>/dev/null); then
  liv=$(printf '%s' "$live" | extract_files); ok=1
fi
if [ "$ok" -eq 0 ]; then
  echo "could not read the change set of $sid; running the review"
  exit 2
fi

files=$(printf '%s\n%s\n' "$rec" "$liv" | grep -v '^$' | sort -u | grep -E "$WORKFLOW_PATHS" || true)
if [ -z "$files" ]; then
  echo "no workflow file changed for $sid; workflow-change review not needed"
  exit 1
fi
echo "workflow files changed for $sid: $(printf '%s' "$files" | tr '\n' ' ')"
exit 0
```
