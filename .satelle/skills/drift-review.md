---
name: drift-review
type: skill
description: The procedure a run of the drift review task follows after every release (release.md step 11) — check the substrate against the released binary (scripts/drift-check.sh), raise a story per finding (scripts/drift-stories.sh), attach drift-report with the open drift stories this run no longer finds and the judges' RESOLVED lines, present each of those already on the drift route done (its drift-resolved gate re-checks it) and list each closed and each still open with its reason, then present the run done, where the drift-constitution gate judges the release's binary change against the constitution, the drift-claims gate judges the principles' statements of mechanism against the released source, and the drift-closures gate checks the closed and stayed-open lists against the kernel; each DRIFT or CLAIM line becomes a story too. Fixes nothing inline.
---

# Drift review (after every release)

Drift goes two ways (operator, 2026-11-07). The substrate drifts from the
binary: `.satelle/` names commands, flags, topics, links or skills the released
binary no longer has, or states a mechanism the source does not implement. And
the binary drifts from the constitution, which matters more: a release puts
process in Go, adds a non-gRPC interface, couples to one agent, or leaves dead
code.

This run is the review. It runs from the main checkout at the released tag,
after the release run and the worktree cleanup (release.md steps 10 and 11).
Its base is the previous release's tag, so its diff is exactly what the release
changed.

**Never edit code or substrate in this run.** Every finding becomes a story
(`scripts/drift-stories.sh`), and the stories are the fix. The run closes the
drift stories whose finding it no longer finds (step 4); the fix itself was
other work. A gate that rejects
is answered by raising stories, never by changing what it judged. An empty
report is a pass.

Shell variables do not survive between the session's separate commands, so
each block finds the in-progress run and the run-local directory
`rd="${TMPDIR:-/tmp}/satelle-drift-$exe"` again.

