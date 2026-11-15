---
name: satelle-substrate-paths-check
type: check
description: Refuses a substrate item's move when any path it changed lies outside .satelle/, naming each such path, and refuses a change with no changed path. A substrate change is this repo's process only; anything else goes on chore or delivery. Deterministic, no agent; reads the ledger and `diff --json`.
---

# Substrate paths

A substrate item changes this repo's process under `.satelle/` and nothing
else. Its changed paths are the `paths` of `satelle <kind> diff <id> --json
--session <session>`: every path changed in the worktree since the item's
base, committed or not.

The check refuses the move when any of those paths lies outside `.satelle/`,
naming each, and when there is no changed path at all (nothing to land). A
change that needs a file elsewhere (Go, a test, a script, docs) is not a
substrate change: move that work to a chore or delivery item, or cancel this
one and raise it there.

The allowed prefix is this script's first variable: it is this repo's
configuration, edited here.

```sh
set -u
allowed='^\.satelle/'
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
if [ -z "$paths" ]; then
  echo "$item: no changed path since the base: a substrate item lands a change under .satelle/"
  exit 1
fi
outside=$(printf '%s\n' "$paths" | grep -Ev "$allowed" || true)
if [ -n "$outside" ]; then
  n=$(printf '%s\n' "$outside" | wc -l | tr -d ' ')
  echo "$item: $n changed path(s) outside .satelle/; a substrate change touches .satelle/ only:"
  printf '%s\n' "$outside"
  echo "Move that work to a chore or delivery item, or revert it from this branch, and present again."
  exit 1
fi
n=$(printf '%s\n' "$paths" | wc -l | tr -d ' ')
echo "$item: all $n changed path(s) are under .satelle/"
```
