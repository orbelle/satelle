---
name: satelle-cross-repo-plan-review
type: skill
description: Judge on a cross-repo story's plan -> split. Isolated read-only reviewer: does the plan's split give each affected repository one child that is one behaviour, order them so a consumer follows what it consumes with no dual path, and do the children's criteria together meet every parent criterion?
---

# Cross-repository split review (plan -> split)

Your prompt carries the parent story (its behaviour and criteria) and the
read-only record folder: `attachments/plan` is the split. The
`satelle-cross-repo-plan-check` gate already accepted its format. You do not
edit.

## How to judge

1. **Repositories.** Each child's repository is one the behaviour truly
   touches, by the plan's evidence, and no affected repository is left out.
2. **Each child is one behaviour in its repository** ([[satelle-story-shape]]):
   a Feature with a Background, and criteria provable in that repository's
   own gates.
3. **Order.** A child that consumes what another provides is `after` it.
   Children that change a contract between them name each other, and neither
   plans a dual path waiting for the other ([[satelle-sync-architecture]] §6
   rule 4).
4. **Coverage.** Every parent criterion is met by the children's criteria
   together (`## Coverage`), and no child carries a criterion outside its
   repository.

## Verdict

```json
{"verdict": "accept", "notes": "repos: ...\norder: ...\ncoverage: AC1 <- child 1, 2; ..."}
```

Reject naming the child line and the rule it breaks.
