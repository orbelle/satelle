---
name: satelle-estimate-actual-review
scope: system
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: CODED gate. Entering in_progress requires an estimate tag (category empty excepted) and judges each token estimate against comparable finished stories' measured actuals (`satelle story comparables`), naming the ids and ratio; rejects one outside the configured tolerance, accepts with a note when too few comparables exist. A dollar tag is no estimate. Entering done prints estimate beside token actuals; never rejects.
---

# Estimate / actual gate (coded functional check)

Scoped gate (workflow declares it on `in_progress` and `done`); the check
below IS the decision — deterministic code, no LLM. Reads `{story, from, to,
review_skill, measured_actual}` on stdin.

The estimate is a story TAG a driver writes with `satelle story estimate`, in
whichever unit it chose:

- `estimate-fresh-input:<n>` / `estimate-output:<n>` — tokens.
- `estimate-tokens:<n>` — a legacy bare token count. Never treated as fresh input.
- `estimate-minutes:<n>` — a legacy duration.

A stored `estimate-usd` tag is not an estimate. The check ignores it.

## Driver duty — compare BEFORE you record

Run `satelle story comparables <id>` first, then `satelle story estimate <id>`
with figures inside the spread it shows. This gate reads the same list and
rejects an estimate no comparable supports, so a number invented without looking
is the thing it exists to stop.

## Entering `in_progress`

Presence is required: an estimate tag in ANY unit above, or the edge is
rejected with the command sequence to run. A story of category `empty` (no
product content, taken through the workflow only to prove it or a remote) is
accepted with no estimate: it has nothing to estimate, and comparables skip it. Then every token estimate
(`estimate-output`, `estimate-fresh-input`) is judged against the **measured
actuals of comparable finished stories**:

- Comparables come from `satelle story comparables <id> --json`: finished
  (terminal by the story's own workflow), not a container (no children), carrying
  measured `actual-fresh-input` / `actual-output` tags that the binary computes
  from the ledger. Ranked same category first, then by shared tags, then recency.
  The fence passes `--limit` and the tag prefixes it ignores when counting shared
  tags (`estimate-`, `actual-`, `order:`, `workflow:`) — the verb has no default
  list. No dollar tag is read.
- Per figure, a comparable only counts if it carries that figure with a value
  above zero. The gate prints every comparable id it read, each figure's
  comparables, the median, the lowest and highest, and
  `ratio estimate/median-actual`.
- **Reject** when the estimate is below `min / tolerance` or above
  `max × tolerance` of its comparables' observed spread; the notes name the
  comparable and the figure that bound the comparison.
- **Accept** when it falls inside. When fewer than `min_comparables` comparables
  carry a figure — or `satelle story comparables` cannot be run — the edge is
  ACCEPTED with a recorded note that no comparison was available for that
  figure. No range is invented. A legacy `estimate-tokens` / `estimate-minutes`
  figure is likewise accepted with a "legacy unit, no comparison" note.
- The passing check's stdout is kept in the verdict notes (the engine does this
  for every check), so the ids, ratios and "no comparison available" notes are
  recorded on an accept, not only on a reject.

This is a spread check, never a forecast: it compares a recorded number with what
comparable stories actually cost.

### Configuration

The tolerance and the minimum comparable count are the repo's own, read by the
fence from `.satelle/satelle.toml`; unset, the defaults below apply (they are
the first lines of the script, so a repo can also override this skill by name):

```toml
[estimate_review]
tolerance = 2.0        # default 1.5 — a number >= 1; the multiple of the spread allowed
min_comparables = 3    # default 3 — fewer comparables carrying a figure means no comparison
```

## Entering `done`

The actual is never a tag this gate requires: it is `measured_actual`,
computed from the ledger by the binary (`ComputeStoryActual`) and attached to
EVERY gate payload, generically — enumeration, not something this skill
triggers by name. The check prints `fresh_input`, `output`, `cache_read` and
`cache_write` from `measured_actual.total`. It does not print a dollar figure,
it does not compute a cost band, and it contains no boundary table.
`Figures.CostBand` owns that table. `recordActual` writes the `actual-cost`
tag from `CostBand` when the result is non-empty, and writes no tag when it
is empty. Display uses `FormatCostBand`, which maps an empty band to the word
"unavailable".

Entering `done`, the check never rejects: it prints the estimate (in whichever
unit was recorded, or "no estimate recorded" when none was) beside the measured
token figures, and exits 0 regardless of the size of any overrun. When the
payload carries no `measured_actual` block at all (a computation failure), it
prints "measured actual unavailable" rather than inventing a fresh-0 figure
nothing measured. No ratio is computed on this edge.

