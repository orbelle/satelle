---
name: satelle-caused-by-check
type: check
description: Functional check on a delivery story's first move out of backlog, ahead of story review. Refuses a story of category bug or fix that carries no caused-by tag, naming the tag to add (caused-by:<the story id that introduced the defect> or caused-by:unknown), and one whose caused-by names no item on the ledger. Any other category is not asked for one. Deterministic, no agent.
---

# Caused-by check (an escaped defect names its cause)

A bug or fix story says which story introduced the defect, so the ledger can
say which gates and judges let it through
([[satelle-conventional-tags]]). The story is `$SATELLE_ITEM`; its category
and tags are read with `satelle story get <id> --json --session <session>`.

- Category neither `bug` nor `fix`: accept, asking for nothing.
- No `caused-by:` tag: refuse, naming the tag to add.
- `caused-by:unknown`: accept.
- `caused-by:<id>`: accept when every such id resolves to an item
  (`satelle <kind> get <id>`); refuse naming each that does not.

Exit 0 accepts; exit 1 refuses or could not read the story.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the story cannot be read"
  exit 1
fi
if ! js=$(satelle story get "$sid" --json 2>&1); then
  echo "could not read $sid:"
  echo "$js"
  exit 1
fi
cat=$(printf '%s' "$js" | jq -r '.category // ""' 2>/dev/null) || { echo "could not read the category of $sid"; exit 1; }
case "$cat" in
  bug|fix) ;;
  *)
    echo "$sid is category ${cat:-none}, not bug or fix: no caused-by tag is asked for"
    exit 0
    ;;
esac
causes=$(printf '%s' "$js" | jq -r '(.tags // [])[] | select(startswith("caused-by:")) | sub("^caused-by:"; "")' 2>/dev/null) || { echo "could not read the tags of $sid"; exit 1; }
if [ -z "$causes" ]; then
  echo "$sid is a $cat story with no caused-by tag: add caused-by:<the id of the story that introduced the defect>, or caused-by:unknown when it cannot be found (satelle story set $sid --add-tag caused-by:<id>)"
  exit 1
fi
missing=()
while IFS= read -r c; do
  [ -z "$c" ] && continue
  [ "$c" = "unknown" ] && continue
  kind=
  case "$c" in
    sto_*) kind=story ;;
    tsk_*) kind=task ;;
    exe_*) kind=execution ;;
    que_*) kind=queue ;;
  esac
  if [ -z "$kind" ] || ! satelle "$kind" get "$c" >/dev/null 2>&1; then
    missing+=("caused-by:$c")
  fi
done <<EOF
$causes
EOF
if [ ${#missing[@]} -gt 0 ]; then
  echo "$sid names a cause that is no item on the ledger: ${missing[*]}; tag the id of the story that introduced the defect, or caused-by:unknown"
  exit 1
fi
echo "$sid ($cat) names its cause: $(printf '%s' "$causes" | tr '\n' ' ' | sed 's/ $//')"
exit 0
```
