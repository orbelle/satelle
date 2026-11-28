---
name: satelle-cross-repo-done-review
type: skill
description: Judge on a cross-repo story's underway -> done. Isolated read-only reviewer: once every child is closed, judge the parent's behaviour and each of its criteria against the children's combined change in the combined-change attachment, and accept or reject the parent on that.
---

# Cross-repository done review (underway -> done)

Your prompt carries the parent story (its behaviour and its acceptance
criteria) and the read-only record folder: `children.md` (each child's id,
status and title), `attachments/plan` (the split), and
`attachments/combined-change`. The `satelle-cross-repo-combined-check` gate
already confirmed that `combined-change` lists each done child's current frozen
change, followed by its diff. You do not edit.

The children passed their own repositories' gates; that is not this question.
Judge whether, **together**, their changes deliver the parent's one behaviour.

## How to judge

1. **The behaviour.** Name the parent's behaviour from its body, and judge the
   combined diff against it. Record the first notes line as
   `behavior: holds — "<the behavior, quoted>"` or `behavior: fails — "<...>"`.
   A behaviour that fails is a reject, even when each criterion looks met.
2. **The criteria.** Then one line per parent criterion,
   `AC<n>: met|not met — <evidence>`, citing the child sections and files of
   `combined-change` that meet it. A criterion met only by a `cancelled`
   child is not met.
3. **The seams.** Where one child consumes what another provides (the
   contract, the order in `attachments/plan`), check that the two sides match
   in the diffs: the names, fields and versions one side sends are the ones the
   other reads.

## Verdict

```json
{"verdict": "accept", "notes": "behavior: holds — \"<the behavior>\"\nAC1: met — <evidence>"}
```

Reject naming each unmet criterion or seam.
