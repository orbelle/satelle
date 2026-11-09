---
name: satelle-drift-resolved-check
type: check
description: The drift workflow's done gate (open → done). A drift item's blocking keys are its "drift-key:" lines of every kind but prose; its "drift-warning:" lines and prose keys never block. Each blocking key must be carried — the item is tagged drift-carried-by:<fix> and that fix item is open on the drift workflow with the key in its body — or resolved by the in-progress drift review run's drift-report, which lists it under "No longer found" and re-checks it (a substrate kind when drift-check ran, exit 0 or 1, and its findings lack the key; constitution only with a "RESOLVED <drift-key>" line and the key absent). An item with only warnings accepts; one with no line at all accepts only when the fix item it is carried by names it in a drift-warning. Otherwise rejects naming each key and why. Deterministic, no agent.
---

# Drift resolved (a drift item closes when each finding is carried or fixed)

A drift item records findings: one `drift-key: <kind>:<file>:<subject>` body
line each (a release's fix item, raised by `scripts/drift-fix.sh`, lists many;
an older per-finding drift story one), and warnings, one
`drift-warning: <key>` line each. The fix is other work. Warnings (the
drift-claims judge's statements the source does not implement) and keys of kind
prose never block: only the other keys do.

A blocking key passes in one of two ways, and the evidence stays on the
ledger: this gate's run on the item, and the fix item or the run's report.

- **Carried.** The item carries the tag `drift-carried-by:<fix>`, `<fix>` is
  another item, open on the drift workflow, and its body has the line
  `drift-key: <key>` or `drift-warning: <key>`. The finding lives on in that
  fix item; no drift run is needed (`scripts/drift-fix.sh --close`).
- **Resolved.** A drift review run is in progress (its procedure is
  `.satelle/skills/drift-review.md`) and its attached `drift-report` lists the
  key under `## No longer found`, and the re-check holds:
  - substrate kinds (command, flag, topic, link, skill, config, the kinds
    drift-check prints): drift-check ran (its exit line reads 0 or 1; exit 2
    means it could not check) and the key is absent from the findings;
  - constitution: a `RESOLVED <drift-key>` line under
    `## Resolved by the judges` and the key absent from the findings. A judge
    accepts when a finding is already carried, so absence alone proves
    nothing.

The report's sections this gate reads, each a `## <name>` heading followed by
its lines up to the next `## ` heading: `## Checks` (the line
`- scripts/drift-check.sh: drift-check exit <N>`), `## Substrate findings`,
`## Constitution findings` and, in older reports, `## Claim findings` (one
finding line each; their keys come from `scripts/drift-fix.sh --keys`, the one
place a finding maps to its key), `## No longer found` (the key first, alone
or followed by a tab and more fields) and `## Resolved by the judges`.

An item with no `drift-key:` and no `drift-warning:` line accepts only when
it is carried and its fix item has a `drift-warning: prose:<item id>:` line
(`drift-fix.sh --open-findings` carries a keyless item that way); otherwise it
rejects with "no drift-key". Every read is checked first, so a daemon error is
never an accept.

```sh
set -u
task=tas_a45ba066
for tool in satelle jq bash; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed"; exit 1; }
done
[ -f scripts/drift-fix.sh ] || { echo "scripts/drift-fix.sh is not in $(pwd): it maps a finding to its drift-key"; exit 1; }
if ! story=$(satelle story get "$SATELLE_ITEM" --json 2>&1); then
  echo "cannot read $SATELLE_ITEM: $story"
  exit 1
fi
lines() { printf '%s' "$story" | jq -r --arg p "$1" '(.body // "") | split("\n")[] | select(startswith($p)) | ltrimstr($p)'; }
keys=$(lines 'drift-key: ')
warnings=$(lines 'drift-warning: ')
blocking=$(printf '%s\n' "$keys" | awk 'NF && $0 !~ /^prose:/')
fix=$(printf '%s' "$story" | jq -r '[.tags[]? | select(startswith("drift-carried-by:")) | ltrimstr("drift-carried-by:")] | .[0] // empty')
# The fix item, when the tag names one: another item, open on the drift
# workflow; fixbody is its body, or empty when it does not carry anything.
fixbody=
fixwhy="no drift-carried-by:<fix> tag"
if [ -n "$fix" ]; then
  if [ "$fix" = "$SATELLE_ITEM" ]; then
    fixwhy="it is tagged drift-carried-by itself"
  elif ! open=$(satelle story list --tag drift --open --json 2>&1); then
    echo "cannot list the open drift items: $open"
    exit 1
  elif ! printf '%s' "$open" | jq -e --arg f "$fix" 'any(.[]; .id == $f and .workflow == "drift")' >/dev/null; then
    fixwhy="its fix item $fix is not open on the drift workflow"
  else
    fixbody=$(printf '%s' "$open" | jq -r --arg f "$fix" '.[] | select(.id == $f) | .body // ""')
    fixwhy="its fix item $fix does not list it"
  fi
fi
carries() { [ -n "$fixbody" ] && printf '%s\n' "$fixbody" | grep -qxF -e "drift-key: $1" -e "drift-warning: $1"; }
if [ -z "$keys" ] && [ -z "$warnings" ]; then
  if [ -n "$fixbody" ] && printf '%s\n' "$fixbody" | grep -q "^drift-warning: prose:$SATELLE_ITEM:"; then
    echo "$SATELLE_ITEM has no drift-key; carried by $fix as a warning, which never blocks"
    exit 0
  fi
  echo "no drift-key: $SATELLE_ITEM's body has no 'drift-key: ' line, so no drift run can find it gone, and $fixwhy as a warning"
  exit 1
fi
if [ -z "$blocking" ]; then
  echo "$SATELLE_ITEM has only warnings, which never block: $(printf '%s\n' "$keys" "$warnings" | grep -c .) line(s)"
  exit 0
fi
# The drift run's report, read once and only when a key is not carried.
report=
runwhy=
load_report() {
  [ -n "$report$runwhy" ] && return 0
  if ! runs=$(satelle execution list --parent "$task" --status in_progress --json 2>&1); then
    echo "cannot list the runs of the drift review task $task: $runs"
    exit 1
  fi
  exe=$(printf '%s' "$runs" | jq -r '.[0].id // empty')
  if [ -z "$exe" ]; then
    runwhy="no drift review run in progress: a drift key not carried closes only inside a run of $task (release.md step 11)"
    return 0
  fi
  if ! run=$(satelle execution get "$exe" --json 2>&1); then
    echo "cannot read the drift run $exe: $run"
    exit 1
  fi
  if ! printf '%s' "$run" | jq -e 'any(.attachments[]?; .name == "drift-report")' >/dev/null; then
    runwhy="no drift-report: the drift run $exe has attached none (drift-review.md step 3)"
    return 0
  fi
  if ! report=$(satelle execution attachment "$exe" drift-report 2>&1); then
    echo "cannot read the drift run $exe's drift-report: $report"
    exit 1
  fi
  if ! found=$({ section 'Substrate findings'; section 'Constitution findings'; section 'Claim findings'; } | bash scripts/drift-fix.sh --keys 2>&1); then
    echo "cannot read the findings of the drift run $exe's drift-report: $found"
    exit 1
  fi
}
# section <name>: the report's lines under "## <name>", up to the next "## ".
section() {
  printf '%s\n' "$report" | awk -v h="## $1" '$0 == h { on = 1; next } /^## / { on = 0 } on && NF'
}
# resolved <key>: prints why it is resolved and returns 0, or why not and 1.
resolved() {
  local key=$1 kind=${1%%:*} ran evidence
  case $kind in
    command | flag | topic | link | skill | config | constitution) ;;
    *) echo "unknown kind: $key has kind $kind"; return 1 ;;
  esac
  load_report
  [ -z "$runwhy" ] || { echo "$runwhy"; return 1; }
  if ! section 'No longer found' | awk -F'\t' -v k="$key" '$1 == k { f = 1 } END { exit !f }'; then
    echo "key not listed: $key is not under 'No longer found' in the drift run $exe's drift-report"
    return 1
  fi
  if [ "$kind" = constitution ]; then
    evidence=$(section 'Resolved by the judges' | awk -v k="RESOLVED $key" '$0 == k || index($0, k " — ") == 1 { print; exit }')
    if [ -z "$evidence" ]; then
      echo "no RESOLVED line: this run's judges did not state 'RESOLVED $key' in the drift run $exe's drift-report; absence from the findings alone never closes a $kind finding"
      return 1
    fi
  else
    ran=$(section 'Checks' | sed -n 's/^- scripts\/drift-check\.sh: drift-check exit \([0-9][0-9]*\)$/\1/p' | head -1)
    case $ran in
      0 | 1) ;;
      *) echo "drift-check failed: the drift run $exe's drift-report reads drift-check exit ${ran:-(none)}, so it did not check $key"; return 1 ;;
    esac
    evidence="drift-check exit $ran"
  fi
  if printf '%s\n' "$found" | grep -qxF -- "$key"; then
    echo "still found: $key is among the drift run $exe's findings"
    return 1
  fi
  echo "$key is no longer found by the drift run $exe: listed under 'No longer found', absent from its findings, $evidence"
}
ok=
bad=
while IFS= read -r key; do
  if carries "$key"; then
    ok="$ok
carried: $key is listed by the fix item $fix"
    continue
  fi
  if why=$(resolved "$key"); then
    ok="$ok
$why"
  else
    bad="$bad
$key: $why; not carried: $fixwhy"
  fi
done <<EOF_KEYS
$blocking
EOF_KEYS
if [ -n "$bad" ]; then
  printf '%s\n' "$bad" | sed '/^$/d'
  exit 1
fi
printf '%s\n' "$ok" | sed '/^$/d'
```
