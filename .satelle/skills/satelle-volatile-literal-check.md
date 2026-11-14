---
name: satelle-volatile-literal-check
type: check
description: Refuses the move while `satelle validate --json --session <session>` lists any volatile line (text matching a pattern of .satelle/volatile.toml that is not dated history), naming each by file and line, and while a line the item adds carries volatile.toml's exempt marker. Deterministic, no agent; reads validate, the ledger, `diff --json` and git.
---

# Volatile literals

[[satelle-reference-not-restate]] allows a volatile fact (a version, a count, a
status, a model id, a present-tense phrase) only as dated history; everywhere else the
text names the command, file or link that answers it. `.satelle/volatile.toml`
holds the patterns that catch such text by its wording, and `satelle validate
--json --session <session>` lists each matching line under `volatile`, without
failing. This check is where a listed line fails a move.

It refuses when:

- validate lists any volatile line, anywhere in the
  substrate: an edit to `volatile.toml` itself can surface lines in files the
  change did not touch, and this repo keeps that list empty;
- a line the item adds (in a changed file under `.satelle/` other than
  `volatile.toml`) carries the `exempt` marker `volatile.toml` names. The
  principle grants no marker exemption, so a new line is never silenced that
  way.

The fix for either is the same: write the reference (the command that prints
the value, the file that holds it, the `[[link]]` that owns it), or write the
fact as dated history with the date it held. The patterns read no line of a
fenced code block; `satelle-reference-review` judges those by meaning.

```sh
set -u
for tool in jq satelle git; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the volatile lines cannot be read"; exit 1; }
done
item=$SATELLE_ITEM
fail=0
report=$(satelle validate --json </dev/null 2>/dev/null) || true
if ! printf '%s' "$report" | jq -e 'type == "object"' >/dev/null 2>&1; then
  echo "validate gave no JSON report, so the volatile lines cannot be read"
  exit 1
fi
lines=$(printf '%s' "$report" | jq -r '(.volatile // [])[] | "\(.file):\(.line): \(.kind // "volatile"): \(.subject // "") — \(.why // "")"')
if [ -n "$lines" ]; then
  n=$(printf '%s\n' "$lines" | wc -l | tr -d ' ')
  echo "$n volatile line(s); write each as a reference, or as dated history with the date it held ([[satelle-reference-not-restate]]):"
  printf '%s\n' "$lines"
  fail=1
fi

root=$(git rev-parse --show-toplevel 2>/dev/null) || root=.
marker=$(sed -n 's/^exempt *= *"\([^"]*\)".*/\1/p' "$root/.satelle/volatile.toml" 2>/dev/null | head -n 1)
if [ -n "$marker" ]; then
  kind=$(satelle ledger list "$item" --json </dev/null | jq -r '[(. // [])[] | select(.kind == "item.created")][0].payload.kind // empty') || kind=
  paths=
  if [ -n "$kind" ]; then
    paths=$(satelle "$kind" diff "$item" --json </dev/null | jq -r '(.paths // [])[]' 2>/dev/null) || paths=
  fi
  base=${SATELLE_BASE:-}
  marked=
  for p in $paths; do
    case "$p" in
      .satelle/volatile.toml) continue ;;
      .satelle/*) ;;
      *) continue ;;
    esac
    [ -f "$root/$p" ] || continue
    if [ -n "$base" ] && git -C "$root" cat-file -e "$base:$p" 2>/dev/null; then
      added=$(git -C "$root" diff -U0 --no-color "$base" -- "$p" | grep -v '^+++ ' | sed -n 's/^+//p')
    else
      added=$(cat "$root/$p")
    fi
    if printf '%s\n' "$added" | grep -qF -- "$marker"; then
      marked="$marked$p
"
    fi
  done
  if [ -n "$marked" ]; then
    echo "line(s) this change adds carry volatile.toml's exempt marker, which [[satelle-reference-not-restate]] does not grant; write a reference or dated history instead, in:"
    printf '%s' "$marked"
    fail=1
  fi
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "no volatile line is listed and no added line carries the exempt marker"
```