## 0. Find the run, seat it on its base

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id // empty')
[ -n "$exe" ] || { echo "no in-progress run of $task: release.md step 11 starts it" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"; mkdir -p "$rd"
base=$(satelle ledger list "$exe" --kind task.run --json | jq -r '.[0].payload.base_ref // empty')
ver=$(sed -n 's/^satelle\.version: *//p' .version)
[ -n "$base" ] && [ -n "$ver" ] || { echo "run $exe has no base_ref, or .version names no satelle.version" >&2; exit 1; }
satelle execution seat "$exe" --base "$base"
satelle task get "$task" | satelle execution attach "$exe" --name task --type text/plain --file - || exit 1   # validate-after judges the run against it
d=$(satelle execution diff "$exe" --json)
if [ "$(printf '%s' "$d" | jq -r .base)" != "$(git rev-parse "$base^{commit}")" ] || [ "$(printf '%s' "$d" | jq '.paths | length')" -eq 0 ]; then
  echo "run $exe is not judged from $base, or its diff is empty:" >&2; printf '%s\n' "$d" >&2; exit 1
fi
echo "run $exe: v$ver judged from $base"
```

Every seat on the run, the first or a re-seat after a restart, carries
`--base "$base"`. A seat without `--base` records HEAD, the released tag, and
the latest seat's base is the one the gates judge from: the diff would be empty
and `drift-constitution` would be skipped before it judged anything.

## 1. Substrate against the binary

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
bash scripts/drift-check.sh > "$rd/substrate.txt"; echo "drift-check exit $?" | tee "$rd/substrate.exit"
cat "$rd/substrate.txt"
```

Exit 1 means misses (one line each); exit 2 means it could not check: stop and
report its reason.

## 2. Raise a story per finding

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
touch "$rd/judged.txt" "$rd/stories.txt"
cat "$rd/substrate.txt" "$rd/judged.txt" | bash scripts/drift-stories.sh "$ver" > "$rd/stories.txt.new" && mv "$rd/stories.txt.new" "$rd/stories.txt"
cat "$rd/stories.txt"
```

Each line is `<drift-key>\t<story id>\tnew|existing`. A finding an open drift
story already carries is not raised twice. Exit 2 stops the run and names what
is missing (for example an epic tag with no open epic).

## 3. Attach drift-report

The report carries what this run finds and what it no longer finds. `No
longer found` lists each open drift story whose drift-key is not among this
run's findings, the key first and its story id after a tab; `Resolved by the
judges` holds every `RESOLVED <drift-key> — <evidence>` line this run's
drift-constitution and drift-claims judges stated, read from their gate runs
on the run's ledger. These are the sections the drift route's done gate reads
(`.satelle/skills/satelle-drift-resolved-check.md`), so the report is attached
before step 4 presents any story.

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
base=$(satelle ledger list "$exe" --kind task.run --json | jq -r '.[0].payload.base_ref')
touch "$rd/judged.txt" "$rd/stories.txt"
satelle story list --tag drift --open --json > "$rd/open.json" || exit 1
jq -r '.[] | [.id, ((.body // "") | split("\n") | map(select(startswith("drift-key: "))) | .[0] // "" | ltrimstr("drift-key: "))] | @tsv' "$rd/open.json" > "$rd/open.tsv" || exit 1
cat "$rd/substrate.txt" "$rd/judged.txt" | bash scripts/drift-stories.sh --keys > "$rd/found.txt" || exit 1
awk -F'\t' -v found="$rd/found.txt" 'BEGIN { while ((getline l < found) > 0) f[l] = 1 } $2 != "" && !($2 in f) { print $2 "\t" $1 }' "$rd/open.tsv" > "$rd/gone.txt"
satelle ledger list "$exe" --kind gate.run --json > "$rd/gate-runs.json" || exit 1
jq -r '.[] | .payload | select(.gate == "drift-constitution" or .gate == "drift-claims") | .notes // ""' "$rd/gate-runs.json" | awk '/^RESOLVED / && !seen[$0]++' > "$rd/resolved.txt"
{
  echo "# Drift review of v$ver"
  echo
  if [ ! -s "$rd/substrate.txt" ] && [ ! -s "$rd/judged.txt" ]; then echo "no drift"; echo; fi
  echo "## Checks"
  echo "- satelle $(satelle version 2>/dev/null | head -1)"
  echo "- scripts/drift-check.sh: $(cat "$rd/substrate.exit")"
  echo "- drift-constitution: $base..v$ver over cmd/, internal/, go.mod, go.sum, .satelle/constitution.md, .satelle/principles/"
  echo "- drift-claims: .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/ at v$ver"
  echo
  echo "## Substrate findings"; cat "$rd/substrate.txt"; echo
  echo "## Constitution findings"; grep ': constitution: ' "$rd/judged.txt"; echo
  echo "## Claim findings"; grep ': prose: ' "$rd/judged.txt"; echo
  echo "## Open drift stories"; awk -F'\t' '{ print $1 " " $2 }' "$rd/open.tsv"; echo
  echo "## Stories"; cat "$rd/stories.txt"; echo
  echo "## No longer found"; cat "$rd/gone.txt"; echo
  echo "## Resolved by the judges"; cat "$rd/resolved.txt"
} > "$rd/drift-report.md"
satelle execution attach "$exe" --name drift-report --type text/markdown --file "$rd/drift-report.md"
```

## 4. Close what the run no longer finds

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
tab=$(printf '\t')
touch "$rd/closed.txt"
: > "$rd/refused.txt"
while IFS="$tab" read -r key id; do
  wf=$(jq -r --arg id "$id" '.[] | select(.id == $id) | .workflow' "$rd/open.json")
  [ "$wf" = drift ] || continue
  if out=$(satelle story set "$id" --status done 2>&1); then
    printf '%s\t%s\n' "$id" "$key" >> "$rd/closed.txt"
  else
    printf '%s\t%s\n' "$id" "$(printf '%s' "$out" | tr '\t\n' '  ')" >> "$rd/refused.txt"
  fi
done < "$rd/gone.txt"
satelle story list --tag drift --open --json > "$rd/still-open.json" || exit 1
jq -r '.[] | [.id, .workflow, ((.body // "") | split("\n") | map(select(startswith("drift-key: "))) | .[0] // "" | ltrimstr("drift-key: "))] | @tsv' "$rd/still-open.json" |
  awk -F'\t' -v found="$rd/found.txt" -v refused="$rd/refused.txt" '
    BEGIN {
      while ((getline l < found) > 0) f[l] = 1
      while ((getline l < refused) > 0) { i = index(l, "\t"); r[substr(l, 1, i - 1)] = substr(l, i + 1) }
    }
    {
      id = $1; wf = $2; key = $3
      if (key == "") { print id "\t-\tno drift-key line in its body, so no run can find it gone"; next }
      if (wf != "drift") { print id "\t" key "\ton the " wf " workflow, not the drift route: re-homing moves it onto drift before it can close"; next }
      if (key in f) { print id "\t" key "\tstill found by this run"; next }
      if (id in r) { print id "\t" key "\tthe drift route refused done: " r[id]; next }
      print id "\t" key "\tnot under No longer found: run step 3 again"
    }' > "$rd/stayed.txt"
{
  cat "$rd/drift-report.md"
  echo
  echo "## Closed"; awk '!seen[$0]++' "$rd/closed.txt"; echo
  echo "## Stayed open"; cat "$rd/stayed.txt"
} > "$rd/drift-report-closed.md"
satelle execution attach "$exe" --name drift-report --type text/markdown --file "$rd/drift-report-closed.md"
cat "$rd/drift-report-closed.md"
```

Step 4 presents `done` only the stories step 3 listed under `No longer found`
that are already on the drift workflow. A drift story still on chore,
delivery or any other workflow is never presented: its route's own gates
would judge a change that does not exist and record a refusal. It stays open
with that reason until re-homing moves it onto the drift route. The drift
route's done gate re-checks each presented story against step 3's report and
leaves a gate.run on its ledger, accept or reject.

The report attached after step 4 adds two sections the run's done move checks
(the `drift-closures` gate, `.satelle/skills/satelle-drift-closures-check.md`):

- `## Closed`: `<story id>\t<drift-key>`, every story this run closed.
- `## Stayed open`: `<story id>\t<drift-key or ->\t<reason>`, every open drift
  story, with why it stayed open (no drift-key line, not on the drift route,
  still found, or the drift route's refusal).

Steps 3 and 4 always run together, in that order: step 4 reads the files step
3 wrote.

## 5. Present done; answer each reject with stories

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
satelle execution set "$exe" --status done || satelle execution route "$exe"
```

The move runs `drift-constitution`, then `drift-claims`, then
`drift-closures`, then `validate-after`. On a reject, read the notes in
`satelle execution route "$exe"`:

- A `drift-constitution` reject: turn each
  `DRIFT <principle> <file>:<line> <symbol> — <what>` line into
  `<file>:<line>: constitution: <principle> <symbol> — <what>`.
- A `drift-claims` reject: turn each
  `CLAIM <file>:<line> "<quote>" — <evidence>` line into
  `<file>:<line>: prose: <quote> — <evidence>`.
- A `RESOLVED <drift-key> — <evidence>` line from either judge needs no
  copying: step 3 reads it from the judge's gate run on the run's ledger.
- A `drift-closures` reject: the attached report's `Closed` or `Stayed open`
  list does not match the stories' states; run steps 3 and 4 again.

Append the DRIFT and CLAIM lines to `$rd/judged.txt`, run steps 2, 3 and 4
again, and present again. An after-check reject (`diff truncated at ... the
judge did not read`) has nothing to raise: present again once. If the same
reject comes back, stop and report it to the operator as a structural gap.
Never present the run any other way, and never change the code or substrate a
gate judged.

## 6. Release the seat

```sh
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status done --json | jq -r '.[0].id')
satelle execution release "$exe"
```
