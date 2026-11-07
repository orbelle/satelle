---
name: satelle-changelog-entry-check
type: check
description: CI/CD gate on approved → done. The first version heading of origin/main's CHANGELOG.md is "## [<satelle.version>] - <YYYY-MM-DD>", and that section holds every line of the item's changelog attachment under its section, ending with the item id. Deterministic, no agent.
---

# Changelog entry

Items never edit CHANGELOG.md: each records its lines as an attachment named
`changelog` (format in `satelle-pr-ready-check.md`, owned by
`scripts/changelog-items.sh`). The release run writes the version section,
`## [<version>] - <date>`, from the attachments of the items it lands, on top
of the file in its one bump commit. This check runs once per landed item, so
every landed item is checked:

- the first `## [` line of origin/main's CHANGELOG.md is the version's heading;
  anything left above the version section, whatever its name, is refused;
- the item has a `changelog` attachment, and every entry of it appears in the
  version section under its `### <Section>`, ending with the item id.

```sh
set -u
git fetch -q origin || { echo "git fetch origin failed"; exit 1; }
ver=$(git show origin/main:.version 2>/dev/null | awk '$1=="satelle.version:" {print $2}')
[ -n "$ver" ] || { echo "origin/main .version lacks satelle.version"; exit 1; }
tmp=$(mktemp -d) || { echo "mktemp failed"; exit 1; }
trap 'rm -rf "$tmp"' EXIT
git show origin/main:CHANGELOG.md >"$tmp/CHANGELOG.md" 2>/dev/null || { echo "origin/main has no CHANGELOG.md"; exit 1; }
esc=$(printf '%s' "$ver" | sed 's/[.]/[.]/g')
first=$(grep -m1 '^## \[' "$tmp/CHANGELOG.md")
if ! printf '%s\n' "$first" | grep -qE "^## \[$esc\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$"; then
  echo "CHANGELOG.md on origin/main opens with '$first', not '## [$ver] - <date>'; the release run writes the version section on top"
  exit 1
fi
if ! satelle story attachment "$SATELLE_ITEM" changelog >"$tmp/item" 2>/dev/null; then
  echo "no changelog attachment on $SATELLE_ITEM: the release run writes the [$ver] section from it"
  exit 1
fi
if ! why=$(bash scripts/changelog-items.sh has "$tmp/CHANGELOG.md" "$ver" "$SATELLE_ITEM:$tmp/item" 2>&1); then
  echo "CHANGELOG.md on origin/main lacks $SATELLE_ITEM's lines: $why"
  exit 1
fi
echo "CHANGELOG.md on origin/main opens with the [$ver] section, which holds every line of $SATELLE_ITEM's changelog attachment"
```
