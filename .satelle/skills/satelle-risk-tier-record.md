---
name: satelle-risk-tier-record
type: check
description: After check of the risk-assess gate. Reads the first line of the judge's own notes (TIER: low or TIER: high) and sets the item's risk tag to it, removing any other risk tag; a rating it cannot read is recorded as high. It never refuses for the rating; it refuses only when the item's tags cannot be read or written, so a stale lighter tag never stays active. Deterministic, no agent.
---

# Risk tier record (the after of risk-assess)

The gate's judge rates the story; this check writes the rating where
`[review]` in `.satelle/satelle.toml` reads it: the item's tag whose key is that
table's `tier_tag`. It reads the gate's result on stdin and takes the rating
only from the first line of the judge's own notes (the first panel member's,
else the gate's), so a `TIER:` line quoted from the story or the plan further
down cannot set it. It keeps exactly one tag of that key. A missing or
unreadable rating is `high`, so a failed assessment never lightens review.

It exits 0 for every rating. It exits 1 when the item's tags cannot be read or
the new tag cannot be written: the item might keep an earlier, lighter tag that
`[review]` would then read, so the move is refused until the tag is recorded.

```sh
#!/usr/bin/env bash
set -u -o pipefail
key=$(sed -n 's/^tier_tag *= *"\([^"]*\)".*/\1/p' .satelle/satelle.toml | head -1)
if [ -z "$key" ]; then
  echo "no tier_tag in .satelle/satelle.toml [review]: nothing recorded"
  exit 0
fi
in=$(cat)
first=$(printf '%s' "$in" | jq -r '((.members // [])[0].notes // .notes // "")' 2>/dev/null | head -1)
tier=$(printf '%s\n' "$first" | sed -n 's/^ *TIER: *\(low\|high\) *$/\1/p')
why=
if [ -z "$tier" ]; then
  tier=high
  why=" (no TIER on the first line of the judge's notes, so the higher tier)"
fi
if ! tags=$(satelle story get "$SATELLE_ITEM" --json | jq -r '.tags // [] | .[]'); then
  echo "could not read the tags of $SATELLE_ITEM, so $key:$tier is not recorded and an earlier $key tag may still be read: refused"
  exit 1
fi
args=()
while IFS= read -r t; do
  case "$t" in
    "$key:$tier") ;;
    "$key:"*) args+=(--remove-tag "$t") ;;
  esac
done <<EOF
$tags
EOF
if ! satelle story set "$SATELLE_ITEM" --add-tag "$key:$tier" "${args[@]}" >/dev/null 2>&1; then
  echo "could not record $key:$tier on $SATELLE_ITEM, and an earlier $key tag may still be read: refused"
  exit 1
fi
echo "recorded $key:$tier on $SATELLE_ITEM$why"
exit 0
```
