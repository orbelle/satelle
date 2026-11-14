---
name: satelle-change-judged-check
type: check
description: Rejects a move when the item's change is no longer the one its latest accepting model gate saw, so a commit made after the review cannot reach a terminal state unjudged. Reads the ledger and `diff --json`; deterministic, no agent.
---

# Change judged

A terminal state is reached only with a change a judge accepted. Every model
gate (a skill that is not `type: check`) records the change it saw: its base,
head, branch and the hash of the diff alone. `satelle <kind> diff <id> --json --session <session>`
reports those verdicts as `judged`, each with `now`, the hash of the item's
change today over the same base, branch and paths (the judged branch when it
still resolves, else the presented tree).

This check takes the latest accept (or carried) verdict that recorded a change
and accepts only when its `now` equals its `judged`. When they differ, as when
a commit landed after the review, it rejects, naming the gate and its
transition: return the item to that transition's from-state, present its
to-state again so the gate judges the new change, then present `$SATELLE_TO`
again. An item no model gate accepted a change on (verdicts recorded before
satelle recorded changes included) passes.

The rule was the binary's until sto_035f3007 (added by sto_e2cec5df); it is
this repo's configuration now, placed first on each transition into `done`
that has no judge of its own.

```sh
set -eu
ledger=$(satelle ledger list "$SATELLE_ITEM" --json)
kind=$(printf '%s' "$ledger" | jq -r 'map(select(.kind == "item.created"))[0].payload.kind // empty')
judged=$(printf '%s' "$ledger" | jq '[.[] | select(.kind == "gate.run") | .payload
  | select(.change != null and (.result == "accept" or .result == "carried"))] | length')
if [ "$judged" -eq 0 ]; then
  echo "no judge recorded a change"
  exit 0
fi
if [ -z "$kind" ]; then
  echo "$SATELLE_ITEM: its ledger names no kind" >&2
  exit 1
fi
report=$(satelle "$kind" diff "$SATELLE_ITEM" --json)
printf '%s' "$report" | jq -r --arg item "$SATELLE_ITEM" --arg to "$SATELLE_TO" '
  [(.judged // [])[] | select(.result == "accept" or .result == "carried")] | last
  | if . == null then "no judge recorded a change"
    elif .now == .judged then "the change is the one gate \(.gate) accepted (\(.judged[0:12]))"
    else (.transition | split("->")) as [$from, $into]
      | "\($item): its change is not the one gate \(.gate) accepted on \($from) -> \($into) (judged \(.judged[0:12]), now \(.now[0:12])); re-run \(.gate): return the item to \($from), present \($into) again, then present \($to) again\n" | halt_error(1)
    end'
```
