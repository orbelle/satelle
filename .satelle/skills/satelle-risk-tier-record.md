---
name: satelle-risk-tier-record
type: check
description: After check of the risk-assess gate. Reads the judge's first notes line (TIER: low or TIER: high) and sets the item's risk tag to it, removing any other risk tag; a rating it cannot read is recorded as high. Never refuses the verdict. Deterministic, no agent.
---

# Risk tier record (the after of risk-assess)

The gate's judge rates the story; this check writes the rating where
`[review]` in `.satelle/satelle.toml` reads it: the item's tag whose key is that
table's `tier_tag`. It reads the gate's result on stdin (`.notes`, or the
first member's notes for a panel), keeps one tag of that key, and exits 0 on
every path, so the rating never blocks the move. A missing or unreadable
rating is `high`, so a failed assessment never lightens review.

```sh
#!/usr/bin/env bash
set -u
key=$(sed -n 's/^tier_tag *= *"\([^"]*\)".*/\1/p' .satelle/satelle.toml | head -1)
if [ -z "$key" ]; then
  echo "no tier_tag in .satelle/satelle.toml [review]: nothing recorded"
  exit 0
fi
in=$(cat)
notes=$(printf '%s' "$in" | jq -r '(.notes // "") + "\n" + ((.members // [])[0].notes // "")' 2>/dev/null)
tier=$(printf '%s\n' "$notes" | sed -nE 's/^ *TIER: *(low|high) *$/\1/p' | head -1)
why=
if [ -z "$tier" ]; then
  tier=high
  why=" (no TIER line in the judge's notes, so the higher tier)"
fi
args=()
for t in $(satelle story get "$SATELLE_ITEM" --json | jq -r --arg k "$key:" '.tags // [] | .[] | select(startswith($k))'); do
  [ "$t" = "$key:$tier" ] || args+=(--remove-tag "$t")
done
if ! satelle story set "$SATELLE_ITEM" --add-tag "$key:$tier" ${args[@]+"${args[@]}"} >/dev/null 2>&1; then
  echo "could not record $key:$tier on $SATELLE_ITEM; the item keeps its tags and code-ac runs its own judges"
  exit 0
fi
echo "recorded $key:$tier on $SATELLE_ITEM$why"
exit 0
```
