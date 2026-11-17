---
name: satelle-plan-touches-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check on plan → in_progress. Rejects when the story's plan has no "## Touches" section listing at least one path. Deterministic, zero LLM tokens; runs before plan-review so a plan without the section does not spend a reviewer turn.
---

# Plan touches check (plan-exit functional gate)

Rejects the move out of `plan` when the newest `plan` attachment has no `## Touches` section, or one that lists no `- <path>` line before the next heading. Whether the lines cover what the plan changes is plan-review's. The story is `$SATELLE_ITEM`; the plan is read with `satelle story attachment <id> plan --session <session>`.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the story's plan cannot be checked"
  exit 1
fi
if ! plan=$(satelle story attachment "$sid" plan 2>&1); then
  echo "could not read the plan of $sid:"
  echo "$plan"
  exit 1
fi
if ! printf '%s\n' "$plan" | grep -q '^## Touches[[:space:]]*$'; then
  echo "the plan of $sid has no \"## Touches\" section: the planner lists one path or glob per line, as - <path>, for every file the plan changes or adds"
  exit 1
fi
n=$(printf '%s\n' "$plan" | awk '/^## Touches[[:space:]]*$/ {in_s=1; next} in_s && /^#/ {in_s=0} in_s && /^- [^[:space:]]/ {n++} END {print n+0}')
if [ "$n" -eq 0 ]; then
  echo "the \"## Touches\" section of the plan of $sid lists no path: one - <path> line per file the plan changes or adds"
  exit 1
fi
echo "the plan of $sid declares $n path(s) under ## Touches"
exit 0
```
