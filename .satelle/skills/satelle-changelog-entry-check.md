---
name: satelle-changelog-entry-check
type: check
description: CI/CD gate on approved → done. origin/main's CHANGELOG.md carries the heading "## [<satelle.version>] - <YYYY-MM-DD>" and no "## [Unreleased]" heading. Deterministic, no agent.
---

# Changelog entry

Items add their lines under `## [Unreleased]`; the release run renames that
heading to `## [<version>] - <date>` in its one bump commit. A release whose
changelog still says Unreleased, or names no heading for its version, is
refused.

```sh
set -u
git fetch -q origin || { echo "git fetch origin failed"; exit 1; }
ver=$(git show origin/main:.version 2>/dev/null | awk '$1=="satelle.version:" {print $2}')
[ -n "$ver" ] || { echo "origin/main .version lacks satelle.version"; exit 1; }
log=$(git show origin/main:CHANGELOG.md 2>/dev/null) || { echo "origin/main has no CHANGELOG.md"; exit 1; }
esc=$(printf '%s' "$ver" | sed 's/[.]/[.]/g')
printf '%s\n' "$log" | grep -qE "^## \[$esc\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$" || { echo "CHANGELOG.md on origin/main has no '## [$ver] - <date>' heading"; exit 1; }
if printf '%s\n' "$log" | grep -qE '^## \[Unreleased\]'; then
  echo "CHANGELOG.md on origin/main still has an [Unreleased] heading; the release run renames it to [$ver]"
  exit 1
fi
echo "CHANGELOG.md on origin/main has the [$ver] heading and no [Unreleased]"
```
