---
name: drift-review
type: skill
description: The procedure a run of the drift review task follows after every release (release.md step 11) — check the substrate against the released binary (scripts/drift-check.sh, the rules of satelle validate), raise the release's one drift-fix item listing every finding (scripts/drift-fix.sh), attach drift-report with the fix item, the other open drift items and the keys this run no longer finds, close every older drift item (each carried into the fix item or resolved on the report) and list each closed and each still open with its reason, then present the run done, where the drift-constitution gate judges the release's binary change against the constitution, the drift-claims gate lists the principles' statements of mechanism the released source does not implement as warnings (it never blocks), and the drift-closures gate checks the closed and stayed-open lists against the kernel; each DRIFT line becomes a finding of the fix item, each WARN line a warning in it. Fixes nothing inline.
---

# Drift review (after every release)

Drift goes two ways (operator, 2026-11-07). The substrate drifts from the
binary: `.satelle/` names commands, flags, topics, links or skills the released
binary no longer has, or states a mechanism the source does not implement. And
the binary drifts from the constitution, which matters more: a release puts
process in Go, adds a non-gRPC interface, couples to one agent, or leaves dead
code. The release itself refuses a candidate whose substrate names what its
binary lacks (release.md, `### Drift gate on the candidate`), so a substrate
finding here is rare.

This run is the review. It runs from the main checkout at the released tag,
after the release run and the worktree cleanup (release.md steps 10 and 11).
Its base is the previous release's tag, so its diff is exactly what the release
changed.

**Never edit code or substrate in this run.** Drift is fixed like an upgrade:
every finding goes into the release's one drift-fix item
(`scripts/drift-fix.sh`), tagged drift, drift-fix and `release:v<ver>`, and
that item is the fix. The run closes every older drift item, each carried into
the fix item or no longer found (step 4); the fix itself is other work. The
drift-claims judge's findings are warnings in the fix item and never block. A
gate that rejects is answered by recording its lines in the fix item, never by
changing what it judged. An empty report is a pass.

Shell variables do not survive between the session's separate commands, so
each block finds the in-progress run and the run-local directory
`rd="${TMPDIR:-/tmp}/satelle-drift-$exe"` again.

