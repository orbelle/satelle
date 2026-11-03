---
name: satelle-ci-published-check
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate on entry to done: the CI runs for HEAD's actual SHA concluded successfully and the tag for the version in .version exists. Verifies the world, not the executor's summary. Never pushes or publishes.
---

# CI + published-tag check (objective 3)

Asks GitHub, keyed on HEAD's SHA, that the `test` and `release` runs concluded success and tag `v<satelle.version>` exists — not the executor's summary. Verifies, never performs; without `gh` it rejects. An epic child whose parent owns the merge (not `lane:trunk`) is skipped.

```check
#!/usr/bin/env bash
set -uo pipefail
# Process of record (sty_9c989241): a story driven in a linked worktree has no
# data dir of its own; read the main tree's, beside the git common dir.
sd=.satelle
if [ ! -d "$sd" ]; then
  common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && [ -d "$(dirname "$common")/.satelle" ] && sd="$(dirname "$common")/.satelle"
fi
# Epic child whose parent owns the merge (sty_2e9d0440). The parent release
# owns the version bump, the main CI run, and dogfood. This check skips those
# assertions and says why. A story that is not such a child falls through.
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -n "$sid" ]; then
  parent_id=$(satelle story get "$sid" --json 2>/dev/null | grep -oE '"parent_id"[[:space:]]*:[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
  # A lane:trunk child ships through its own full release (sty_cf79e443), so it is
  # judged like a story outside an epic.
  if [ -n "$parent_id" ] && ! satelle story get "$sid" --json 2>/dev/null | grep -q '"lane:trunk"'; then
    parent_cat=$(satelle story get "$parent_id" --json 2>/dev/null | grep -oE '"category"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 | grep -oE '"[^"]+"$' | tr -d '"')
    if [ "$parent_cat" = "epic-parent" ] || [ "$parent_cat" = "parent" ]; then
      if grep -qE '^after_children[[:space:]]*=' "$sd"/workflows/step.toml 2>/dev/null; then
        echo "skipped: epic child $sid of $parent_cat $parent_id; a step sets after_children, so this check does not require a version bump, a main workflow run, or a dogfood install"
        exit 0
      fi
    fi
  fi
fi


if ! command -v gh >/dev/null 2>&1; then
  echo "OBJECTIVE 3 FAILED: gh is not installed, so the GitHub outcome cannot be verified"
  echo "  a gate that cannot check must reject — 'unknown' is not 'passed'"
  exit 1
fi

sha=$(git rev-parse HEAD 2>/dev/null)
if [ -z "$sha" ]; then
  echo "OBJECTIVE 3 FAILED: could not resolve HEAD"
  exit 1
fi

# The slice must actually be pushed: a local-only commit has no CI to check.
if ! git branch -r --contains "$sha" 2>/dev/null | grep -q .; then
  echo "OBJECTIVE 3 FAILED (not pushed): HEAD $sha is on no remote branch"
  echo "  push the slice, let CI finish, then retry this edge"
  exit 1
fi

fail=0
for wf in test release; do
  line=$(gh run list --commit "$sha" --workflow "$wf" --limit 1 \
           --json status,conclusion,databaseId \
           -q '.[0] | "\(.databaseId) \(.status) \(.conclusion // "-")"' 2>/dev/null)
  if [ -z "$line" ]; then
    echo "OBJECTIVE 3 FAILED ($wf): no run found for HEAD $sha"
    echo "  the summary may name a run from a DIFFERENT commit — this gate keys on HEAD"
    fail=1
    continue
  fi
  set -- $line
  echo "  $wf: run $1 status=$2 conclusion=$3"
  if [ "$2" != "completed" ]; then
    echo "OBJECTIVE 3 FAILED ($wf): run $1 is still $2 — wait for it, then retry the edge"
    fail=1
  elif [ "$3" != "success" ]; then
    echo "OBJECTIVE 3 FAILED ($wf): run $1 concluded $3"
    fail=1
  fi
done

# The package: .version is the single source of truth for the tag the release
# workflow cuts, so verify THAT tag is published rather than "some" latest tag.
ver=$(awk '$1=="satelle.version:"{print $2}' .version 2>/dev/null)
if [ -z "$ver" ]; then
  echo "OBJECTIVE 3 FAILED: could not read satelle.version from .version"
  exit 1
fi
if ! gh release view "v$ver" --json tagName -q .tagName >/dev/null 2>&1; then
  echo "OBJECTIVE 3 FAILED (not published): release tag v$ver does not exist"
  echo "  the release workflow cuts a tag only when .version changed — confirm the bump landed"
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "objective 3 PASS: test + release workflows green for HEAD $sha; v$ver published"
```
