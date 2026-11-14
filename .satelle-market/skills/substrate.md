---
name: substrate
scope: project
type: skill
tags: [solo-dev, executor, substrate]
description: Executor skill for substrate-only work: edit process markdown/config, validate, commit without a product version bump or release tag.
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
   satelle workflow validate --session <session>
   satelle skill validate
   ```
4. **Commit + push while engaged** at `in_progress` (the commit gate requires an
   engaged story). Conventional subject ending with the story id; **no** AI
   attribution; **no** version bump.
5. **Stop for the close gate** — request `done`; do not self-enact around
   `satelle-substrate-only-check`.

## Do not

- Touch `internal/`, `cmd/`, `tests/`, or other binary sources under this workflow.
- Cut a release or tag for markdown-only work.
- Leave the story open after the slice is pushed — drive to `done` or `cancelled`.

See [[satelle-agent-model]], the substrate workflow prose, and `satelle-repo-agnostic` (repo principle/doc).
