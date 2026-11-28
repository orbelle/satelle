---
name: satelle-cross-repo-split-check
type: check
description: Deterministic check on a cross-repo story's split -> underway. For each line of the plan's "## Split" section it requires exactly one child tagged split:<n> with that repo and a depends-on tag for each earlier child it is after, no other open or done child, and a split attachment on the parent that records each child. No agent.
---

# Cross-repository split check

Compares the children of `$SATELLE_ITEM` (`satelle story list --parent
<id> --json`) with the `## Split` section of its `plan` attachment and with its
`split` attachment, in the formats `cross-repo-plan` and `cross-repo-split`
define. It rejects naming each line whose child is missing, doubled, on another
repository or out of order.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the split cannot be checked"
  exit 1
fi
if ! plan=$(satelle story attachment "$sid" plan 2>&1); then
  echo "could not read the plan of $sid:"
  echo "$plan"
  exit 1
fi
if ! all=$(satelle story list --parent "$sid" --json 2>&1); then
  echo "could not list the children of $sid:"
  echo "$all"
  exit 1
fi
kids=$(printf '%s' "$all" | jq '[.[] | select(.status != "cancelled")]')
record=$(satelle story attachment "$sid" split 2>/dev/null || true)
if [ -z "$record" ]; then
  echo "$sid has no split attachment recording its children (see .satelle/skills/cross-repo-split.md)"
  exit 1
fi
lines=$(printf '%s\n' "$plan" | awk '/^## Split[[:space:]]*$/ {s=1; next} s && /^#/ {s=0} s && /^- / {print}')
if [ -z "$lines" ]; then
  echo "the plan of $sid has no \"## Split\" lines"
  exit 1
fi
fail=0
count=0
ids=" "
map=""
while IFS= read -r line; do
  n=$(printf '%s' "$line" | awk -F' [|] ' '{sub(/^- /, "", $1); print $1}')
  repo=$(printf '%s' "$line" | awk -F' [|] ' '{print $2}')
  after=$(printf '%s' "$line" | awk -F' [|] ' '{sub(/^after /, "", $3); print $3}')
  count=$((count + 1))
  match=$(printf '%s' "$kids" | jq -r --arg t "split:$n" '[.[] | select((.tags // []) | index($t))] | map(.id) | join(" ")')
  set -- $match
  ids="$ids$match "
  if [ "$#" -ne 1 ]; then
    echo "split line $n ($repo) has $# children tagged split:$n, not one: $match"
    fail=1
    continue
  fi
  cid=$1
  map="$map $n=$cid"
  crepo=$(printf '%s' "$kids" | jq -r --arg id "$cid" '.[] | select(.id == $id) | .repo // "self"')
  if [ "$crepo" != "$repo" ]; then
    echo "child $cid (split line $n) targets $crepo, not $repo"
    fail=1
  fi
  if [ "$after" != "-" ]; then
    for a in $(printf '%s' "$after" | tr ',' ' '); do
      dep=$(printf '%s\n' $map | awk -F= -v a="$a" '$1 == a {print $2}')
      if [ -z "$dep" ]; then
        echo "child $cid (split line $n) is after line $a, which has no single child"
        fail=1
        continue
      fi
      has=$(printf '%s' "$kids" | jq -r --arg id "$cid" --arg t "depends-on:$dep" '.[] | select(.id == $id) | (.tags // []) | index($t) != null')
      if [ "$has" != "true" ]; then
        echo "child $cid (split line $n) lacks the tag depends-on:$dep (line $a)"
        fail=1
      fi
    done
  fi
  if ! printf '%s\n' "$record" | grep -Fxq -- "- $n $cid $repo"; then
    echo "the split attachment of $sid lacks the line \"- $n $cid $repo\""
    fail=1
  fi
done <<EOF
$lines
EOF
extra=$(printf '%s' "$kids" | jq -r '.[].id' | while read -r k; do
  case "$ids" in *" $k "*) ;; *) printf '%s ' "$k" ;; esac
done)
if [ -n "$extra" ]; then
  echo "$sid has children no split line names: $extra"
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "$sid has one child per split line ($count), each on its repository and in the plan's order:"
printf '%s\n' "$record"
exit 0
```
