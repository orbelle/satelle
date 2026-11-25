---
name: satelle-replan-base-check
type: check
description: Refuses a story's move from coding back to planning while its branch's tree differs, outside .satelle/runs, from where it left its base branch, and names the forward commit that sets the code aside under the seat the driver still holds; once that commit returns the tree to the fork, the move is accepted, the earlier code stays in history, and satelle-tree-unchanged-check later sees an unchanged tree. Deterministic, no agent; reads git and the ledger, writes nothing.
---

# Re-plan base (code is set aside before a story returns to planning)

A story can move from coding back to planning, for a plan that must change
after a code gate rejected the code. The move out of planning is judged by
`satelle-tree-unchanged-check`: a plan is judged before any code, so that
check refuses a branch whose tree differs from where it left its base branch.
A planning seat allows attachments only, so once the story is back in
planning no seat can move the code off the branch.

This check judges the move back to planning, while the driver's seat still
allows tree edits. It refuses the move while the tree differs from the fork
and names the one way out: a forward commit that returns the tree to the fork.
Nothing is rewritten or force-pushed, so a branch already on origin stays a
fast-forward. The earlier code stays in history at the commit the check
names, from which the coder restores it after the re-plan. Once that commit
exists, the tree equals the fork and the move is accepted. The check runs
only git reads and `satelle ledger list <id> --session <session>`; the driver runs the commit.

**The base.** As in `satelle-tree-unchanged-check`: `origin/main`, or, for
a story stacked on another story, `origin/<parent id>` for the newest
`--base` a seat of the story named that is another item's id with a branch on
origin. That check's own rules decide its base on the move out of planning.

```sh
set -u
excluded=.satelle/runs/
for tool in git jq satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the tree cannot be compared"; exit 1; }
done
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "not a git work tree: there is no tree to compare"
  exit 1
fi
item=$SATELLE_ITEM
events=$(satelle ledger list "$item" --json </dev/null) || { echo "$item: its ledger cannot be read"; exit 1; }
base_ref=origin/main
tip=
for ref in $(printf '%s' "$events" | jq -r '[(. // [])[] | select(.kind == "seat.bound") | .payload.base_ref // empty | select(. != "")] | reverse | .[]'); do
  name=${ref#refs/remotes/origin/}
  name=${name#origin/}
  case "$name" in
    "$item" | "" | *[!A-Za-z0-9_.-]*) continue ;;
  esac
  satelle ledger list "$name" --json </dev/null 2>/dev/null | jq -e 'any((. // [])[]; .kind == "item.created")' >/dev/null 2>&1 || continue
  t=$(git rev-parse --verify --quiet "refs/remotes/origin/$name^{commit}" </dev/null) || continue
  base_ref="origin/$name"
  tip=$t
  break
done
if [ -z "$tip" ]; then
  tip=$(git rev-parse --verify --quiet "refs/remotes/origin/main^{commit}") || { echo "$item: origin/main names no commit here; fetch it"; exit 1; }
fi
head=$(git rev-parse --verify --quiet 'HEAD^{commit}') || { echo "$item: HEAD names no commit"; exit 1; }
fork=$(git merge-base HEAD "$tip" 2>/dev/null) || { echo "$item: HEAD and $base_ref share no commit"; exit 1; }
paths=$( { git -c core.quotePath=false diff --name-only --no-renames "$fork" --; git -c core.quotePath=false ls-files --others --exclude-standard; } |
  awk -v p="$excluded" 'length($0) > 0 && index($0, p) != 1' | LC_ALL=C sort -u)
if [ -z "$paths" ]; then
  echo "the tree is unchanged since it left $base_ref at $(git rev-parse --short=12 "$fork"): $item may return to planning"
  exit 0
fi
n=$(printf '%s\n' "$paths" | wc -l | tr -d ' ')
echo "$item: the tree differs from where its branch left $base_ref ($fork): $n path(s) outside .satelle/runs. A story returns to planning only with its code set aside, because a plan is judged before any code. Set it aside now, under the seat this move leaves, with one forward commit that returns the tree to the fork (nothing rewritten or force-pushed; the code stays in history at $head):"
echo "  commit or discard any uncommitted change first, then: git restore --source=$fork --staged --worktree -- . && git commit -qm 'set aside the code of $(git rev-parse --short=12 "$head") for a re-plan ($item)'"
echo "Present the move back to planning again. After the re-plan the coder restores the code with git restore --source=$head --staged --worktree -- . and builds on it."
printf '%s\n' "$paths"
exit 1
```
