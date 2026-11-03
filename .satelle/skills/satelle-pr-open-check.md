---
name: satelle-pr-open-check
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate on the pr route's hand-off (pr_raising → pr_open). Passes when the story carries a pr:<n> tag whose pull request is OPEN on base main from head branch epic/<id>, and the local epic/<id>, origin/epic/<id> and the PR head are one commit. Verifies, never performs. Without gh it rejects.
---

# PR open check (pr route hand-off gate)

**Functional-check** on entry to `pr_open`. `pr_open` is the
hand-off to the team PR remote on satelle.dev, which finds the story by its
`epic/<id>` branch and its `pr:<n>` tag. Both must therefore be true before the
story leaves this machine:

- the story has exactly one `pr:<n>` tag;
- pull request `<n>` is `OPEN`, its base is `main` and its head is `epic/<id>`;
- the local `epic/<id>` branch, `origin/epic/<id>` and the pull request's
  head are the same commit, so nothing committed for the story is left
  unpushed.

It runs no `git push` and opens no pull request. Without `gh` it REJECTS:
"could not check" never reads as "fine".

```check
#!/usr/bin/env bash
set -uo pipefail
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "PR OPEN FAILED: no story id in the payload"
  exit 1
fi

prs=$(satelle story get "$sid" --json 2>/dev/null | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(2)
for t in d.get("tags") or []:
    if t.startswith("pr:"):
        print(t[3:])
')
if [ $? -eq 2 ]; then
  echo "PR OPEN FAILED: could not read satelle story get $sid"
  exit 1
fi
count=$(printf '%s\n' "$prs" | grep -c .)
if [ "$count" -ne 1 ]; then
  echo "PR OPEN FAILED: $sid has $count pr:<n> tags; the raise-pr step must tag exactly one"
  exit 1
fi
n=$prs

if ! command -v gh >/dev/null 2>&1; then
  echo "PR OPEN FAILED: gh is not installed, so pull request #$n cannot be verified"
  exit 1
fi
view=$(gh pr view "$n" --json state,baseRefName,headRefName,headRefOid \
  -q '"\(.state) \(.baseRefName) \(.headRefName) \(.headRefOid)"' 2>/dev/null)
if [ -z "$view" ]; then
  echo "PR OPEN FAILED: gh pr view $n returned nothing (no such pull request, or gh is not signed in)"
  exit 1
fi
read -r state base head oid <<< "$view"
branch="epic/$sid"
if [ "$state" != "OPEN" ]; then
  echo "PR OPEN FAILED: pull request #$n is $state, not OPEN"
  exit 1
fi
if [ "$base" != "main" ] || [ "$head" != "$branch" ]; then
  echo "PR OPEN FAILED: pull request #$n is $head → $base; want $branch → main"
  exit 1
fi
local_sha=$(git rev-parse --verify -q "refs/heads/$branch" 2>/dev/null)
if [ -z "$local_sha" ]; then
  echo "PR OPEN FAILED: there is no local $branch branch; the raise-pr step commits the slice on it"
  exit 1
fi
remote=$(git ls-remote origin "refs/heads/$branch" 2>/dev/null | awk '{print $1}')
if [ -z "$remote" ]; then
  echo "PR OPEN FAILED: $branch is not on origin; push it, then retry this edge"
  exit 1
fi
if [ "$local_sha" != "$remote" ]; then
  echo "PR OPEN FAILED: local $branch is $local_sha but origin/$branch is $remote; push the branch, then retry this edge"
  exit 1
fi
if [ "$remote" != "$oid" ]; then
  echo "PR OPEN FAILED: origin/$branch is $remote but pull request #$n heads $oid; wait for the PR to catch up, then retry"
  exit 1
fi
echo "pr open PASS: $sid → pull request #$n ($branch → main, head $oid) is OPEN"
exit 0
```
