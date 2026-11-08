---
name: satelle-drift-claims-review
type: skill
description: Judge of the drift-claims gate on a drift review run (runbook in_progress → done), relevant on every release. A cold, read-only judge reads .satelle/constitution.md and every .satelle/principles/*.md and checks each statement of a binary mechanism against the released cmd/ and internal/ sources, rejecting with one CLAIM line per contradicted statement the run's drift-report does not yet carry, and re-checks every open prose drift story the report lists, stating one RESOLVED line for each whose finding no longer holds. Judges, never enacts.
---

# Drift review: the principles' claims against the released binary

You judge whether the standing principles still describe the binary that was
just released. The work tree is the released head. The diff in your prompt is
only `.version` (it makes this gate run on every release) and is not your
input. The record folder in your prompt holds the run's `drift-report`.

## What to read

- `.satelle/constitution.md` and every file in `.satelle/principles/`, in full.
- `cmd/` and `internal/` in the work tree: Grep and Read for the code that
  implements each statement you check.
- The run's `drift-report` (its path is in the record section): the findings
  already raised, every open drift story with its drift-key, and the
  `RESOLVED` lines already carried under `## Resolved by the judges`.

## What to check

Every statement of a mechanism of the binary: where state is stored (a table,
a file, a chain), which command, event or flag does a thing, what the binary
refuses. For example, the architecture principle's "queue entries live in the
story chain" against the queue file in `.satelle/queues/` and the queue code in
`internal/kernel/queue.go`.

Skip statements of intent, rulings about process, and anything the binary does
not implement.

## Re-check the open prose findings

Every open drift story listed under `## Open drift stories` whose drift-key
has kind `prose` (`prose:<file>:<the statement>`) records one finding.
Re-check each against the released tree: is the statement still in the file,
and does the source still contradict it? When the finding no longer holds (the
statement was removed or corrected, or the source now does what it says),
state one line for it:

`RESOLVED <drift-key> — <evidence>`

`<drift-key>` is the story's key verbatim and `<evidence>` names the file and
line that show it fixed. A finding that still holds gets no line. Never state
`RESOLVED` for a finding of another kind.

## Verdict

- **Accept** when no statement is contradicted by the source, or when every
  contradiction is already carried in `drift-report`: a story id listed for
  it, or an open drift story whose drift-key names the same file and
  statement; and every `RESOLVED` line you would state is already under
  `## Resolved by the judges`.
- **Reject** otherwise. The notes hold exactly one line per contradiction not
  yet carried:

  `CLAIM <file>:<line> "<the statement>" — <source file:line that contradicts it>`

  Quote the whole sentence verbatim from that line, so the finding's drift-key
  is the same on every release.

  And one `RESOLVED <drift-key> — <evidence>` line per resolved finding not
  yet under `## Resolved by the judges`. A reject that carries only RESOLVED
  lines is how the run learns to close those stories (drift-review.md step 3
  reads them from this gate's run).

End with one JSON object:

```json
{"verdict": "reject", "notes": "CLAIM .satelle/principles/p.md:6 \"queue entries live in the story chain\" — internal/kernel/queue.go:62"}
```
