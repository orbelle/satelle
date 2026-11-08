---
name: satelle-drift-closures-check
type: check
description: The drift review run's after-review of its closures (runbook in_progress → done, drift runs only). Reads the run's attached drift-report's "Closed" and "Stayed open" lists (drift-review.md step 4) and every story's state from the kernel; rejects when a story listed closed is not done, or an open drift story is missing from the stayed-open list or listed there without a reason. Deterministic, no agent.
---

# Drift closures (the report's closed and stayed-open lists hold)

Step 4 of the drift review (`.satelle/skills/drift-review.md`) presents done
each open drift story the run no longer finds that is already on the drift
route, and re-attaches the run's `drift-report` with two sections, each a
`## <name>` heading followed by its lines up to the next `## ` heading:

- `## Closed`: `<story id>\t<drift-key>`, each story the run closed.
- `## Stayed open`: `<story id>\t<drift-key or ->\t<reason>`, each open drift
  story and why it stayed open.

This check holds that list to the kernel, so the report never claims a close
that did not happen nor leaves an open drift story unaccounted for. A story
listed closed must be `done`; every drift story open now must be listed as
stayed open with a reason. A report with neither section passes only when no
drift story is open and none is claimed closed.

```sh
set -u
for tool in satelle jq awk; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed"; exit 1; }
done
exe=$SATELLE_ITEM
if ! run=$(satelle execution get "$exe" --json 2>&1); then
  echo "cannot read the drift run $exe: $run"
  exit 1
fi
if ! printf '%s' "$run" | jq -e 'any(.attachments[]?; .name == "drift-report")' >/dev/null; then
  echo "no drift-report: the drift run $exe has attached none (drift-review.md steps 3 and 4)"
  exit 1
fi
if ! report=$(satelle execution attachment "$exe" drift-report 2>&1); then
  echo "cannot read the drift run $exe's drift-report: $report"
  exit 1
fi
# section <name>: the report's lines under "## <name>", up to the next "## ".
section() {
  printf '%s\n' "$report" | awk -v h="## $1" '$0 == h { on = 1; next } /^## / { on = 0 } on && NF'
}
if ! open=$(satelle story list --tag drift --open --json 2>&1); then
  echo "cannot list the open drift stories: $open"
  exit 1
fi
if ! ids=$(printf '%s' "$open" | jq -r '.[].id' 2>&1); then
  echo "cannot read the open drift stories: $ids"
  exit 1
fi
closed=$(section 'Closed' | awk -F'\t' '{ print $1 }')
stayed=$(section 'Stayed open')
bad=0
for id in $closed; do
  if ! story=$(satelle story get "$id" --json 2>&1); then
    echo "cannot read $id, listed closed: $story"
    exit 1
  fi
  st=$(printf '%s' "$story" | jq -r '.status')
  if [ "$st" != done ]; then
    echo "listed closed but not done: $id is $st"
    bad=1
  fi
done
for id in $ids; do
  if ! printf '%s\n' "$stayed" | awk -F'\t' -v id="$id" '$1 == id && $3 != "" { f = 1 } END { exit !f }'; then
    echo "missing from Stayed open: the open drift story $id is not listed with a reason"
    bad=1
  fi
done
if [ "$bad" -ne 0 ]; then
  echo "the drift run $exe's drift-report does not match the stories' states: run drift-review.md steps 3 and 4 again"
  exit 1
fi
echo "the drift run $exe's drift-report holds: $(printf '%s\n' "$closed" | grep -c .) closed and done, $(printf '%s\n' "$ids" | grep -c .) open drift stories each listed as stayed open with a reason"
```
