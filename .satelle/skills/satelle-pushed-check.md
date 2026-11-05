---
name: satelle-pushed-check
type: check
description: Rejects closing an item until the work is pushed — HEAD of the current branch is on its origin branch and the tree has no uncommitted tracked change. The session pushes before it presents done; this check only verifies. Deterministic, no agent.
---

# Pushed

Done includes pushed (operator, 2026-11-06): GitHub holds the code, so an item
is not done until its commits are on the remote branch. The session pushes the
branch (`git push origin <branch>`) before presenting the move to done; this
check refuses while HEAD is not contained in `origin/<branch>` or a tracked file
is uncommitted.

```sh
set -eu
branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$branch" = "HEAD" ]; then
  echo "detached HEAD: check out the item's branch and push it"
  exit 1
fi
if ! git diff --quiet HEAD --; then
  echo "uncommitted tracked changes on $branch: commit them, push, then present done"
  git status --short --untracked-files=no | head -20
  exit 1
fi
git fetch -q origin "$branch" 2>/dev/null || { echo "origin has no branch $branch: git push -u origin $branch"; exit 1; }
if ! git merge-base --is-ancestor HEAD "origin/$branch"; then
  echo "HEAD $(git rev-parse --short=12 HEAD) is not on origin/$branch ($(git rev-list --count "origin/$branch..HEAD") commit(s) unpushed): git push origin $branch"
  exit 1
fi
echo "HEAD $(git rev-parse --short=12 HEAD) is on origin/$branch"
```
