---
name: substrate
scope: project
type: skill
tags: [type:skill]
description: In-loop executor skill for the substrate route's in_progress step. Author the markdown-only slice under .satelle/ and/or docs/, reindex/validate, commit and push (where substrate is tracked in git) while engaged — no version bump, no release. Close is gated by satelle-substrate-only-check.
---

# Substrate (in-loop executor step)

You are the **executor** for a **substrate-only** story (`category: substrate`),
in-loop on the driving session (`agent=executor`). The slice is authored markdown
under `.satelle/` (workflows, skills, principles, documents, tasks, config) and/or
`docs/` — **not** Go code. There is no plan/code-ac/integration/release path and
**no `.version` bump**.

## Do

1. **Author the slice** named by the story body and ACs — smallest honest edit.
2. **Keep it substrate-only.** Paths outside `.satelle/`, `docs/`, and
   `[gate] edit_exempt_paths` will fail `satelle-substrate-only-check` on close.
3. **Reindex / validate** so the index and structure stay green:
   ```bash
   satelle reindex
   satelle agent validate
   satelle workflow validate
   satelle skill validate
   ```
4. **Commit + push (where substrate is tracked in git) while engaged** at `in_progress` (the commit gate requires an
   engaged story). Conventional subject ending with the story id; **no** AI
   attribution; **no** version bump.
5. **Stop for the close gate** — request `done`; do not self-enact around
   `satelle-substrate-only-check`.

## Do not

- Touch `internal/`, `cmd/`, `tests/`, or other binary sources under this workflow.
- Cut a release or tag for markdown-only work.
- Leave the story open after the slice is pushed — drive to `done` or `cancelled`.

See [[satelle-agent-model]], the substrate workflow prose, and [[satelle-mechanism-vs-substrate]].
