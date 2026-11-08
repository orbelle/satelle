---
name: satelle-release-kept-check
type: check
description: CI/CD gate on a run's in_progress → done. A run that landed items (its release-kept attachment, release.md step 4's kept list) is refused while any of them is not in a terminal state of its own workflow, naming each with its state; a run with no release-kept attachment landed nothing and is accepted. Deterministic, no agent.
---

# Release kept (a run closes only after the items it landed)

The release run records the items it keeps in the candidate, `<id> <head>`
per line in landing order, and attaches that list to itself as `release-kept`
before it pushes main (release.md step 4). Once main holds an item's head, the
run that pushed it is the only record of the landing, so the run's done waits
for every one of them: each item's done is presented in step 9 while the run
is still in progress. A refused item done is fixed and presented again; a run
that cannot finish is cancelled, and the next fire carries its landed items
(release.md step 2a).

Presence is read from the run's attachment list, never inferred from a failed
read, so a daemon error is never an accept. Terminal states are read from each
item's own workflow; none is named here.

```sh
set -u
for tool in satelle jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed"; exit 1; }
done
if ! run=$(satelle execution get "$SATELLE_ITEM" --json 2>&1); then
  echo "cannot read $SATELLE_ITEM: $run"
  exit 1
fi
if ! printf '%s' "$run" | jq -e 'any(.attachments[]?; .name == "release-kept")' >/dev/null; then
  echo "$SATELLE_ITEM has no release-kept attachment: it landed no item"
  exit 0
fi
if ! kept=$(satelle execution attachment "$SATELLE_ITEM" release-kept 2>&1); then
  echo "cannot read $SATELLE_ITEM's release-kept attachment: $kept"
  exit 1
fi
nl='
'
open=; closed=
while read -r id head; do
  [ -n "$id" ] || continue
  if ! js=$(satelle story get "$id" --json 2>/dev/null); then
    open="${open}cannot read $id (release-kept names it at head ${head:-?})${nl}"
    continue
  fi
  st=$(printf '%s' "$js" | jq -r '.status // empty')
  wf=$(printf '%s' "$js" | jq -r '.workflow // empty')
  term=$(satelle workflow show "$wf" --json 2>/dev/null | jq -r --arg s "$st" '.states[$s].Terminal == true' 2>/dev/null)
  if [ "$term" = true ]; then
    closed="${closed:+$closed }$id $st"
  else
    open="${open}$id is ${st:-unknown}, not terminal${nl}"
  fi
done <<KEPT
$kept
KEPT
if [ -n "$open" ]; then
  printf '%s' "$open"
  echo "$SATELLE_ITEM landed these items and has not closed them: present each one's done (release.md step 9) before the run's"
  exit 1
fi
echo "every item $SATELLE_ITEM landed is terminal: ${closed:-none listed}"
```