## 0. Find the run, seat it on its base

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r '.[0].id // empty')
[ -n "$exe" ] || { echo "no in-progress run of $task: release.md step 11 starts it" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"; mkdir -p "$rd"
base=$(satelle ledger list "$exe" --kind task.run --json --session "$session" | jq -r '.[0].payload.base_ref // empty')
ver=$(sed -n 's/^satelle\.version: *//p' .version)
[ -n "$base" ] && [ -n "$ver" ] || { echo "run $exe has no base_ref, or .version names no satelle.version" >&2; exit 1; }
satelle execution seat "$exe" --base "$base" --session "$session"
satelle task get "$task" --session "$session" | satelle execution attach "$exe" --name task --type text/plain --file - --session "$session" || exit 1   # validate-after judges the run against it
d=$(satelle execution diff "$exe" --json --session "$session")
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
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
bash scripts/drift-check.sh > "$rd/substrate.txt"; echo "drift-check exit $?" | tee "$rd/substrate.exit"
cat "$rd/substrate.txt"
```

drift-check prints `satelle validate --session <session>`'s substrate findings and configuration
problems, one line each. Exit 1 means misses; exit 2 means it could not check:
stop and report its reason.

## 2. Raise the release's fix item

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
touch "$rd/judged.txt"
satelle ledger list "$exe" --kind gate.run --json --session "$session" > "$rd/gate-runs.json" || exit 1
jq -r '.[] | .payload | select(.gate == "drift-constitution" or .gate == "drift-claims") | .notes // ""' "$rd/gate-runs.json" | awk '/^RESOLVED / && !seen[$0]++' > "$rd/resolved.txt"
bash scripts/drift-fix.sh --open-findings > "$rd/open-findings.tsv" || exit 1
awk -F'\t' -v res="$rd/resolved.txt" '
  BEGIN { while ((getline l < res) > 0) { sub(/^RESOLVED /, "", l); i = index(l, " — "); if (i) l = substr(l, 1, i - 1); r[l] = 1 } }
  {
    id = $1; line = $2
    i = index(line, ":"); file = substr(line, 1, i - 1); rest = substr(line, i + 1)
    i = index(rest, ": "); rest = substr(rest, i + 2)
    i = index(rest, ": "); kind = substr(rest, 1, i - 1); rest = substr(rest, i + 2)
    i = index(rest, " — "); subj = i ? substr(rest, 1, i - 1) : rest
    key = kind ":" file ":" subj
    if ((kind == "constitution" && !(key in r) || kind == "prose" && file == id) && !seen[key]++) print line
  }' "$rd/open-findings.tsv" > "$rd/carried.txt"
cat "$rd/substrate.txt" "$rd/judged.txt" "$rd/carried.txt" > "$rd/findings.txt"
cat "$rd/findings.txt" $([ -f "$rd/warnings.txt" ] && echo "$rd/warnings.txt") | bash scripts/drift-fix.sh "$ver" > "$rd/fix.txt" || exit 1
cat "$rd/fix.txt"
```

`findings.txt` is every finding this run makes or carries: drift-check's
lines, the drift-constitution judge's DRIFT lines as constitution findings
(`judged.txt`, step 5), and every open drift item's constitution finding that
no judge of this run stated `RESOLVED` (`carried.txt`): a constitution finding
stays open until a judge re-checks it. An older drift item with no key line at
all is carried as a warning that names it, so it can close. A substrate finding needs no carrying:
drift-check finds it again while it holds. `fix.txt` is one line,
`<id>\tnew|updated\t<n> findings\t<m> warnings`, or empty when there is
nothing to record (no fix item for this release). A rerun rewrites the same
item. Exit 2 stops the run and names what failed.

## 3. Attach drift-report

The report carries what this run finds, the fix item, every other open drift
item's keys, and the keys it no longer finds. `No longer found` lists each
blocking key (any kind but prose) of an older open drift item that this run
neither finds nor carries into the fix item, the key first and its item id
after a tab; `Resolved by the judges` holds every
`RESOLVED <drift-key> — <evidence>` line this run's drift-constitution judge
stated, read from its gate runs on the run's ledger. These are the sections the
drift route's done gate reads (`.satelle/skills/satelle-drift-resolved-check.md`),
so the report is attached before step 4 presents any item.

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
base=$(satelle ledger list "$exe" --kind task.run --json --session "$session" | jq -r '.[0].payload.base_ref')
touch "$rd/judged.txt" "$rd/findings.txt" "$rd/fix.txt"
fix=$(cut -f1 "$rd/fix.txt" | head -1); [ -n "$fix" ] || fix=-
satelle story list --tag drift --open --json --session "$session" > "$rd/open.json" || exit 1
jq -r --arg fix "$fix" '.[] | select(.id != $fix) | .id as $id | (.body // "") | split("\n")[] | select(startswith("drift-key: ")) | [$id, ltrimstr("drift-key: ")] | @tsv' "$rd/open.json" > "$rd/open.tsv" || exit 1
bash scripts/drift-fix.sh --keys < "$rd/findings.txt" > "$rd/found.txt" || exit 1
awk -F'\t' -v found="$rd/found.txt" 'BEGIN { while ((getline l < found) > 0) f[l] = 1 } $2 !~ /^prose:/ && !($2 in f) { print $2 "\t" $1 }' "$rd/open.tsv" > "$rd/gone.txt"
satelle ledger list "$exe" --kind gate.run --json --session "$session" > "$rd/gate-runs.json" || exit 1
jq -r '.[] | .payload | select(.gate == "drift-constitution" or .gate == "drift-claims") | .notes // ""' "$rd/gate-runs.json" | awk '/^RESOLVED / && !seen[$0]++' > "$rd/resolved.txt"
{
  echo "# Drift review of v$ver"
  echo
  if [ ! -s "$rd/substrate.txt" ] && [ ! -s "$rd/judged.txt" ]; then echo "no drift"; echo; fi
  echo "## Checks"
  echo "- satelle $(satelle version --session "$session" 2>/dev/null | head -1)"
  echo "- scripts/drift-check.sh: $(cat "$rd/substrate.exit")"
  echo "- drift-constitution: $base..v$ver over cmd/, internal/, go.mod, go.sum, .satelle/constitution.md, .satelle/principles/"
  echo "- drift-claims: .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/ at v$ver"
  echo
  echo "## Substrate findings"; cat "$rd/substrate.txt"; echo
  echo "## Constitution findings"; grep ': constitution: ' "$rd/judged.txt"; echo
  echo "## Claim warnings"; [ -f "$rd/warnings.txt" ] && cat "$rd/warnings.txt"; echo
  echo "## Fix item"; [ "$fix" != - ] && { echo "$fix"; cat "$rd/found.txt"; }; echo
  echo "## Open drift items"; awk -F'\t' '{ print $1 " " $2 }' "$rd/open.tsv"; echo
  echo "## No longer found"; cat "$rd/gone.txt"; echo
  echo "## Resolved by the judges"; cat "$rd/resolved.txt"
} > "$rd/drift-report.md"
satelle execution attach "$exe" --name drift-report --type text/markdown --file "$rd/drift-report.md" --session "$session"
```

## 4. Close every older drift item

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
tab=$(printf '\t')
fix=$(cut -f1 "$rd/fix.txt" | head -1); [ -n "$fix" ] || fix=-
bash scripts/drift-fix.sh --close "$fix" > "$rd/close.txt"
[ $? -le 1 ] || exit 1
key() { jq -r --arg id "$1" '.[] | select(.id == $id) | (.body // "") | split("\n") | map(select(startswith("drift-key: ") or startswith("drift-warning: "))) | .[0] // "-" | sub("^drift-(key|warning): "; "")' "$rd/open.json"; }
: > "$rd/closed.txt"; : > "$rd/carried-out.txt"; : > "$rd/refused.txt"
while IFS="$tab" read -r id what why; do
  case $what in
    closed) printf '%s\t%s\n' "$id" "$(key "$id")" >> "$rd/closed.txt" ;;
    carried) printf '%s\t%s\tcarried: %s\n' "$id" "$(key "$id")" "$why" >> "$rd/carried-out.txt" ;;
    refused) printf '%s\t%s\n' "$id" "$why" >> "$rd/refused.txt" ;;
  esac
done < "$rd/close.txt"
satelle story list --tag drift --open --json --session "$session" > "$rd/still-open.json" || exit 1
jq -r '.[] | [.id, ((.body // "") | split("\n") | map(select(startswith("drift-key: ") or startswith("drift-warning: "))) | .[0] // "-" | sub("^drift-(key|warning): "; ""))] | @tsv' "$rd/still-open.json" |
  awk -F'\t' -v fix="$fix" -v ver="$ver" -v refused="$rd/refused.txt" '
    BEGIN { while ((getline l < refused) > 0) { i = index(l, "\t"); r[substr(l, 1, i - 1)] = substr(l, i + 1) } }
    {
      if ($1 == fix) { print $1 "\t" $2 "\tthe fix item of v" ver; next }
      if ($1 in r) { print $1 "\t" $2 "\tthe drift route refused done: " r[$1]; next }
      print $1 "\t" $2 "\tnot presented: run step 4 again"
    }' > "$rd/stayed.txt"
{
  cat "$rd/drift-report.md"
  echo
  echo "## Closed"; cat "$rd/closed.txt"; echo
  echo "## Carried"; cat "$rd/carried-out.txt"; echo
  echo "## Stayed open"; cat "$rd/stayed.txt"
} > "$rd/drift-report-closed.md"
satelle execution attach "$exe" --name drift-report --type text/markdown --file "$rd/drift-report-closed.md" --session "$session"
cat "$rd/drift-report-closed.md"
```

`drift-fix.sh --close` presents done every open drift item but the fix item:
an item not on the drift workflow is first reselected onto it, and one its
workflow has moved or judged keeps that route, its drift tag swapped for
`drift-carried-by:<fix id>`. Each presented item is tagged
`drift-carried-by:<fix id>`, and the drift route's done gate re-checks each
of its keys: carried when the fix item lists it, resolved when step 3's
report lists it under `No longer found` and the re-check holds. It leaves a
gate.run on the item's ledger, accept or reject. No item is cancelled.

The report attached after step 4 adds the sections the run's done move checks
(the `drift-closures` gate, `.satelle/skills/satelle-drift-closures-check.md`):

- `## Closed`: `<item id>\t<drift-key>`, every item this run closed.
- `## Carried`: `<item id>\t<drift-key>\tcarried: on <workflow>/<state>`,
  every item that left the drift set on its own route, carried by the fix
  item.
- `## Stayed open`: `<item id>\t<drift-key or ->\t<reason>`, every open
  drift item, with why it stayed open: the fix item of this release, or the
  drift route's refusal.

Steps 3 and 4 always run together, in that order: step 4 reads the files step
3 wrote.

## 5. Present done

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r '.[0].id')
satelle execution set "$exe" --status done --session "$session" || satelle execution route "$exe" --session "$session"
```

The move runs `drift-constitution`, then `drift-claims`, then
`drift-closures`, then `validate-after`. On a reject, read the notes in
`satelle execution route "$exe" --session <session>`:

- A `drift-constitution` reject: turn each
  `DRIFT <principle> <file>:<line> <symbol> — <what>` line into
  `<file>:<line>: constitution: <principle> <symbol> — <what>` and append it
  to `$rd/judged.txt`. A `RESOLVED <drift-key> — <evidence>` line needs no
  copying: steps 2 and 3 read it from the judge's gate run on the run's
  ledger. Run steps 2, 3 and 4 again, and present again.
- `drift-claims` accepts by its skill, whatever it finds: its WARN lines are
  recorded in step 6. If its judge rejects anyway, it has departed from its
  skill: turn each `WARN <file>:<line> "<statement>" — <evidence>` line of the
  reject into `<file>:<line>: prose: <statement> — <evidence>` in
  `$rd/warnings.txt` and present again. A second reject stops the run: raise a
  story against `.satelle/skills/satelle-drift-claims-review.md`.
- A `drift-closures` reject: the attached report's `Closed` or `Stayed open`
  list does not match the items' states; run steps 3 and 4 again.

An after-check reject (`diff truncated at ... the judge did not read`) has
nothing to record: present again once. If the same reject comes back, stop and
report it to the operator as a structural gap. Never present the run any
other way, and never change the code or substrate a gate judged.

## 6. Record the warnings

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status done --json --session "$session" | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
touch "$rd/findings.txt" "$rd/warnings.txt"
satelle ledger list "$exe" --kind gate.run --json --session "$session" > "$rd/gate-runs.json" || exit 1
jq -r '[.[] | .payload | select(.gate == "drift-claims" and .result == "accept")] | last | .notes // ""' "$rd/gate-runs.json" |
  sed -n 's/^WARN \([^ ]*\) "\(.*\)" — \(.*\)$/\1: prose: \2 — \3/p' >> "$rd/warnings.txt"
awk '!seen[$0]++' "$rd/warnings.txt" > "$rd/warnings.new" && mv "$rd/warnings.new" "$rd/warnings.txt"
cat "$rd/findings.txt" "$rd/warnings.txt" | bash scripts/drift-fix.sh "$ver" | tee "$rd/fix.txt"
```

Each `WARN <file>:<line> "<statement>" — <evidence>` line of the accepting
drift-claims run becomes `<file>:<line>: prose: <statement> — <evidence>`, a
`drift-warning:` line under the fix item's `## Warnings`, never a
`drift-key:` line: a warning never holds the fix item open. With no fix item
yet and at least one warning, this raises the release's fix item with only
warnings.

## 7. Release the seat

```sh
session=<session>   # this session's id, from the context line
task=tas_a45ba066
exe=$(satelle execution list --parent "$task" --status done --json --session "$session" | jq -r '.[0].id')
satelle execution release "$exe" --session "$session"
```
