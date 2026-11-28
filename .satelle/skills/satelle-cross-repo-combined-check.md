---
name: satelle-cross-repo-combined-check
type: check
description: Deterministic check of a cross-repo story's combined change. Requires the parent's combined-change attachment to list every done child with the frozen base and head that satelle story diff reports for it now, to carry each done child's diff body exactly as that repository's git produces it for that change, and to list every cancelled child as cancelled, so the judge reads the children's actual changes. No agent.
---

# Cross-repository combined change check

Where it runs, and what has already been checked by then, is in
`satelle workflow show cross-repo --session <session>`; it expects every child
of `$SATELLE_ITEM` to be terminal. For each child it reads the frozen change (`satelle story diff <child>
--json --session <session>`) and checks the parent's `combined-change`
attachment in the format `cross-repo-underway` defines:

1. **The header.** The `## Children` line for the child must match its frozen
   base and head, or say `cancelled` for a cancelled child.
2. **The body.** For a done child, the `## <child id>` section must equal
   `git -C <checkout> diff --no-ext-diff --no-textconv <base> <head>`, where
   `<checkout>` is the child's repository's path in `satelle repo list --json
   --session <session>`. One surrounding ```` ``` ```` fence and trailing blank
   lines are ignored; nothing else is.

A stale or missing header, a missing section, or a body that differs from the
recomputed diff is rejected, naming the child.

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
repos=""
section() {
  printf '%s\n' "$combined" | awk -v h="## $1" '
    $0 == h { on = 1; next }
    on && /^## / { exit }
    on { print }
  ' | awk '
    NR == 1 && /^```[A-Za-z]*$/ { fenced = 1; next }
    { line[++n] = $0 }
    END {
      while (n > 0 && line[n] ~ /^[[:space:]]*$/) n--
      if (fenced && n > 0 && line[n] == "```") n--
      while (n > 0 && line[n] ~ /^[[:space:]]*$/) n--
      for (i = 1; i <= n; i++) print line[i]
    }
  '
}
fail=0
done_n=0
while read -r cid repo status; do
  if [ "$status" = "cancelled" ]; then
    want="- $cid $repo cancelled"
    if ! printf '%s\n' "$combined" | grep -Fxq -- "$want"; then
      echo "combined-change lacks the line \"$want\""
      fail=1
    fi
    continue
  fi
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
  base=$(printf '%s' "$d" | jq -r '.base')
  head=$(printf '%s' "$d" | jq -r '.head')
  done_n=$((done_n + 1))
  want="- $cid $repo $base..$head"
  if ! printf '%s\n' "$combined" | grep -Fxq -- "$want"; then
    echo "combined-change lacks the line \"$want\""
    fail=1
    continue
  fi
  if [ -z "$repos" ]; then
    if ! repos=$(satelle repo list --json 2>&1); then
      echo "could not list the registered repositories:"
      echo "$repos"
      exit 1
    fi
  fi
  checkout=$(printf '%s' "$repos" | jq -r --arg r "$repo" '.repos[] | select(.name == $r) | .local.path // empty')
  if [ -z "$checkout" ]; then
    echo "$cid targets $repo, which has no registered local checkout on this machine, so its diff cannot be recomputed"
    fail=1
    continue
  fi
  if ! printf '%s\n' "$combined" | grep -Fxq -- "## $cid"; then
    echo "combined-change has no \"## $cid\" section holding its diff"
    fail=1
    continue
  fi
  if ! expect=$(git -C "$checkout" -c core.fsmonitor=false diff --no-ext-diff --no-textconv "$base" "$head" 2>&1); then
    echo "could not recompute the diff of $cid in $checkout ($base..$head):"
    echo "$expect"
    fail=1
    continue
  fi
  got=$(section "$cid")
  if [ "$got" != "$expect" ]; then
    echo "the \"## $cid\" section of combined-change differs from the diff of $cid's frozen change ($repo $base..$head); write it again from: git -C <checkout> diff --no-ext-diff --no-textconv $base $head"
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
echo "combined-change of $sid carries the current frozen change and diff of its $done_n done child(ren)"
exit 0
```
