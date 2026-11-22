---
name: satelle-changelog-entry-check
type: check
description: CI/CD gate on approved → done. The published version's notes are read from docs/releases/v<satelle.version>.md on origin/main; their first version heading is "## [<satelle.version>] - <YYYY-MM-DD>". On a binary-unchanged release (the run's release-binary record, release.md ### Decide) the item's changelog attachment is valid, since the next binary-changed release writes its lines (release.md step 3, carried). Otherwise the version's notes hold every line of the item's changelog attachment under its section, ending with the item id; an item an earlier release already published (a landed item a later run carries to done, release.md step 2a) is accepted from the earlier release's notes (a docs/releases file) that hold its lines. Deterministic, no agent.
---

# Changelog entry

Items never edit a file under `docs/releases/`: each records
its lines as an attachment named `changelog` (format in
`satelle-pr-ready-check/SKILL.md`, owned by `scripts/changelog-items.sh`). The
release run writes the version's notes, `## [<version>] - <date>`, to
`docs/releases/v<version>.md` from the attachments of the items it lands, in
its one bump commit; every earlier version has its own file there too
(`docs/releases/v0.md` holds the 0.0.x and serve-v0.0.x notes). This check
runs once per landed item, so every landed item is checked.

The notes of `<satelle.version>` (origin/main's `.version`) are
`docs/releases/v<satelle.version>.md` on origin/main; a missing file is
refused. Their first `## [` line must be the version's heading;
anything above the version section, whatever its name, is refused.

A binary-unchanged release writes no notes: the run's `release-binary` record
(read through `scripts/release-binary.sh of`) says so, and the item's lines
stay on its `changelog` attachment until the next binary-changed run writes
them first in its notes (release.md step 3,
`### Carried from binary-unchanged runs`). On that record the check accepts
when the published version's notes still open with its heading and the item
has a `changelog` attachment `scripts/changelog-items.sh lines` accepts; a
missing or malformed attachment is refused, so no line is lost. Otherwise
(`binary: changed`, or a run with no record):

- the item has a `changelog` attachment, and every entry of it appears in the
  version's notes under its `### <Section>`, ending with the item id;
- or, when they lack them, in an earlier release's notes: each
  `docs/releases/v*.md` on origin/main, newest first. An item whose head an earlier release already published,
  and which a later run carries to done, has its lines in that release's notes
  and none in this one (release.md step 3 writes nothing for it). Every line
  ends with the item id and only step 3 writes them, so such lines exist only
  when a release published the item.

```sh
set -u
task=tas_42301d36
git fetch -q origin || { echo "git fetch origin failed"; exit 1; }
if ! rec=$(bash scripts/release-binary.sh of "$task" "$SATELLE_ITEM" 2>&1); then
  echo "cannot read the release run's binary decision for $SATELLE_ITEM: $rec"
  exit 1
fi
ver=$(git show origin/main:.version 2>/dev/null | awk '$1=="satelle.version:" {print $2}')
[ -n "$ver" ] || { echo "origin/main .version lacks satelle.version"; exit 1; }
tmp=$(mktemp -d) || { echo "mktemp failed"; exit 1; }
trap 'rm -rf "$tmp"' EXIT
esc=$(printf '%s' "$ver" | sed 's/[.]/[.]/g')
notes=docs/releases/v$ver.md
git cat-file -e "origin/main:$notes" 2>/dev/null || { echo "origin/main has no $notes; the release run writes it"; exit 1; }
mkdir -p "$tmp/docs/releases"
git show "origin/main:$notes" >"$tmp/$notes" || { echo "cannot read $notes on origin/main"; exit 1; }
first=$(grep -m1 '^## \[' "$tmp/$notes")
if ! printf '%s\n' "$first" | grep -qE "^## \[$esc\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$"; then
  echo "$notes on origin/main opens with '$first', not '## [$ver] - <date>'"
  exit 1
fi
if ! satelle story attachment "$SATELLE_ITEM" changelog >"$tmp/item" 2>/dev/null; then
  echo "no changelog attachment on $SATELLE_ITEM: the release run writes the [$ver] notes from it"
  exit 1
fi
if printf '%s\n' "$rec" | grep -qx 'binary: unchanged'; then
  if ! why=$(bash scripts/changelog-items.sh lines "$SATELLE_ITEM" <"$tmp/item" 2>&1 >/dev/null); then
    echo "binary unchanged, but $SATELLE_ITEM's changelog attachment is refused, so the next release's notes could not carry it: $why"
    exit 1
  fi
  echo "binary unchanged since v$ver: $SATELLE_ITEM's lines are held on its changelog attachment; the next release's notes carry them (release.md step 3)"
  exit 0
fi
if why=$(bash scripts/changelog-items.sh has "$tmp/$notes" "$ver" "$SATELLE_ITEM:$tmp/item" 2>&1); then
  echo "$notes on origin/main opens with the [$ver] section, which holds every line of $SATELLE_ITEM's changelog attachment"
  exit 0
fi
for v in $(git ls-tree --name-only origin/main docs/releases/ 2>/dev/null | sed -n 's|^docs/releases/v\(.*\)[.]md$|\1|p' | sort -rV); do
  [ "$v" = "$ver" ] && continue
  f=docs/releases/v$v.md
  git show "origin/main:$f" >"$tmp/$f" 2>/dev/null || continue
  if bash scripts/changelog-items.sh has "$tmp/$f" "$v" "$SATELLE_ITEM:$tmp/item" >/dev/null 2>&1; then
    echo "$f on origin/main holds every line of $SATELLE_ITEM's changelog attachment in the [$v] section, the release that published it"
    exit 0
  fi
done
echo "$notes on origin/main lacks $SATELLE_ITEM's lines: $why"
exit 1
```
