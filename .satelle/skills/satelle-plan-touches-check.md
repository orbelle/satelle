---
name: satelle-plan-touches-check
type: check
description: Functional check on plan → in_progress and on in_progress → integration. Rejects when the story's plan has no "## Touches" section listing at least one path, and rejects when the item's changed paths include one the section does not name, reading the item's target repository when it targets one. Deterministic, zero LLM tokens; on plan exit it runs before plan-review so a plan without the section does not spend a reviewer turn.
---

# Plan touches check

The story is `$SATELLE_ITEM`; its plan is read with `satelle story attachment <id> plan --session <session>`.

1. **The section.** The move is rejected when the newest `plan` attachment has no `## Touches` section, or one that lists no `- <path>` line before the next heading. Whether the lines cover what the plan changes is plan-review's.
2. **The changed paths.** The item's changed paths are compared with the section's entries. A changed path is in scope when it equals an entry, matches it as a shell glob, or lies under an entry that ends with `/`. An entry is the first word of its `- ` line, with backticks removed. The move is rejected naming every changed path no entry covers; the fix is a re-plan that names the path, not a wider diff.

The changed paths are those `satelle help workflows --session <session>` defines for a check: the item's change since `$SATELLE_BASE`. For an item that targets a registered repository they are read in the checkout `$SATELLE_WORKTREE` names; for a self item, in the working directory. On plan → in_progress the tree-unchanged gate has already refused any change, so step 2 finds none there and the check judges only step 1. Step 2 bites on in_progress → integration, where the code exists, so every delivery story's code, self or target, stays within its plan's `## Touches`.

A changed path that is a submodule is read as the gitlink alone; the submodule's own work tree is not inspected.

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
entries=$(printf '%s\n' "$plan" | awk '/^## Touches[[:space:]]*$/ {in_s=1; next} in_s && /^#/ {in_s=0} in_s && /^- [^[:space:]]/ {sub(/^- /, ""); gsub(/`/, ""); split($0, w, /[[:space:]]+/); print w[1]}')
n=$(printf '%s\n' "$entries" | grep -c .)
if [ "$n" -eq 0 ]; then
  echo "the \"## Touches\" section of the plan of $sid lists no path: one - <path> line per file the plan changes or adds"
  exit 1
fi
base=${SATELLE_BASE:-}
if [ -z "$base" ]; then
  echo "the plan of $sid declares $n path(s) under ## Touches; the item has no base commit, so there are no changed paths to compare"
  exit 0
fi
dir=${SATELLE_WORKTREE:-.}
if ! changed=$(git -C "$dir" -c core.fsmonitor=false diff --no-ext-diff --no-textconv --ignore-submodules=dirty --submodule=short --name-only "$base" 2>&1); then
  echo "could not list the changed paths of $sid since $base in $dir:"
  echo "$changed"
  exit 1
fi
outside=()
while IFS= read -r p; do
  [ -n "$p" ] || continue
  covered=0
  while IFS= read -r e; do
    [ -n "$e" ] || continue
    if [ "$p" = "$e" ]; then covered=1; break; fi
    case "$e" in
      */) case "$p" in "$e"*) covered=1; break ;; esac ;;
    esac
    if [[ $p == $e ]]; then covered=1; break; fi
  done <<< "$entries"
  [ "$covered" -eq 1 ] || outside+=("$p")
done <<< "$changed"
if [ "${#outside[@]}" -gt 0 ]; then
  echo "$sid changes ${#outside[@]} path(s) its plan's ## Touches does not name; re-plan to name each path, or take the change out:"
  for p in "${outside[@]}"; do echo "  $p"; done
  exit 1
fi
m=$(printf '%s\n' "$changed" | grep -c .)
echo "the plan of $sid declares $n path(s) under ## Touches, and all $m changed path(s) since ${base:0:12} are within them"
exit 0
```
