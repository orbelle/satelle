---
name: satelle-epic-branch-check
scope: system
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check for the epic-branch lane's close — a container member (an epic's `epic:<theme>` set or a parent's children) with no `lane:trunk` tag closes only when its slice is on a pushed `epic/<its-id>` branch not yet on trunk. Accepts a `lane:trunk` story and a story in no container. Deterministic and self-contained.
---

# Epic-branch check (close gate)

**Functional-check** gate on a working-lane story's close. It reads the **lane**
the story is on and enforces the half of it a story can break:

- **trunk lane** — the story carries `lane:trunk`: it lands on trunk through its
  own release. Accepted without looking at a branch.
- **branch lane** — the story is a member of a container and carries no
  `lane:trunk`: it commits on its own branch `epic/<story-id>` and **never touches
  trunk**. The container's `merged` step merges that branch once for every child.
  The close is accepted only when the branch is **pushed** and its tip is **not
  yet an ancestor of trunk**. A missing branch means the slice is not safe off
  this machine; a branch already on trunk means the story released itself and
  skipped the lane. Either is rejected, with the fix named.
- **no container** — the story is in no epic and has no `parent` parent: there is
  no lane to enforce. Accepted.

The lane is read here, in the check, because a route is selected by category and
a gate's `applies_to` matches tags only positively: no route table can say "a
story **without** `lane:trunk`".

Membership follows the binary's one rule (`epicset`): a story with an
`epic:<theme>` tag belongs to that theme's `epic-parent`; otherwise a story whose
`parent_id` is a `parent` belongs to it. The container's own member listing
(`satelle story route <container>`, `## Members`) confirms it.

The trunk and the remote are the ones `satelle trunk sync --json` reports, never
a hardcoded `main` or `origin`. A repo whose lane differs — a pull-request flow,
a different branch name — overrides this skill by name under `.satelle/skills/`.

The check is the embedded ```check script below — **self-contained**, no
external file (see [[satelle-reviewer-self-contained]]). Exit 0 accepts;
non-zero rejects with notes. **Mechanism, not judgment.** See
[[satelle-agent-model]].

```check
#!/usr/bin/env bash
set -uo pipefail

payload=$(cat)
# Anchored on the id KEY so a story id quoted inside the body cannot win.
sid=$(printf '%s' "$payload" | grep -oE '"id":[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload"
  exit 1
fi

# The payload carries the story itself (its tags and parent_id), so a story that
# is plainly on the trunk lane, or plainly in no container, is judged without a
# CLI read. Anything else falls through to the reads below. A match preceded by
# a backslash is JSON-escaped text inside a field, never the story's own key.
ptags=$(printf '%s' "$payload" | grep -oE '(^|[^\\])"tags":[[:space:]]*(\[[^]]*\]|null)' | head -1)
ppid=$(printf '%s' "$payload" | grep -oE '(^|[^\\])"parent_id":[[:space:]]*"sty_[a-f0-9]+"' | head -1)
if [ -n "$ptags" ]; then
  if printf '%s' "$ptags" | grep -q '"lane:trunk"'; then
    echo "$sid is on the trunk lane (lane:trunk); no epic branch is required"
    exit 0
  fi
  if ! printf '%s' "$ptags" | grep -q '"epic:' && [ -z "$ppid" ]; then
    echo "$sid is in no container (no epic: parent, no parent story); no epic branch is required"
    exit 0
  fi
fi

# json_tags: stdin is `satelle story get --json`; stdout is one tag per line.
json_tags() {
  awk '/^  "tags": \[\]/{next} /^  "tags": \[/{f=1;next} f&&/^  \]/{f=0} f{gsub(/^[ \t]*"|",?$/,""); print}'
}
# json_field NAME: stdin is `satelle story get --json`; stdout is a top-level string field.
json_field() {
  sed -n "s/^  \"$1\": \"\\([^\"]*\\)\".*/\\1/p" | head -1
}

story=$(satelle story get "$sid" --json 2>&1) || {
  echo "cannot read $sid: $story"
  exit 1
}
tags=$(printf '%s\n' "$story" | json_tags)

# The trunk lane: the story releases itself.
if printf '%s\n' "$tags" | grep -qx 'lane:trunk'; then
  echo "$sid is on the trunk lane (lane:trunk); no epic branch is required"
  exit 0
fi

# Find the container, by epicset's rule: the epic:<theme> tag's epic-parent,
# else parent_id to a `parent`.
container=""
for theme in $(printf '%s\n' "$tags" | grep '^epic:' || true); do
  listed=$(satelle story list --tag "$theme" --limit 1000 --json 2>&1) || {
    echo "cannot list $theme: $listed"
    exit 1
  }
  # One key per line; the category line follows its id line within an object.
  container=$(printf '%s\n' "$listed" | awk '
    /^    "id": "/ { id=$2; gsub(/[",]/,"",id) }
    /^    "category": "epic-parent"/ { print id; exit }')
  [ -n "$container" ] && break
done
if [ -z "$container" ]; then
  pid=$(printf '%s\n' "$story" | json_field parent_id)
  if [ -n "$pid" ]; then
    pstory=$(satelle story get "$pid" --json 2>&1) || {
      echo "cannot read parent $pid: $pstory"
      exit 1
    }
    if [ "$(printf '%s\n' "$pstory" | json_field category)" = "parent" ]; then
      container="$pid"
    fi
  fi
fi
if [ -z "$container" ]; then
  echo "$sid is in no container (no epic: parent, no parent story); no epic branch is required"
  exit 0
fi

# Confirm the story is a listed member of that container.
route=$(satelle story route "$container" 2>&1) || {
  echo "cannot read the members of $container: $route"
  exit 1
}
if ! printf '%s\n' "$route" | grep -qE "^- $sid "; then
  echo "$sid is not a member of $container; no epic branch is required"
  exit 0
fi

# The trunk and the remote are the binary's answer, not a guess.
sync=$(satelle trunk sync --json 2>&1)
trunk=$(printf '%s\n' "$sync" | sed -n 's/^[[:space:]]*"trunk":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
remote=$(printf '%s\n' "$sync" | sed -n 's/^[[:space:]]*"remote":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$trunk" ] || [ -z "$remote" ]; then
  echo "cannot resolve trunk and remote: $sync"
  exit 1
fi

branch="epic/$sid"
tip=$(git ls-remote "$remote" "refs/heads/$branch" 2>/dev/null | awk '{print $1}' | head -1)
if [ -z "$tip" ]; then
  echo "$sid is a member of $container on the branch lane, but $remote has no $branch."
  echo "Commit the slice to $branch from the story's worktree and push that branch ('git push $remote $branch'); leave $trunk alone — $container merges the branch once."
  exit 1
fi

git fetch --quiet "$remote" "$branch" "$trunk" >/dev/null 2>&1 || true
if ! git cat-file -e "$tip^{commit}" 2>/dev/null; then
  echo "cannot read $branch ($tip) from $remote"
  exit 1
fi
git merge-base --is-ancestor "$tip" "refs/remotes/$remote/$trunk" 2>/dev/null
case $? in
  0)
    echo "$sid is a member of $container on the branch lane, but $branch ($tip) is already on $trunk."
    echo "A branch-lane story never lands on $trunk itself: $container merges $branch once. If this story must release on its own, tag it lane:trunk through the container's readiness round."
    exit 1
    ;;
  1)
    echo "$sid is on the branch lane: $branch ($tip) is pushed to $remote and not yet on $trunk"
    exit 0
    ;;
  *)
    echo "cannot compare $branch with $remote/$trunk"
    exit 1
    ;;
esac
```
