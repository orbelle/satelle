---
name: satelle-drift-accept-quoted
type: check
description: After check of runbook's validate-after (in_progress → done), drift review runs only. An accept stands only when the judge's notes name attachments/drift-report and quote three of its lines - the "- satelle" version line and the drift-check exit line under "## Checks", and the fix item's id (or the line "no drift" when "## Fix item" is empty); each one missing is named with the report's own text. Every other run, and every reject, passes with no output. Deterministic, no agent.
---

# Drift accept quoted (a drift run's close accept quotes its report)

validate-after's judge (`.satelle/skills/satelle-task-validate-after-review.md`)
closes a drift review run on the run's `drift-report`. Its skill tells it, for
a drift run, to name `attachments/drift-report` and copy three lines from it.
This check holds an accept to that (sto_09b81217): the required text comes
from the report, never from the notes, so a quote of another release's
version, or of an exit the report does not hold, is refused as a missing one.

It runs third in validate-after's `after`, behind the notes and reference
rules, so it sees only an accept those let through. It judges only a run of
the drift review task: the `task=` line is the trigger's
(`satelle-drift-run-trigger.md`), read against the run's `parent` the same way.
Every other run, and a reject, passes with no output, so their gate notes are
unchanged.

The lines it requires restate drift-review.md step 3's report, a `## <name>`
heading followed by its lines up to the next `## ` heading:

- under `## Checks`, the first line starting `- satelle ` (the version) and
  the first starting `- scripts/drift-check.sh: ` (the drift-check exit), each
  quoted without its leading `- `;
- under `## Fix item`, the fix item's id, the first field of its first line;
  when that section is empty, the report's line `no drift`.

`tests/drift_gate_unix_test.go` fails the suite when the task id or any of
those line forms no longer matches its owner.

```sh
set -u
task=tas_a45ba066
version_prefix='- satelle '
exit_prefix='- scripts/drift-check.sh: '
fix_heading='## Fix item'
no_drift='no drift'
[ "${SATELLE_GATE_RESULT:-}" = accept ] || exit 0
p=$(cat)
for tool in satelle jq awk grep; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed"; exit 1; }
done
exe=$SATELLE_ITEM
if ! run=$(satelle execution get "$exe" --json 2>&1); then
  echo "cannot read the run $exe: $run"
  exit 1
fi
parent=$(printf '%s' "$run" | jq -r '.parent // empty')
[ "$parent" = "$task" ] || exit 0
if ! printf '%s' "$run" | jq -e 'any(.attachments[]?; .name == "drift-report")' >/dev/null; then
  echo "no drift-report: the drift run $exe has attached none (drift-review.md steps 3 and 4)"
  exit 1
fi
if ! report=$(satelle execution attachment "$exe" drift-report 2>&1); then
  echo "cannot read the drift run $exe's drift-report: $report"
  exit 1
fi
# section <heading>: the report's lines under the heading, up to the next "## ".
section() {
  printf '%s\n' "$report" | awk -v h="$1" '$0 == h { on = 1; next } /^## / { on = 0 } on && NF'
}
# checked <prefix>: the first "## Checks" line starting with prefix, without
# its leading "- " and trailing whitespace.
checked() {
  section '## Checks' | awk -v p="$1" 'index($0, p) == 1 { sub(/^- /, ""); sub(/[ \t\r]+$/, ""); print; exit }'
}
version=$(checked "$version_prefix")
exitline=$(checked "$exit_prefix")
fixid=$(section "$fix_heading" | awk '{ print $1; exit }')
fix=
fixwhat=
if [ -n "$fixid" ]; then
  fix=$fixid
  fixwhat="fix item: $fixid"
elif printf '%s\n' "$report" | grep -qxF -e "$no_drift"; then
  fix=$no_drift
  fixwhat="line: $no_drift"
fi
lacks=0
[ -n "$version" ] || { echo "attachments/drift-report has no version line: run drift-review.md steps 3 and 4 again"; lacks=1; }
[ -n "$exitline" ] || { echo "attachments/drift-report has no drift-check exit line: run drift-review.md steps 3 and 4 again"; lacks=1; }
[ -n "$fix" ] || { echo "attachments/drift-report has no fix item and no '$no_drift' line: run drift-review.md steps 3 and 4 again"; lacks=1; }
[ "$lacks" -eq 0 ] || exit 1
n=$(printf '%s' "$p" | jq '.members // [] | length') || { echo "the payload is not JSON"; exit 1; }
# has <notes> <text>: the notes hold text, as a fixed string.
has() { printf '%s\n' "$1" | grep -qF -e "$2"; }
bad=0
i=0
while [ "$i" -lt "$n" ]; do
  result=$(printf '%s' "$p" | jq -r ".members[$i].result")
  if [ "$result" = accept ]; then
    binding=$(printf '%s' "$p" | jq -r ".members[$i].binding")
    notes=$(printf '%s' "$p" | jq -r ".members[$i].notes // \"\"")
    has "$notes" attachments/drift-report || { echo "$binding accepted without naming attachments/drift-report"; bad=1; }
    has "$notes" "$version" || { echo "$binding accepted without quoting attachments/drift-report's version line: $version"; bad=1; }
    has "$notes" "$exitline" || { echo "$binding accepted without quoting attachments/drift-report's drift-check exit line: $exitline"; bad=1; }
    has "$notes" "$fix" || { echo "$binding accepted without quoting attachments/drift-report's $fixwhat"; bad=1; }
  fi
  i=$((i + 1))
done
[ "$bad" -eq 0 ] || exit 1
echo "the accept quotes attachments/drift-report: $version; $exitline; $fix"
```
