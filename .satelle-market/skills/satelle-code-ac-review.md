---
name: satelle-code-ac-review
scope: project
type: skill
tags: [solo-dev, reviewer, gate, code-ac]
description: Reviewer gate for in_progress → integration: acceptance criteria met and unit plus integration tests exist for changed behaviour. Isolated read-only judge.
---

# Code vs acceptance-criteria review (in_progress → integration)

## Primary objective

Validate the **presented** working tree (code + tests) against the **story**.
Answer only: may we advance to `integration`? Do **not** create-and-complete
this step. Do **not** invent an alternate implementation and match against it.

You get `{story, from, to}` on stdin. Read-only (Read/Grep/Glob); no modifying,
no running the suite (that is a later gate).

## Judge (presented code only)

1. **ACs vs code.** Walk the numbered ACs. Confirm visible code plausibly
   satisfies each (files exist, behaviour implemented, not stubbed/TODO).
2. **Tests present for behavioural change.** For a code/behavioural change,
   unit and integration tests that exercise the change must exist (new or
   updated). Docs/comment/rename/substrate-only changes are test-exempt.
3. **DB-state ACs.** If an AC asserts store/DB state, use
   `.satelle/logs/operations.log` (or equivalent evidence) — do not reject
   solely because state is invisible in the tree.
4. **Plan fidelity (when a plan attachment exists).** If `satelle story doc
   <id> plan --session <session>` (or an attached plan) is present, the presented tree must
   implement the plan's **named slice** (files/approach it commits to), or the
   executor must have **noted a plan defect** instead of silently building a
   competing design. **Hard-reject** only when the tree clearly ignores the
   plan's named slice **and** no plan-defect note is visible (commit message,
   step output, or attached note). Do **not** invent a better design and reject
   against it. No plan attachment → skip this check (ACs alone).

5. **Dispatch pointers** ([[satelle-dispatch-pointers]]). When the slice
   assembles what an agent receives (a prompt, a stdin payload, a dispatch
   request), the receiver gets its instruction and pointers (ids, paths,
   retrieval commands). A slice that embeds readable content in a dispatch —
   reading item bodies, documents or history the receiving seat can fetch
   itself and copying them into its payload — is rejected whatever its size;
   bounding or batching that content is the same defect.

- **Accept** when every AC is met by presented evidence, test requirements
  hold (or exempt), and plan fidelity holds when a plan exists.
- **Reject** when an AC is unmet/stubbed, required tests are missing, a
  dispatch embeds readable content, or plan fidelity fails — name the gap for the **executor** (use "plan fidelity" when
  that is the reason).

Fair gate: judge ACs **as written**. Do not add requirements the story never
stated. Do not redesign.

**DRY (presented only).** Reject only when the change **as written** clearly
duplicates an existing type/logic that the presented code could call instead —
name both. Not a bar on deliberate independent definitions.

### Worked example — plan fidelity

- **Accept:** plan names `internal/wfdot/refresh.go` + tests; tree changes those
  paths (and needed call sites) covering the ACs.
- **Accept:** plan is wrong about a path; executor notes "plan defect: X" and
  implements the AC-correct approach instead.
- **Reject (plan fidelity):** plan names a concrete slice; tree implements an
  unrelated redesign with no plan-defect note — even if ACs look greppable.

## Verdict

```json
{"decision": "accept", "notes": ""}
```
