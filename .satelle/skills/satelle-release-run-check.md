---
name: satelle-release-run-check
type: check
description: CI/CD's first gate on approved → done. Accepts only while an in-progress run of the release task (tas_42301d36) lists the item among its inputs and the item's pull-request head is on origin/main. A hand-presented done move has no such run and is refused. Deterministic, no agent.
---

# Release run (only a release run closes an item)

`done` is terminal and is reached only by a run of the release task, after the
item's head is on main, the version is bumped, the release is published and the
build is installed (operator, 2026-11-06), or, when the run's landing left the
shipped binaries unchanged, after main fast-forwarded to the tested candidate
(release.md, `### Decide: did the binary change`). The run that lists the item
is found by `scripts/release-binary.sh run`, which the version gates share. The release task is `tas_42301d36`
(its procedure is `.satelle/skills/release.md`).

```sh
set -u
task=tas_42301d36
for tool in gh jq git satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed"; exit 1; }
done
listed=$(bash scripts/release-binary.sh run "$task" "$SATELLE_ITEM")
if [ $? -eq 2 ]; then
  echo "cannot read the in-progress runs of $task"
  exit 1
fi
if [ -z "$listed" ]; then
  echo "no in-progress run of $task lists $SATELLE_ITEM: an item reaches done only through a release run (follow .satelle/skills/release.md step 0: satelle queue fire release --session <session>)"
  exit 1
fi
git fetch -q origin || { echo "git fetch origin failed"; exit 1; }
head=$(gh pr list --state all --base main --json headRefOid,title --limit 200 |
  jq -r --arg id "($SATELLE_ITEM)" '[.[] | select(.title | endswith($id))][0].headRefOid // empty')
if [ -z "$head" ]; then
  echo "no pull request against main has a title ending ($SATELLE_ITEM)"
  exit 1
fi
if ! git merge-base --is-ancestor "$head" origin/main; then
  echo "$SATELLE_ITEM's head ${head:0:12} is not on origin/main yet"
  exit 1
fi
echo "release run $listed lists $SATELLE_ITEM; its head ${head:0:12} is on origin/main"
```
