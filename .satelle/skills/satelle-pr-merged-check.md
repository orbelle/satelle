---
name: satelle-pr-merged-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate on the pr route's close (pr_open → done). Passes when the story's pr:<n> pull request is MERGED into main. The team PR remote performs the merge and release; this gate only verifies the world. Without gh it rejects.
---

# PR merged check (pr route close gate)

**Functional-check** on entry to `done` from `pr_open`. On the pr
route the team PR remote on satelle.dev reviews, merges, releases and closes the
story; its own workflow judges that work. Locally the close has one question:
was this story's pull request merged into `main`?

- the story has exactly one `pr:<n>` tag;
- pull request `<n>` is `MERGED`, base `main`, head `epic/<id>`.

A pull request the remote denied is not merged: the remote parks the story at
`blocked` (origin `pr_open`) instead, so this gate is never the place a denial
is decided. It runs no merge. Without `gh` it REJECTS.

```check
#!/usr/bin/env bash
set -uo pipefail
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "PR MERGED FAILED: no story id in the payload"
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
  echo "PR MERGED FAILED: could not read satelle story get $sid"
  exit 1
fi
count=$(printf '%s\n' "$prs" | grep -c .)
if [ "$count" -ne 1 ]; then
  echo "PR MERGED FAILED: $sid has $count pr:<n> tags; want exactly one"
  exit 1
fi
n=$prs

if ! command -v gh >/dev/null 2>&1; then
  echo "PR MERGED FAILED: gh is not installed, so pull request #$n cannot be verified"
  exit 1
fi
view=$(gh pr view "$n" --json state,baseRefName,headRefName \
  -q '"\(.state) \(.baseRefName) \(.headRefName)"' 2>/dev/null)
if [ -z "$view" ]; then
  echo "PR MERGED FAILED: gh pr view $n returned nothing (no such pull request, or gh is not signed in)"
  exit 1
fi
read -r state base head <<< "$view"
if [ "$base" != "main" ] || [ "$head" != "epic/$sid" ]; then
  echo "PR MERGED FAILED: pull request #$n is $head → $base; want epic/$sid → main"
  exit 1
fi
if [ "$state" != "MERGED" ]; then
  echo "PR MERGED FAILED: pull request #$n is $state; the PR remote merges it, then this edge closes"
  exit 1
fi
echo "pr merged PASS: $sid → pull request #$n (epic/$sid → main) is MERGED"
exit 0
```
