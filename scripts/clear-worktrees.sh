#!/usr/bin/env bash
# clear-worktrees — the ACTION of the cleanup task tas_b3424a1c (release.md
# step 10). Run from the main checkout after `git fetch origin`.
#
# For every linked worktree of this repo it removes the worktree, and deletes
# its local branch, only when all three hold:
#   1. git status --porcelain is empty in it (no uncommitted or untracked change);
#   2. its HEAD is contained in a remote branch (git branch -r --contains HEAD);
#   3. its branch names no item, or names one whose state is terminal in the
#      item's own workflow (read from `satelle workflow show`, never a state
#      named here). An item sent back to in_progress keeps its worktree
#      (sto_6f16d931).
# Every other worktree is kept and reported with its reason. Then
# git worktree prune, then the main checkout goes to main: its current branch
# must be merged into origin/main, then main is checked out and fast-forwarded
# to origin/main.
#
# Output, one line each, for the run's worktree-evidence:
#   worktrees before: <n>
#   removed <path> (<branch>)
#   kept <path> (<branch>): <reason>
#   worktrees after: <n>
#   main checkout: <branch> is merged into origin/main <sha>
#   <git status -sb of the main checkout>
# Exits 1 when it cannot judge (a tool is missing, not in the main checkout)
# or the main checkout cannot be put on main; nothing is removed on a refusal
# made before the walk.
set -uo pipefail

for tool in git satelle jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not on PATH, so the worktrees cannot be judged" >&2; exit 1; }
done

top=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "not in a git repository" >&2; exit 1; }
main=$(git worktree list --porcelain | sed -n '1s/^worktree //p')
if [ "$top" != "$main" ]; then
  echo "run from the main checkout ($main), not the linked worktree $top, which the cleanup may remove" >&2
  exit 1
fi
cd "$main" || exit 1

count() { git worktree list --porcelain | grep -c '^worktree '; }

# item_state <branch>: prints "<id> <state> <terminal>" when the branch names
# an item (terminal is true, false or unknown); prints nothing when it names none.
item_state() {
  local b=$1 id kind js st wf term
  [[ $b =~ ^[a-z]+_[0-9a-f]+$ ]] || return 0
  id=$b
  for kind in story task execution queue; do
    js=$(satelle "$kind" get "$id" --json 2>/dev/null) || continue
    st=$(printf '%s' "$js" | jq -r '.status // empty')
    wf=$(printf '%s' "$js" | jq -r '.workflow // empty')
    term=unknown
    if [ -n "$wf" ] && [ -n "$st" ]; then
      term=$(satelle workflow show "$wf" --json 2>/dev/null |
        jq -r --arg s "$st" 'if .states[$s] == null then "unknown" else (.states[$s].Terminal == true | tostring) end' 2>/dev/null)
      [ -n "$term" ] || term=unknown
    fi
    echo "$id ${st:-unknown} $term"
    return 0
  done
}

echo "worktrees before: $(count)"

# The linked worktrees: every entry after the first (the main checkout).
entries=$(git worktree list --porcelain | awk '
  /^worktree / { if (p != "") print p "\t" b "\t" pr; p = substr($0, 10); b = ""; pr = "" }
  /^branch /   { b = substr($0, 8); sub(/^refs\/heads\//, "", b) }
  /^prunable/  { pr = "prunable" }
  END          { if (p != "") print p "\t" b "\t" pr }' | tail -n +2)

while IFS=$'\t' read -r wt br pr <&3; do
  [ -n "$wt" ] || continue
  label=${br:-detached}
  if [ -n "$pr" ] || [ ! -d "$wt" ]; then
    echo "kept $wt ($label): missing on disk; git worktree prune drops it"
    continue
  fi
  dirty=$(git -C "$wt" status --porcelain 2>/dev/null | wc -l)
  if [ "$dirty" -gt 0 ]; then
    echo "kept $wt ($label): dirty: $dirty uncommitted or untracked files"
    continue
  fi
  head=$(git -C "$wt" rev-parse HEAD 2>/dev/null)
  if [ -z "$head" ] || [ -z "$(git branch -r --contains "$head" 2>/dev/null)" ]; then
    echo "kept $wt ($label): unpushed head ${head:0:12}"
    continue
  fi
  id= st= term=
  if [ -n "$br" ]; then
    read -r id st term < <(item_state "$br")
    if [ -n "${id:-}" ] && [ "$term" != true ]; then
      echo "kept $wt ($label): item $id is $st, not terminal"
      continue
    fi
  fi
  if ! git worktree remove "$wt"; then
    echo "kept $wt ($label): git worktree remove refused"
    continue
  fi
  if [ -n "$br" ]; then
    git branch -q -D "$br" || echo "removed $wt but its branch $br was not deleted"
  fi
  echo "removed $wt ($label)"
done 3< <(printf '%s\n' "$entries")

git worktree prune
echo "worktrees after: $(count)"

cur=$(git symbolic-ref -q --short HEAD || echo detached)
git rev-parse -q --verify origin/main >/dev/null || { echo "main checkout: no origin/main; fetch origin first" >&2; exit 1; }
if ! git merge-base --is-ancestor HEAD origin/main; then
  echo "main checkout: $cur ($(git rev-parse --short=12 HEAD)) is not merged into origin/main; left as it is" >&2
  exit 1
fi
echo "main checkout: $cur is merged into origin/main $(git rev-parse --short=12 origin/main)"
git checkout -q main 2>/dev/null || git checkout -q -b main origin/main || { echo "main checkout: cannot check out main" >&2; exit 1; }
git merge -q --ff-only origin/main || { echo "main checkout: main does not fast-forward to origin/main" >&2; exit 1; }
git status -sb | head -1
