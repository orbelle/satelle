---
name: satelle-substrate-paths-check
type: check
description: Refuses a substrate item's move when any path it changed lies outside .satelle/ and the tests under tests/ that pin it, naming each such path, and refuses a change with no changed path under .satelle/. A substrate change is this repo's process and the tests its change needs; binary source goes on chore or delivery. Deterministic, no agent; reads the ledger and `diff --json`.
---

# Substrate paths

A substrate item changes this repo's process under `.satelle/`, and the tests
under `tests/` that its change makes assert the wrong behaviour: a story
carries its own test changes ([[satelle-split-authored-process]]). Its changed
paths are the `paths` of `satelle <kind> diff <id> --json
--session <session>`: every path changed in the worktree since the item's
base, committed or not.

The check refuses the move when any of those paths lies outside `.satelle/`
and `tests/`, or is Go source under `tests/` that is not a `_test.go` file,
naming each, and when no changed path lies under `.satelle/` (a change to
tests alone is a chore). A change that needs a file elsewhere (binary source,
a script, docs) is not a substrate change: move that work to a chore or
delivery item, or cancel this one and raise it there. The suite gate on the
same move runs the changed tests, and the reference review judges why each
one changed.

The allowed prefixes are this script's first variables: they are this repo's
configuration, edited here.

```sh
set -u
allowed='^\.satelle/'
tests='^tests/'
binary='^tests/.*\.go$'
testgo='_test\.go$'
for tool in jq satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the changed paths cannot be read"; exit 1; }
done
item=$SATELLE_ITEM
kind=$(satelle ledger list "$item" --json </dev/null | jq -r '[(. // [])[] | select(.kind == "item.created")][0].payload.kind // empty') || kind=
if [ -z "$kind" ]; then
  echo "$item: no item.created event on its ledger"
  exit 1
fi
report=$(satelle "$kind" diff "$item" --json </dev/null) || { echo "$item: satelle $kind diff failed, so the changed paths cannot be read"; exit 1; }
paths=$(printf '%s' "$report" | jq -r '(.paths // [])[]') || { echo "$item: the diff report does not parse"; exit 1; }
if [ -z "$paths" ] || ! printf '%s\n' "$paths" | grep -Eq "$allowed"; then
  echo "$item: no changed path under .satelle/ since the base: a substrate item lands a change under .satelle/ (a change to tests alone is a chore)"
  exit 1
fi
outside=$(printf '%s\n' "$paths" | A="$allowed" T="$tests" B="$binary" G="$testgo" awk '$0 ~ ENVIRON["A"] { next } $0 ~ ENVIRON["T"] && !($0 ~ ENVIRON["B"] && $0 !~ ENVIRON["G"]) { next } { print }')
if [ -n "$outside" ]; then
  n=$(printf '%s\n' "$outside" | wc -l | tr -d ' ')
  echo "$item: $n changed path(s) outside .satelle/ and the tests under tests/; a substrate change touches its process and the tests it needs only:"
  printf '%s\n' "$outside"
  echo "Move that work to a chore or delivery item, or revert it from this branch, and present again."
  exit 1
fi
n=$(printf '%s\n' "$paths" | wc -l | tr -d ' ')
nt=$(printf '%s\n' "$paths" | grep -Ec "$tests" || true)
echo "$item: all $n changed path(s) are under .satelle/ or are tests under tests/ ($nt test path(s))"
```
