---
name: satelle-changelog-entry-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate for release close (release → done) — rejects if CHANGELOG.md has no level-2 entry for the satelle.version on HEAD. Deterministic, no LLM; self-contained.
---

# Changelog entry check (release close gate)

**Functional-check** on `release → done`. The project workflow declares it as a
scoped reviewer node (`on="done"`). It verifies the release commit on HEAD
bumped `.version` **and** `CHANGELOG.md` carries a matching `## [X.Y.Z]` entry
for that version. A release without a changelog entry fails closed.

An epic child whose parent owns the merge (not `lane:trunk`) is skipped with a `skipped:` line.

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

# Independent versions (sty_19ff03f4): CLI key satelle.version → ## [X.Y.Z];
# serve key satelle-serve.version → ## [serve-vY]. Both required when present.
CLI=$(awk '$1=="satelle.version:" {print $2}' .version 2>/dev/null)
SERVE=$(awk '$1=="satelled.version:" {print $2}' .version 2>/dev/null)
if [ -z "$CLI" ]; then
  echo "could not read satelle.version from .version on HEAD"
  exit 1
fi
if [ ! -f CHANGELOG.md ]; then
  echo "CHANGELOG.md missing — add an entry for $CLI before closing release"
  exit 1
fi
if ! grep -qE "^## \[${CLI}\]" CHANGELOG.md; then
  echo "CHANGELOG.md has no entry for CLI version $CLI — add '## [$CLI] - <date>' before close"
  exit 1
fi
if [ -n "$SERVE" ] && ! grep -qE "^## \[serve-v${SERVE}\]" CHANGELOG.md; then
  echo "CHANGELOG.md has no entry for serve version $SERVE — add '## [serve-v$SERVE] - <date>' before close"
  exit 1
fi
echo "changelog entry present for CLI $CLI${SERVE:+ and serve-v$SERVE}"
```
