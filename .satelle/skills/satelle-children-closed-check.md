---
name: satelle-children-closed-check
type: check
description: A container may not close while any of its children is open. Lists the item's children through the CLI and rejects while any is not in a terminal state of its workflow.
---

# Children closed

Rejects the transition while the item has a child that is not in a terminal
state. Deterministic; no agent.

```sh
open=$(satelle story list --parent "$SATELLE_ITEM" --open)
if [ -n "$open" ]; then
  echo "open children of $SATELLE_ITEM:"
  echo "$open"
  exit 1
fi
echo "no open children of $SATELLE_ITEM"
```
