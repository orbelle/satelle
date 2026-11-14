---
name: satelle-drift-claims-review
type: skill
description: Judge of the drift-claims gate on a drift review run (runbook in_progress → done), relevant when the run's range changed .version (every binary-changed release), .satelle/constitution.md or a principle. A cold, read-only judge reads .satelle/constitution.md and every .satelle/principles/*.md and checks each statement of a binary mechanism against the cmd/ and internal/ sources at the run's head, listing one WARN line per contradicted statement. Its findings are warnings, recorded in the release's drift-fix item, and never block — its verdict is always accept. Judges, never enacts.
---

# Drift review: the principles' claims against the released binary

You judge whether the standing principles still describe the binary's source
at the run's head. The work tree is that head: the released tag after a
binary-changed release, main after a binary-unchanged one. The diff in your prompt is the range's change to `.version`, `.satelle/constitution.md` and `.satelle/principles/` (the gate's `paths`; the gate runs when any of them changed) and is not your input: read the files in full. The record folder in your prompt holds the run's `drift-report`.

What you find is a warning: the drift review records each of your WARN lines
in the release's drift-fix item (`.satelle/skills/drift-review.md` step 6),
where it never holds anything open. You never block the run.

## What to read

- `.satelle/constitution.md` and every file in `.satelle/principles/`, in full.
- `cmd/` and `internal/` in the work tree: Grep and Read for the code that
  implements each statement you check.
- The run's `drift-report` (its path is in the record section), for context:
  the findings already raised and the fix item.

## What to check

Every statement of a mechanism of the binary: where state is stored (a table,
a file, a chain), which command, event or flag does a thing, what the binary
refuses. For example, the architecture principle's "queue entries live in the
story chain" against the queue file in `.satelle/queues/` and the queue code in
`internal/kernel/queue.go`.

Skip statements of intent, rulings about process, and anything the binary does
not implement.

Every contradicted statement you find gets one line, whether or not an earlier
release already listed it:

`WARN <file>:<line> "<the statement>" — <source file:line that contradicts it>`

Quote the whole sentence verbatim from that line, so the warning's key is the
same on every run.

## Verdict

The verdict is always **accept**, with zero or many warnings. The notes' first
line is always

`checked .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/: <n> contradicted statements`

with `<n>` the number of WARN lines, and the WARN lines follow, one per line.
That first line gives the gate's notes and reference after checks what they
need, so an accept with no warning stands.

End with one JSON object. With no warning:

```json
{"verdict": "accept", "notes": "checked .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/: 0 contradicted statements"}
```

With one warning:

```json
{"verdict": "accept", "notes": "checked .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/: 1 contradicted statements\nWARN .satelle/principles/p.md:6 \"queue entries live in the story chain\" — internal/kernel/queue.go:62"}
```
