---
name: satelle-substrate-pushed-check
type: check
description: Refuses a substrate item's move to done when its worktree has uncommitted changes to tracked files, naming each, or when its branch's head on the git remote is not the local HEAD the other done gates judge. Done then always means the judged change is on the item's branch. Deterministic, no agent; reads git.
---

# Substrate pushed

The substrate done gates judge the worktree: `satelle <kind> diff` counts every
path changed since the item's base, committed or not. A change that is judged
but never committed or pushed would reach done with nothing on its branch. This
check refuses the move (where it sits on the transition:
`satelle workflow show substrate --session <session>`):

- **when a tracked file has uncommitted changes.** It names each path. Commit
  them, push, and present again. Untracked files are not judged as the change,
  so they do not refuse the move.
- **when the branch's head on its remote is not HEAD.** That includes a branch
  never pushed, a push that failed, or a push of another commit. The branch is
  the one HEAD is on: a detached HEAD has no branch to land, and is refused.
  The remote is the branch's configured remote. Without one, it is
  `checkout.defaultRemote`, else the only remote.

```sh
set -u
command -v git >/dev/null 2>&1 || { echo "git is not installed, so the worktree cannot be checked"; exit 1; }
item=$SATELLE_ITEM
dirty=$(git status --porcelain --untracked-files=no) || { echo "$item: git status failed, so the worktree cannot be checked"; exit 1; }
if [ -n "$dirty" ]; then
  n=$(printf '%s\n' "$dirty" | wc -l | tr -d ' ')
  echo "$item: $n tracked path(s) have uncommitted changes; the done gates judge the worktree, so done would land none of them:"
  printf '%s\n' "$dirty" | cut -c4-
  echo "Commit them, push the branch, and present done again."
  exit 1
fi
branch=$(git symbolic-ref -q --short HEAD) || { echo "$item: HEAD is detached, so there is no branch to land the change on: check out the item's branch, then push it and present done again"; exit 1; }
remote=$(git config --get "branch.$branch.remote" || true)
if [ -z "$remote" ] || [ "$remote" = "." ]; then
  remote=$(git config --get checkout.defaultRemote || true)
fi
if [ -z "$remote" ]; then
  remotes=$(git remote)
  case $(printf '%s' "$remotes" | grep -c .) in
    0) echo "$item: this repo has no git remote, so the branch $branch cannot be pushed: git remote add <remote> <url>"; exit 1 ;;
    1) remote=$remotes ;;
    *) echo "$item: the branch $branch has no configured remote and this repo has several ($(printf '%s' "$remotes" | tr '\n' ' ')) with no checkout.defaultRemote: git config checkout.defaultRemote <remote>"; exit 1 ;;
  esac
fi
head=$(git rev-parse HEAD) || { echo "$item: git rev-parse HEAD failed"; exit 1; }
refs=$(GIT_TERMINAL_PROMPT=0 git ls-remote -- "$remote" "refs/heads/$branch" 2>&1) || { echo "$item: could not read the branch $branch on the remote $remote: $refs"; exit 1; }
pushed=$(printf '%s\n' "$refs" | awk -v r="refs/heads/$branch" '$2 == r { print $1; exit }')
if [ -z "$pushed" ]; then
  echo "$item: the branch $branch is not on the remote $remote: git push $remote HEAD:refs/heads/$branch, then present done again"
  exit 1
fi
if [ "$pushed" != "$head" ]; then
  echo "$item: the branch $branch on $remote is at ${pushed:0:12}, not HEAD ${head:0:12}, the head the done gates judge: push HEAD to $remote $branch (git push $remote HEAD:refs/heads/$branch), then present done again"
  exit 1
fi
echo "$item: the worktree is clean and $remote $branch is at HEAD ${head:0:12}"
```
