---
name: satelle-definition-unchanged-check
type: check
description: Rejects leaving the coding step when the item's definition (title, body, acceptance criteria, category) was edited after its plan was accepted, so no reviewer judged that edit. Reads the item's own ledger; deterministic, no agent.
---

# Definition unchanged

The plan gates judged the item's definition when it moved into coding. An edit
to the title, body, acceptance criteria or category after that move was never
judged, so this check refuses to let the work out of coding until the item goes
back through planning (`satelle story set <id> --status plan --session <session>`, then forward
again, so the plan gates see the edit).

It reads the item's ledger: the latest accepted transition into the coding state
(the `from` state of this transition, `$SATELLE_FROM`), then every
`item.updated` event after it. Tags, parent, assignees and workflow are not the
definition and are ignored.

Ported to the kernel 2026-11-06 (the old script read a transition payload and
called commands the rebuild removed).

```sh
set -eu
ledger=$(satelle ledger list "$SATELLE_ITEM" --json)
printf '%s' "$ledger" | jq -r --arg into "$SATELLE_FROM" '
  (map(.kind == "item.transitioned" and .payload.to == $into) | rindex(true)) as $i
  | if $i == null then "no accepted move into \($into) is on the ledger" | halt_error(1)
    else .[$i + 1:]
      | map(select(.kind == "item.updated") | .payload | keys[]
            | select(. == "title" or . == "body" or . == "criteria" or . == "category"))
      | unique
      | if length == 0 then "the definition is unchanged since the item moved into \($into)"
        else "the definition was edited after the item moved into \($into) (\(join(", "))); move it back to plan so the plan gates judge the edit\n" | halt_error(1)
        end
    end'
```
