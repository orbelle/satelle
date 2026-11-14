---
name: satelle-tree-unchanged-check
type: check
description: Refuses a move out of planning when the item's branch already differs, outside .satelle/runs, from where it left origin/main (or the branch on origin of another item it is stacked on, once that item has passed its own plan), naming every changed path in a tree-changes attachment. A plan is judged before any code. Deterministic, no agent; reads git and the ledger.
---

# Tree unchanged (a plan is judged before any code)

An item is worked on its own branch, named by its id, cut from `origin/main`
or stacked on another item's branch (`satelle-pr-ready-check`). Its plan is
judged before any code, so when the item leaves planning that branch holds
nothing of its own. This check refuses the move when it does: a plan written
after the code is refused.

**The diff.** The paths that differ between the worktree (committed,
uncommitted and untracked, as `satelle <kind> diff --session <session>` counts
a changed path) and the merge base of `HEAD` with the item's base branch on
origin. Paths under `.satelle/runs/` are not counted. What the base branch
held at the fork, and what the item's branch merged or fast-forwarded in from
it since, is not in the diff: the merge base moves with the merge.

**The base branch.** `origin/main`, unless the item is stacked: then it is
`origin/<parent id>` for the newest `--base` any seat of the item named (a
released or replaced seat's too) that holds all of:

- with a leading `origin/` or `refs/remotes/origin/` removed, it is the id of
  an item on the ledger;
- that item is not this item (an item is not its own base);
- that item's workflow can be read;
- that item has passed its own plan: of the states it entered, the most recent
  one with an agent performer lets its performer change the tree (its `edits`
  is not `attachments`). An item still in its start state, in planning,
  returned to planning, or cancelled before coding has not;
- that item's branch is on origin: `refs/remotes/origin/<its id>` names a
  commit.

Every other `--base`, `HEAD`, a commit, a tag, a local branch, a branch on
origin with any other name, is ignored, so no seat empties the diff: seating
at the coded commit, or pushing the code under another branch name and naming
it, still leaves the code in the diff. Only `refs/remotes/origin/main` and
`refs/remotes/origin/<item id>` are ever resolved. The check fetches nothing
and writes nothing to git; with no `origin/main` and no qualifying parent it
refuses.

**On a refusal** the full list of paths, one per line, is the item's
`tree-changes` attachment (recorded again only when the list changed), and the
notes name it, the base and every path (bounded by the gate's notes). Two
remedies: move the work off the item's branch and present again, or cancel the
item and raise it again. Seating is not a remedy, because no seat changes the
diff.

```sh
set -u
trunk=refs/remotes/origin/main
excluded=.satelle/runs/
for tool in git jq satelle sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the tree cannot be compared"; exit 1; }
done
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "not a git work tree: there is no tree to compare"
  exit 0
fi
item=$SATELLE_ITEM
events=$(satelle ledger list "$item" --json </dev/null) || { echo "$item: its ledger cannot be read"; exit 1; }
kind=$(printf '%s' "$events" | jq -r '[(. // [])[] | select(.kind == "item.created")][0].payload.kind // empty')
if [ -z "$kind" ]; then
  echo "$item: no item.created event on its ledger"
  exit 1
fi
candidates=$(printf '%s' "$events" | jq -r '
  [(. // [])[] | select(.kind == "seat.bound") | .payload.base_ref // empty | select(. != "")]
  | reverse | reduce .[] as $r ([]; if any(.[]; . == $r) then . else . + [$r] end) | .[]')

base_ref=
tip=
ignored_ref=
ignored_why=
nl='
'
saved_ifs=$IFS
IFS=$nl
set -f
for ref in $candidates; do
  IFS=$saved_ifs
  name=${ref#refs/remotes/origin/}
  name=${name#origin/}
  why=
  case "$name" in
    [A-Za-z0-9]*) case "$name" in *[!A-Za-z0-9_.-]*) why="not the branch of an item" ;; esac ;;
    *) why="not the branch of an item" ;;
  esac
  pkind=
  if [ -z "$why" ]; then
    pevents=$(satelle ledger list "$name" --json </dev/null 2>/dev/null) || pevents='[]'
    pkind=$(printf '%s' "$pevents" | jq -r '[(. // [])[] | select(.kind == "item.created")][0].payload.kind // empty' 2>/dev/null)
    [ -n "$pkind" ] || why="not the branch of an item"
  fi
  if [ -z "$why" ] && [ "$name" = "$item" ]; then
    why="the item's own branch"
  fi
  if [ -z "$why" ]; then
    pitem=$(satelle "$pkind" get "$name" --json </dev/null 2>/dev/null) || pitem=
    pflow=$(printf '%s' "$pitem" | jq -r '.workflow // empty' 2>/dev/null)
    pstatus=$(printf '%s' "$pitem" | jq -r '.status // empty' 2>/dev/null)
    pshow=
    if [ -n "$pflow" ]; then
      pshow=$(satelle workflow show "$pflow" --json </dev/null 2>/dev/null) || pshow=
    fi
    [ -n "$pshow" ] || why="$name's workflow cannot be read"
  fi
  if [ -z "$why" ]; then
    if ! printf '%s' "$pevents" | jq -e --argjson wf "$pshow" '
      [(. // [])[] | select(.kind == "item.transitioned") | .payload.to]
      | reverse
      | map(select(($wf.states[.] // {}).Performer != null and ((($wf.states[.].Performer.Operator) // "") == "")))
      | if length == 0 then false else (($wf.states[.[0]].Edits // "") != "attachments") end' >/dev/null 2>&1; then
      why="$name is in $pstatus and has not passed its own plan (the latest state with an agent performer it entered lets that performer change only attachments, or it entered none)"
    fi
  fi
  if [ -z "$why" ]; then
    ptip=$(git rev-parse --verify --quiet "refs/remotes/origin/$name^{commit}" </dev/null) || ptip=
    [ -n "$ptip" ] || why="$name has no branch on origin"
  fi
  if [ -z "$why" ]; then
    base_ref="origin/$name"
    tip=$ptip
    break
  fi
  if [ -z "$ignored_ref" ]; then
    ignored_ref=$ref
    ignored_why=$why
  fi
done
IFS=$saved_ifs
set +f
if [ -z "$base_ref" ]; then
  base_ref=origin/main
  tip=$(git rev-parse --verify --quiet "$trunk^{commit}") || tip=
  if [ -z "$tip" ]; then
    echo "$item: origin/main names no commit here; fetch it"
    exit 1
  fi
fi

fork=$(git merge-base HEAD "$tip" 2>/dev/null) || fork=
if [ -z "$fork" ]; then
  echo "$item: HEAD and $base_ref share no commit"
  exit 1
fi
short=$(printf '%s' "$fork" | cut -c1-12)
changed=$(git -c core.quotePath=false diff --name-only --no-renames "$fork" --) || { echo "$item: git diff --name-only $short failed"; exit 1; }
untracked=$(git -c core.quotePath=false ls-files --others --exclude-standard) || { echo "$item: git ls-files --others failed"; exit 1; }
paths=$(printf '%s\n%s\n' "$changed" "$untracked" | awk -v p="$excluded" 'length($0) > 0 && index($0, p) != 1' | LC_ALL=C sort -u)

if [ -z "$paths" ]; then
  echo "the tree is unchanged since it left $base_ref at $short"
  exit 0
fi

n=$(printf '%s\n' "$paths" | wc -l | tr -d ' ')
sum=$(printf '%s\n' "$paths" | sha256sum | cut -d' ' -f1)
latest=$(satelle "$kind" attachments "$item" --json </dev/null | jq -r '[(. // [])[] | select(.name == "tree-changes")] | last | .sha256 // empty' 2>/dev/null) || latest=
attached=
if [ "$latest" != "$sum" ]; then
  printf '%s\n' "$paths" | satelle "$kind" attach "$item" --name tree-changes --type text/plain --file - >/dev/null 2>&1 ||
    attached="(could not record the tree-changes attachment; the paths are listed below.) "
fi
echo "$item: the tree already differs from where its branch left $base_ref ($short): $n path(s) outside .satelle/runs. A plan is judged before any code. ${attached}Every path is in the item's tree-changes attachment: satelle $kind attachment $item tree-changes --session <session>. Move this work off the item's branch and present again, or cancel the item and raise it again: satelle $kind set $item --status cancelled --session <session>."
if [ -n "$ignored_ref" ]; then
  echo "The base a seat named, $ignored_ref, is not used: $ignored_why. The base is origin/main, or the branch on origin of another item that has passed its own plan."
fi
printf '%s\n' "$paths"
exit 1
```
