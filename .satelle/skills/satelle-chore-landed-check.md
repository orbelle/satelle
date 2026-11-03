---
name: satelle-chore-landed-check
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate on a chore's close (landing → done). Passes when the story changed no tracked file; otherwise requires those files committed, HEAD on origin/main, and the test workflow green for HEAD. No .version, changelog, release tag or dogfood requirement. Verifies, never performs.
---

# Chore landed check (chore close gate)

**Functional-check** on entry to `done` from the chore lane's `landing` step.
A chore ships nothing, so this gate replaces the release close
gates (changelog entry, published CI release, dogfood) with one question: did
whatever tracked change the chore made reach main with CI green?

- The slice is `satelle story diff <id> --recorded`, minus paths git ignores.
  Nothing left means nothing to land: PASS.
- Otherwise every slice path must be committed (no worktree change), `HEAD` must
  be on `origin/main`, and the `test` workflow run for `HEAD` must have
  completed successfully.

It runs no `git push` and triggers no workflow. Without `gh` it REJECTS when
there is something to verify: "could not check" never reads as "fine".

```check
#!/usr/bin/env bash
set -uo pipefail
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "CHORE LANDED FAILED: no story id in the payload"
  exit 1
fi

diff_json=$(satelle story diff "$sid" --recorded --json 2>/dev/null)
files=$(printf '%s' "$diff_json" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(2)
for f in d.get("files") or []:
    print(f["path"] if isinstance(f, dict) else f)
')
if [ $? -eq 2 ]; then
  echo "CHORE LANDED FAILED: could not read satelle story diff $sid --recorded"
  exit 1
fi

tracked=()
while IFS= read -r f; do
  [ -z "$f" ] && continue
  git check-ignore -q -- "$f" 2>/dev/null && continue
  tracked+=("$f")
done <<< "$files"

if [ ${#tracked[@]} -eq 0 ]; then
  echo "chore landed PASS: $sid changed no tracked file; nothing to land"
  exit 0
fi

dirty=$(git status --porcelain -- "${tracked[@]}" 2>/dev/null)
if [ -n "$dirty" ]; then
  echo "CHORE LANDED FAILED (not committed): the slice has uncommitted changes:"
  printf '%s\n' "$dirty" | sed 's/^/  /'
  exit 1
fi

sha=$(git rev-parse HEAD 2>/dev/null)
if ! git merge-base --is-ancestor "$sha" origin/main 2>/dev/null; then
  echo "CHORE LANDED FAILED (not on main): HEAD $sha is not on origin/main"
  echo "  push the slice, let CI finish, then retry this edge"
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "CHORE LANDED FAILED: gh is not installed, so CI for HEAD $sha cannot be verified"
  exit 1
fi
line=$(gh run list --commit "$sha" --workflow test --limit 1 \
         --json status,conclusion,databaseId \
         -q '.[0] | "\(.databaseId) \(.status) \(.conclusion // "-")"' 2>/dev/null)
if [ -z "$line" ]; then
  echo "CHORE LANDED FAILED: no test workflow run found for HEAD $sha"
  exit 1
fi
set -- $line
if [ "$2" != "completed" ]; then
  echo "CHORE LANDED FAILED: test run $1 is still $2 — wait for it, then retry the edge"
  exit 1
fi
if [ "$3" != "success" ]; then
  echo "CHORE LANDED FAILED: test run $1 concluded $3"
  exit 1
fi
echo "chore landed PASS: ${#tracked[@]} tracked file(s) committed, HEAD $sha pushed, test run $1 green"
```
