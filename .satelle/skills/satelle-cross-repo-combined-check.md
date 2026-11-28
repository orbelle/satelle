---
name: satelle-cross-repo-combined-check
type: check
description: Deterministic check on a cross-repo story's underway -> done. Requires the parent's combined-change attachment to list every done child with the frozen base and head that satelle story diff reports for it now, and every cancelled child as cancelled, so the judge reads the children's actual changes. No agent.
---

# Cross-repository combined change check

Runs after `children-closed`, so every child of `$SATELLE_ITEM` is terminal.
For each child it reads the frozen change (`satelle story diff <child>
--json --session <session>`) and requires the matching line in the parent's `combined-change`
attachment, in the format `cross-repo-underway` defines. A stale or missing
line is rejected, naming the child.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the combined change cannot be checked"
  exit 1
fi
combined=$(satelle story attachment "$sid" combined-change 2>/dev/null || true)
if [ -z "$combined" ]; then
  echo "$sid has no combined-change attachment (see .satelle/skills/cross-repo-underway.md)"
  exit 1
fi
if ! kids=$(satelle story list --parent "$sid" --json 2>&1); then
  echo "could not list the children of $sid:"
  echo "$kids"
  exit 1
fi
rows=$(printf '%s' "$kids" | jq -r '.[] | "\(.id) \(.repo // "self") \(.status)"')
if [ -z "$rows" ]; then
  echo "$sid has no children: a cross-repo story closes after the children its split created"
  exit 1
fi
fail=0
done_n=0
while read -r cid repo status; do
  if [ "$status" = "cancelled" ]; then
    want="- $cid $repo cancelled"
  else
    if ! d=$(satelle story diff "$cid" --json 2>&1); then
      echo "could not read the change of $cid:"
      echo "$d"
      fail=1
      continue
    fi
    frozen=$(printf '%s' "$d" | jq -r '.frozen // false')
    if [ "$frozen" != "true" ]; then
      echo "$cid ($status) has no frozen change"
      fail=1
      continue
    fi
    want="- $cid $repo $(printf '%s' "$d" | jq -r '.base')..$(printf '%s' "$d" | jq -r '.head')"
    done_n=$((done_n + 1))
  fi
  if ! printf '%s\n' "$combined" | grep -Fxq -- "$want"; then
    echo "combined-change lacks the line \"$want\""
    fail=1
  fi
done <<EOF
$rows
EOF
if [ "$done_n" -eq 0 ]; then
  echo "no child of $sid is done: there is no combined change to judge"
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "combined-change of $sid carries the current frozen change of its $done_n done child(ren)"
exit 0
```
