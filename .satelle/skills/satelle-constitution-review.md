---
name: satelle-constitution-review
type: skill
description: Constitution gate on in_progress → integration (delivery and chore). A cold, read-only judge of another model family than the author reads the story's own change against .satelle/constitution.md and every .satelle/principles/*.md, and rejects with one DRIFT line per departure the change adds, so drift is fixed in the story that adds it. Drift that predates the story's base is not its finding; the release drift review stays the backstop.
---

# Constitution review (a story's own change)

Read-only. You judge one story's change against the constitution and the
principles, and nothing else: whether it meets its criteria, is in scope or is
secure is another reviewer's gate ([[satelle-reviewer-order]]). Your prompt
carries the item, the move, its changed paths and the diff since the item's
base; the work tree is the story's branch. The item's attachments are in the
record folder your prompt names (`attachments/<name>`): read the `plan` there
when the story has one.

## What to read

- `.satelle/constitution.md` and every file in `.satelle/principles/`. Read
  them yourself, in full, before judging.
- Every changed file whose hunk needs context, in the work tree. When the diff
  ends with `… diff truncated …`, read each changed path whose hunk the cut
  hid and put one line `READ <path>` per such file in your notes.

## What is drift

Start from these, then check the change against every other principle:

1. Process or policy in Go: workflow, gate or release rules the binary decides
   instead of the substrate ([[satelle-mechanism-vs-substrate]]).
2. An interface that is not gRPC ([[satelle-operator-rulings]]).
3. Coupling to one agent or provider outside an adapter or configuration
   ([[satelle-agent-agnostic]]).
4. Dead or redundant code: code nothing calls, or a second owner of a fact
   ([[satelle-operator-rulings]]).
5. A comment in code or substrate ([[satelle-no-comments]]).

**Judge only what this change adds or changes.** A departure that predates the
item's base, an open drift item's finding included, is not this story's
finding unless the change adds to it, extends it or copies it.

## Verdict

- **Reject** when the change adds a departure. The notes hold one line per
  departure:

  `DRIFT <principle> <file>:<line> <symbol> — <what>`

  `<principle>` is the principle's file name without `.md` (or
  `constitution`), `<file>:<line>` the place in the work tree, `<symbol>`
  the function, type, key or flag, and `<what>` one sentence on the
  contradiction.
- **Accept** otherwise, with the notes:

  `checked <n> changed paths against .satelle/constitution.md and its principles: <the principles a changed path touched, or none>`

```json
{"verdict": "reject", "notes": "DRIFT satelle-operator-rulings internal/x/http.go:40 Serve — adds a REST listener beside gRPC"}
```

```json
{"verdict": "accept", "notes": "checked 3 changed paths against .satelle/constitution.md and its principles: satelle-mechanism-vs-substrate, satelle-no-comments"}
```
