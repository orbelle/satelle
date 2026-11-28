---
name: satelle-cross-repo-plan-check
type: check
description: Deterministic check on a cross-repo story's plan -> split. Rejects a plan whose "## Split" section is missing or malformed, names a repository twice or one that is neither self nor registered, orders a child after a later one, lacks a "### Child <n>" section with criteria for a line, or has fewer than two children. No agent.
---

# Cross-repository plan check

Reads the newest `plan` attachment of `$SATELLE_ITEM` and checks the
`## Split` section the `cross-repo-plan` skill defines. The repositories a
child may name are the ones `satelle repo list --json --session <session>`
prints. Whether the
split fits the story is the split review's judgement, not this check's.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the plan cannot be checked"
  exit 1
fi
if ! plan=$(satelle story attachment "$sid" plan 2>&1); then
  echo "could not read the plan of $sid:"
  echo "$plan"
  exit 1
fi
if ! repos=$(satelle repo list --json 2>&1); then
  echo "could not list the repositories:"
  echo "$repos"
  exit 1
fi
known=$(printf '%s' "$repos" | jq -r '.repos[].name')
lines=$(printf '%s\n' "$plan" | awk '/^## Split[[:space:]]*$/ {s=1; next} s && /^#/ {s=0} s && /^- / {print}')
if [ -z "$lines" ]; then
  echo "the plan of $sid has no \"## Split\" section with child lines (see .satelle/skills/cross-repo-plan.md)"
  exit 1
fi
fail=0
want=1
seen=" "
while IFS= read -r line; do
  if ! printf '%s\n' "$line" | grep -Eq '^- [0-9]+ \| [A-Za-z0-9._-]+ \| after (-|[0-9]+(,[0-9]+)*) \| [^[:space:]].*$'; then
    echo "split line is not \"- <n> | <repo> | after <n,...|-> | <title>\": $line"
    fail=1
    continue
  fi
  n=$(printf '%s' "$line" | awk -F' [|] ' '{sub(/^- /, "", $1); print $1}')
  repo=$(printf '%s' "$line" | awk -F' [|] ' '{print $2}')
  after=$(printf '%s' "$line" | awk -F' [|] ' '{sub(/^after /, "", $3); print $3}')
  if [ "$n" -ne "$want" ]; then
    echo "split line $want is numbered $n: number the children 1, 2, ... in landing order"
    fail=1
  fi
  want=$((want + 1))
  if ! printf '%s\n' "$known" | grep -Fxq -- "$repo"; then
    echo "child $n names repository $repo, which is neither self nor registered in .satelle/repos.toml"
    fail=1
  fi
  case "$seen" in
    *" $repo "*) echo "repository $repo has more than one child: one child per repository"; fail=1 ;;
  esac
  seen="$seen$repo "
  if [ "$after" != "-" ]; then
    for a in $(printf '%s' "$after" | tr ',' ' '); do
      if [ "$a" -ge "$n" ]; then
        echo "child $n is after $a, which is not an earlier child"
        fail=1
      fi
    done
  fi
  crit=$(printf '%s\n' "$plan" | awk -v h="### Child $n" '$0 == h {s=1; next} s && /^#/ {s=0} s && /^- Given / {c++} END {print c+0}')
  if [ "$crit" -eq 0 ]; then
    echo "child $n has no \"### Child $n\" section with a \"- Given ...\" criterion"
    fail=1
  fi
done <<EOF
$lines
EOF
count=$((want - 1))
if [ "$count" -lt 2 ]; then
  echo "the split of $sid has $count child: a cross-repo story splits into at least two repositories"
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "the plan of $sid splits into $count children:"
printf '%s\n' "$lines"
exit 0
```