```check
# Coded estimate/actual gate. Reads {story, from, to, review_skill,
# measured_actual} on stdin; exit 0 accepts, non-zero rejects with the reason
# on stdout. Prints token figures only — no dollar figure, no cost band.
IN=$(cat)
rest=${IN##*\"to\":\"}; to=${rest%%\"*}

# THE CONFIGURATION — defaults; the repo's [estimate_review] table in
# .satelle/satelle.toml overrides TOLERANCE and MIN_COMPARABLES.
TOLERANCE=1.5
MIN_COMPARABLES=3
LIMIT=10
IGNORE_PREFIXES="estimate- actual- order: workflow:"

# tagval <tag-name> — the value after "<tag-name>:" up to the closing quote,
# read from the story's own tags array; empty + failure when absent.
tagval() {
  local hit
  hit=$(printf '%s' "$IN" | grep -o "\"$1:[^\"]*\"" | head -1)
  [ -n "$hit" ] || return 1
  hit=${hit#\"$1:}
  printf '%s' "${hit%\"}"
}

# ledgerval <json-key> — the LAST "<key>":<number> in the payload. Own,
# each child, and Total all carry the same figure names inside
# measured_actual; Total is marshalled last (StoryActual{Own,Children,Total}),
# so the last occurrence in the whole payload is always Total's.
ledgerval() {
  printf '%s' "$IN" | grep -o "\"$1\":[0-9.eE+-]*" | tail -1 | cut -d: -f2
}

# cfgval <key> — a number from the repo's [estimate_review] table; empty when
# the table or key is absent.
cfgval() {
  sed 's/#.*//' .satelle/satelle.toml 2>/dev/null \
    | awk '/^\[estimate_review\]/{s=1;next} /^\[/{s=0} s' | tr -d '"' \
    | sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\([0-9][0-9.]*\)[[:space:]]*\$/\1/p" | head -1
}

# compare_figure <label> <estimate> <json-key> — judge one token estimate
# against the comparables in $lines (one JSON object per line). Prints what it
# read; exit 1 rejects, exit 0 accepts (including "no comparison available").
compare_figure() {
  local l id v pairs
  pairs=$(printf '%s\n' "$lines" | while IFS= read -r l; do
    [ -n "$l" ] || continue
    id=$(printf '%s' "$l" | sed -n 's/^{"id":"\([^"]*\)".*/\1/p')
    v=$(printf '%s' "$l" | grep -o "\"$3\":[0-9.eE+-]*" | head -1 | cut -d: -f2)
    [ -n "$v" ] && printf '%s %s\n' "$id" "$v"
  done)
  printf '%s\n' "$pairs" | awk -v est="$2" -v tol="$TOLERANCE" -v minc="$MIN_COMPARABLES" -v label="$1" '
    NF==2 && $2+0>0 { n++; id[n]=$1; v[n]=$2+0 }
    END {
      if (n < minc) {
        printf "no comparison available for %s: %d comparable(s) carry it, minimum %d — estimate %.0f recorded unchecked\n", label, n, minc, est
        exit 0
      }
      for (i=1;i<=n;i++) o[i]=i
      for (i=2;i<=n;i++) { k=o[i]; j=i-1; while (j>=1 && v[o[j]]>v[k]) { o[j+1]=o[j]; j-- } o[j+1]=k }
      lo=o[1]; hi=o[n]
      med = (n%2) ? v[o[(n+1)/2]] : (v[o[n/2]]+v[o[n/2+1]])/2
      printf "%s: estimate %.0f vs %d comparable(s) [", label, est, n
      for (i=1;i<=n;i++) printf "%s%s=%.0f", (i>1 ? " " : ""), id[i], v[i]
      printf "] median %.0f, min %.0f (%s), max %.0f (%s); ratio estimate/median-actual %.2f\n", med, v[lo], id[lo], v[hi], id[hi], est/med
      if (est*tol < v[lo]) {
        printf "REJECT %s: estimate %.0f is below the spread — more than %sx under the lowest comparable %s (%s %.0f)\n", label, est, tol, id[lo], label, v[lo]
        exit 1
      }
      if (est > v[hi]*tol) {
        printf "REJECT %s: estimate %.0f is above the spread — more than %sx over the highest comparable %s (%s %.0f)\n", label, est, tol, id[hi], label, v[hi]
        exit 1
      }
      printf "%s within tolerance %sx of the observed spread\n", label, tol
    }'
}

case "$to" in
 in_progress)
 cat=$(printf '%s' "$IN" | grep -oE '"category":[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
 if [ "$cat" = empty ]; then
 echo "category empty: no estimate required"
 exit 0
 fi
 units=""
 for tag in estimate-fresh-input estimate-output estimate-tokens estimate-minutes; do
 if tagval "$tag" >/dev/null; then units="$units $tag"; fi
 done
 if [ -z "$units" ]; then
 echo "no plan estimate recorded — run: satelle story comparables <id>, then satelle story estimate <id> --fresh-input <n> --output <n> within the comparables' spread (or the legacy --tokens <n> / --time <dur>), then retry the edge; satelle-estimate-actual-review enforces it"
 exit 1
 fi

 tol_src=default; min_src=default
 if v=$(cfgval tolerance) && [ -n "$v" ]; then TOLERANCE=$v; tol_src=configured; fi
 if v=$(cfgval min_comparables) && [ -n "$v" ]; then MIN_COMPARABLES=$v; min_src=configured; fi
 if ! awk -v t="$TOLERANCE" 'BEGIN{exit !(t+0 >= 1)}'; then
 echo "[estimate_review] tolerance must be a number >= 1 (got $TOLERANCE)"
 exit 1
 fi
 case "$MIN_COMPARABLES" in ''|*[!0-9]*|0)
 echo "[estimate_review] min_comparables must be a whole number >= 1 (got $MIN_COMPARABLES)"
 exit 1;;
 esac
 echo "estimate gate: tolerance $TOLERANCE ($tol_src), min_comparables $MIN_COMPARABLES ($min_src)"

 for tag in estimate-tokens estimate-minutes; do
 if v=$(tagval "$tag"); then echo "$tag:$v is a legacy unit — no comparison available"; fi
 done

 # Only a token estimate has anything to compare; legacy units end here.
 if ! tagval estimate-output >/dev/null && ! tagval estimate-fresh-input >/dev/null; then exit 0; fi

 sid=$(printf '%s' "$IN" | grep -oE '"id":[[:space:]]*"[^"]+"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
 flags=""
 for p in $IGNORE_PREFIXES; do flags="$flags --ignore-prefix $p"; done
 # shellcheck disable=SC2086
 raw=$(satelle story comparables "$sid" --json --limit "$LIMIT" $flags 2>/dev/null) || raw=""
 lines=$(printf '%s' "$raw" | tr -d ' \t\r\n' | sed 's/{"id":/\n{"id":/g' | grep '^{"id":' || true)
 ids=$(printf '%s\n' "$lines" | sed -n 's/^{"id":"\([^"]*\)".*/\1/p' | tr '\n' ' ')
 if [ -z "$raw" ]; then
 echo "no comparison available: satelle story comparables could not be read for $sid — estimate recorded unchecked"
 exit 0
 fi
 echo "read $(printf '%s\n' "$lines" | grep -c .) comparable(s) for $sid: ${ids:-none}"

 rc=0
 for fig in "estimate-output output output" "estimate-fresh-input fresh-input fresh_input"; do
 set -- $fig
 if est=$(tagval "$1"); then
 case "$est" in ''|*[!0-9.]*) echo "$1:$est is not a number — no comparison available"; continue;; esac
 compare_figure "$2" "$est" "$3" || rc=1
 fi
 done
 if [ "$rc" -ne 0 ]; then
 echo "estimate outside its comparables' spread — re-run satelle story comparables $sid, record satelle story estimate $sid within it, then retry the edge"
 exit 1
 fi
 exit 0;;
 done)
 if ! printf '%s' "$IN" | grep -q '"measured_actual"'; then
 # No computed actual on this payload (an unwired store, a missing item) —
 # say so rather than inventing a fresh-0 figure nothing measured.
 actual="measured actual unavailable"
 else
 fresh=$(ledgerval fresh_input); out=$(ledgerval output)
 cread=$(ledgerval cache_read); cwrite=$(ledgerval cache_write)
 actual="actual (ledger-computed): fresh ${fresh:-0}, output ${out:-0}, cache read ${cread:-0}, cache write ${cwrite:-0}"
 fi
 if v=$(tagval estimate-fresh-input); then
 o=$(tagval estimate-output) || o=0
 echo "estimate: $v fresh input + $o output tokens | $actual"; exit 0
 fi
 if v=$(tagval estimate-tokens); then
 echo "estimate: $v tokens (legacy unit) | $actual"; exit 0
 fi
 if v=$(tagval estimate-minutes); then
 echo "estimate: $v minutes (legacy unit) | $actual"; exit 0
 fi
 echo "no estimate recorded on this story | $actual"
 exit 0;;
esac
exit 0
```
