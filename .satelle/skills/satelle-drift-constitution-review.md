---
name: satelle-drift-constitution-review
type: skill
description: Judge of the drift-constitution gate on a drift review run (runbook in_progress → done). A cold, read-only judge reads the release's change (the run's diff from the previous release tag to the released head, over cmd/, internal/, go.mod, go.sum, .satelle/constitution.md and .satelle/principles/) against .satelle/constitution.md and every .satelle/principles/*.md, rejects with one DRIFT line per drift the run's drift-report does not yet carry (in its fix item), and re-checks every open constitution finding the report lists (other open drift items and the release's fix item), stating one RESOLVED line for each that no longer holds. Judges, never enacts.
---

# Drift review: the release's binary change against the constitution

You judge one release's change to the binary. The prompt carries the run
(a drift review run), its base commit (the previous release's tag), the
changed paths and the diff over the binary's sources (`cmd/`, `internal/`,
`go.mod`, `go.sum`) and the constitution and principles
(`.satelle/constitution.md`, `.satelle/principles/`) up to the released head,
which is the work tree you read. A release that edits only the constitution or
a principle reaches you too: that is how a constitution finding gets fixed.
The record folder in your prompt holds the run's `drift-report`.

## What to read

- `.satelle/constitution.md` and every file in `.satelle/principles/`. Read
  them yourself, in full, before judging.
- Any file at a path in the diff, in the work tree, when a hunk needs its
  context.
- The run's `drift-report` (its path is in the record section): the findings
  already raised, the release's fix item and its keys under `## Fix item`,
  every other open drift item with its drift-key under `## Open drift items`,
  and the `RESOLVED` lines already carried under `## Resolved by the judges`.

## What is drift

Start from these, then check the change against every other principle:

1. Process or policy in Go: workflow, gate or release rules the binary decides
   instead of the substrate (`satelle-mechanism-vs-substrate`).
2. An interface that is not gRPC (`satelle-operator-rulings`).
3. Coupling to one agent or provider outside an adapter or config
   (`satelle-agent-agnostic`).
4. Dead or redundant code: code nothing calls, or a second owner of a fact
   (`satelle-operator-rulings`).

Judge only what this release changed. Drift that predates the base is not
yours (the drift-claims gate reads the standing principles).

## Re-check the open constitution findings

Every key of kind `constitution` (`constitution:<file>:<principle> <symbol>`)
listed under `## Open drift items` or `## Fix item` records one finding. Re-check each against the released tree: read the file and the
symbol, and the principle as it now reads. When the finding no longer holds
(the code is gone or changed, or the principle was amended so it no longer
contradicts it), state one line for it:

`RESOLVED <drift-key> — <evidence>`

`<drift-key>` is the key verbatim and `<evidence>` names the file and
line that show it fixed. A finding that still holds gets no line. Never state
`RESOLVED` for a finding of another kind.

## A truncated diff

When the diff ends with `… diff truncated …`, the cut hid part of the change.
The `Changed paths:` list is never cut. Read, in the work tree, every changed
path under `cmd/`, `internal/`, `go.mod`, `go.sum`, `.satelle/constitution.md`
and `.satelle/principles/` whose hunk the cut hid or split, judge each one's
full content, and put one line `READ <path>` per such file in your notes. An
accept without those lines is turned into a reject.

## Verdict

- **Accept** when the change contradicts no principle, or when every drift is
  already carried in `drift-report`: its key is listed under `## Fix item` or
  `## Open drift items` (a key naming the same file and symbol); and every
  `RESOLVED` line you would state is already under `## Resolved by the judges`.
  An accept's notes are always exactly this one line, never empty:

  `checked <n> changed paths against .satelle/constitution.md and its principles; carried in attachments/drift-report: <keys>`

  with `<n>` the number of changed paths you judged and `<keys>` the
  drift-report keys that already carry this change's drift, comma-separated,
  or `none`. That line gives the gate's notes and reference after checks
  (`satelle-verdict-notes-check`, `satelle-verdict-reference-check`) what they
  need, so an accept of an all-carried change stands.
- **Reject** otherwise. The notes hold exactly one line per drift not yet
  carried, and nothing else on lines starting `DRIFT`:

  `DRIFT <principle> <file>:<line> <symbol> — <what>`

  `<principle>` is the principle's file name without `.md` (or
  `constitution`), `<file>:<line>` the place in the released tree, `<symbol>`
  the function, type or flag, and `<what>` one sentence on the contradiction.

  And one `RESOLVED <drift-key> — <evidence>` line per resolved finding not
  yet under `## Resolved by the judges`. A reject that carries only RESOLVED
  lines is how the run learns to stop carrying those findings
  (drift-review.md steps 2 and 3 read them from this gate's run).

End with one JSON object. A reject:

```json
{"verdict": "reject", "notes": "DRIFT satelle-operator-rulings internal/x/http.go:40 Serve — adds a REST listener beside gRPC"}
```

An accept whose drift is all carried:

```json
{"verdict": "accept", "notes": "checked 27 changed paths against .satelle/constitution.md and its principles; carried in attachments/drift-report: satelle-agent-agnostic:internal/cli/cmd_hook.go:hookDefers"}
```

An accept with no drift:

```json
{"verdict": "accept", "notes": "checked 3 changed paths against .satelle/constitution.md and its principles; carried in attachments/drift-report: none"}
```
