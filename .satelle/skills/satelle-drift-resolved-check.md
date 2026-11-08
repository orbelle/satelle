---
name: satelle-drift-resolved-check
type: check
description: The drift workflow's done gate (open → done). A drift story closes only while a drift review run is in progress whose attached drift-report lists the story's drift-key under "No longer found" and the re-check holds there — a substrate kind when drift-check ran (exit 0 or 1) and its findings lack the key; prose and constitution only with a "RESOLVED <drift-key>" line from this run's judges and the key absent from their findings, so absence alone never closes. Otherwise rejects naming the reason. Deterministic, no agent.
---

# Drift resolved (a drift story closes on the drift run's evidence)

A drift story records one finding (its `drift-key: <kind>:<file>:<subject>`
body line, raised by `scripts/drift-stories.sh`); the fix is other work. The
record closes only inside the drift review run that follows the fix (its
procedure is `.satelle/skills/drift-review.md`), on that run's `drift-report`
attachment, so the evidence of every close stays on the ledger: this gate's
run on the story, and the report on the run.

The report's sections this gate reads, each a `## <name>` heading followed by
its lines up to the next `## ` heading:

- `## Checks`: the line `- scripts/drift-check.sh: drift-check exit <N>`.
- `## Substrate findings`, `## Constitution findings`, `## Claim findings`:
  the run's findings, one `<file>:<line>: <kind>: <subject> — <why>` line
  each. Their keys come from `scripts/drift-stories.sh --keys`, the one place
  a finding maps to its key.
- `## No longer found`: one line per open drift story the run no longer
  finds, the drift-key first (alone, or followed by a tab and more fields).
- `## Resolved by the judges`: a judge's `RESOLVED <drift-key>` line (alone,
  or followed by ` — <evidence>`) for each prose or constitution finding it
  re-checked and found fixed.

The story passes only when its key is listed under `## No longer found` and
the re-check holds:

- **Substrate kinds** (command, flag, topic, link, skill, config, the kinds
  drift-check prints): drift-check ran (its exit line reads 0 or 1; exit 2
  means it could not check) and the key is absent from the findings.
- **Prose and constitution** (judged by drift-claims and drift-constitution):
  a `RESOLVED <drift-key>` line under `## Resolved by the judges` and the key
  absent from the findings. A judge accepts when a finding is already carried
  by an open drift story, so a key missing from the findings proves nothing
  alone: without the RESOLVED line the story stays open.

The run is read from the attachment list first, so a daemon error is never an
accept.

```sh
set -u
task=tas_a45ba066
for tool in satelle jq bash; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed"; exit 1; }
done
[ -f scripts/drift-stories.sh ] || { echo "scripts/drift-stories.sh is not in $(pwd): it maps a finding to its drift-key"; exit 1; }
if ! story=$(satelle story get "$SATELLE_ITEM" --json 2>&1); then
  echo "cannot read $SATELLE_ITEM: $story"
  exit 1
fi
key=$(printf '%s' "$story" | jq -r '(.body // "") | split("\n") | map(select(startswith("drift-key: "))) | .[0] // "" | ltrimstr("drift-key: ")')
if [ -z "$key" ]; then
  echo "no drift-key: $SATELLE_ITEM's body has no 'drift-key: ' line, so no drift run can find it gone"
  exit 1
fi
kind=${key%%:*}
case $kind in
  command | flag | topic | link | skill | config) judged= ;;
  prose | constitution) judged=yes ;;
  *) echo "unknown kind: $SATELLE_ITEM's drift-key $key has kind $kind"; exit 1 ;;
esac
if ! runs=$(satelle execution list --parent "$task" --status in_progress --json 2>&1); then
  echo "cannot list the runs of the drift review task $task: $runs"
  exit 1
fi
exe=$(printf '%s' "$runs" | jq -r '.[0].id // empty')
if [ -z "$exe" ]; then
  echo "no drift review run in progress: a drift story closes only inside a run of $task (release.md step 11)"
  exit 1
fi
if ! run=$(satelle execution get "$exe" --json 2>&1); then
  echo "cannot read the drift run $exe: $run"
  exit 1
fi
if ! printf '%s' "$run" | jq -e 'any(.attachments[]?; .name == "drift-report")' >/dev/null; then
  echo "no drift-report: the drift run $exe has attached none (drift-review.md step 3)"
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
if ! section 'No longer found' | awk -F'\t' -v k="$key" '$1 == k { f = 1 } END { exit !f }'; then
  echo "key not listed: $key is not under 'No longer found' in the drift run $exe's drift-report"
  exit 1
fi
if ! found=$({ section 'Substrate findings'; section 'Constitution findings'; section 'Claim findings'; } | bash scripts/drift-stories.sh --keys 2>&1); then
  echo "cannot read the findings of the drift run $exe's drift-report: $found"
  exit 1
fi
if [ -z "$judged" ]; then
  ran=$(section 'Checks' | sed -n 's/^- scripts\/drift-check\.sh: drift-check exit \([0-9][0-9]*\)$/\1/p' | head -1)
  case $ran in
    0 | 1) ;;
    *) echo "drift-check failed: the drift run $exe's drift-report reads drift-check exit ${ran:-(none)}, so it did not check $key"; exit 1 ;;
  esac
  evidence="drift-check exit $ran"
else
  evidence=$(section 'Resolved by the judges' | awk -v k="RESOLVED $key" '$0 == k || index($0, k " — ") == 1 { print; exit }')
  if [ -z "$evidence" ]; then
    echo "no RESOLVED line: this run's judges did not state 'RESOLVED $key' in the drift run $exe's drift-report; absence from the findings alone never closes a $kind story"
    exit 1
  fi
fi
if printf '%s\n' "$found" | grep -qxF -- "$key"; then
  echo "still found: $key is among the drift run $exe's findings"
  exit 1
fi
echo "$key is no longer found by the drift run $exe: listed under 'No longer found', absent from its findings, $evidence"
```
