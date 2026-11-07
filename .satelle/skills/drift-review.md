---
name: drift-review
type: skill
description: The procedure a run of the drift review task follows after every release (release.md step 11) — check the substrate against the released binary (scripts/drift-check.sh), raise a story per finding (scripts/drift-stories.sh), attach drift-report, then present done, where the drift-constitution gate judges the release's binary change against the constitution and the drift-claims gate judges the principles' statements of mechanism against the released source; each DRIFT or CLAIM line becomes a story too. Fixes nothing inline.
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
(`scripts/drift-stories.sh`), and the stories are the fix. A gate that rejects
is answered by raising stories, never by changing what it judged. An empty
report is a pass.

Shell variables do not survive between the session's separate commands, so
each block finds the in-progress run and the run-local directory
`rd="${TMPDIR:-/tmp}/satelle-drift-$exe"` again.

## 0. Find the run, seat it on its base

```sh
task=tas_DRIFT_REVIEW
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id // empty')
[ -n "$exe" ] || { echo "no in-progress run of $task: release.md step 11 starts it" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"; mkdir -p "$rd"
base=$(satelle ledger list "$exe" --kind task.run --json | jq -r '.[0].payload.base_ref // empty')
ver=$(sed -n 's/^satelle\.version: *//p' .version)
[ -n "$base" ] && [ -n "$ver" ] || { echo "run $exe has no base_ref, or .version names no satelle.version" >&2; exit 1; }
satelle execution seat "$exe" --base "$base"
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
task=tas_DRIFT_REVIEW
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
bash scripts/drift-check.sh > "$rd/substrate.txt"; echo "drift-check exit $?" | tee "$rd/substrate.exit"
cat "$rd/substrate.txt"
```

Exit 1 means misses (one line each); exit 2 means it could not check: stop and
report its reason.

## 2. Raise a story per finding

```sh
task=tas_DRIFT_REVIEW
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

```sh
task=tas_DRIFT_REVIEW
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
rd="${TMPDIR:-/tmp}/satelle-drift-$exe"
ver=$(sed -n 's/^satelle\.version: *//p' .version)
base=$(satelle ledger list "$exe" --kind task.run --json | jq -r '.[0].payload.base_ref')
{
  echo "# Drift review of v$ver"
  echo
  if [ ! -s "$rd/substrate.txt" ] && [ ! -s "$rd/judged.txt" ]; then echo "no drift"; echo; fi
  echo "## Checks"
  echo "- satelle $(satelle version 2>/dev/null | head -1)"
  echo "- scripts/drift-check.sh: $(cat "$rd/substrate.exit")"
  echo "- drift-constitution: $base..v$ver over cmd/, internal/, go.mod, go.sum"
  echo "- drift-claims: .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/ at v$ver"
  echo
  echo "## Substrate findings"; cat "$rd/substrate.txt"; echo
  echo "## Constitution findings"; grep ': constitution: ' "$rd/judged.txt"; echo
  echo "## Claim findings"; grep ': prose: ' "$rd/judged.txt"; echo
  echo "## Open drift stories"
  satelle story list --tag drift --open --json | jq -r '.[] | "\(.id) \((.body // "") | split("\n") | map(select(startswith("drift-key: "))) | .[0] // "" | ltrimstr("drift-key: "))"'
  echo
  echo "## Stories"; cat "$rd/stories.txt"
} > "$rd/drift-report.md"
satelle execution attach "$exe" --name drift-report --type text/markdown --file "$rd/drift-report.md"
```

## 4. Present done; answer each reject with stories

```sh
task=tas_DRIFT_REVIEW
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r '.[0].id')
satelle execution set "$exe" --status done || satelle execution route "$exe"
```

The move runs `drift-constitution`, then `drift-claims`, then `validate-after`.
On a reject, read the notes in `satelle execution route "$exe"`:

- A `drift-constitution` reject: turn each
  `DRIFT <principle> <file>:<line> <symbol> — <what>` line into
  `<file>:<line>: constitution: <principle> <symbol> — <what>`.
- A `drift-claims` reject: turn each
  `CLAIM <file>:<line> "<quote>" — <evidence>` line into
  `<file>:<line>: prose: <quote> — <evidence>`.

Append those lines to `$rd/judged.txt`, run steps 2 and 3 again, and present
again. An after-check reject (`diff truncated at ... the judge did not read`)
has nothing to raise: present again once. If the same reject comes back, stop
and report it to the operator as a structural gap. Never present the run any
other way, and never change the code or substrate a gate judged.

## 5. Release the seat

```sh
task=tas_DRIFT_REVIEW
exe=$(satelle execution list --parent "$task" --status done --json | jq -r '.[0].id')
satelle execution release "$exe"
```
