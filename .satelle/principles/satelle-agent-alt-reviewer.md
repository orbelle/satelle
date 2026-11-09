---
name: satelle-agent-alt-reviewer
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Every gate's judge runs a model of another family than the planner and the coder of the work it judges, the interactive session included. [rules] reviewer_differs_from_performer checks only an exact model id; the binding author keeps the rest. Bindings change on evidence, and implementation outcome is the marker.
---

# The reviewer is alt to the author

1. **Another family judges.** Each gate's judge runs a model of a different
   family (vendor) than the planner and the coder of the work it judges. A
   model shares its author's blind spots; a smaller model of the same family
   shares most of them. A judge from another family catches what the author's
   family cannot see.
2. **The session coder counts.** When the interactive session performs a
   state (`executor = session`), its harness's model is the author, though no
   binding names it. Choose judges alt to it as well.
3. **The rule checks only part of it.** `[rules]
   reviewer_differs_from_performer` refuses a judge whose model id equals the
   dispatched performer's. It does not see families or the session's model,
   so whoever edits `agents.toml` keeps the rest, and says in a comment on
   each judge binding why it is alt.
4. **Bindings change on evidence.** Which model plans, codes and judges is a
   loop of trial and outcome. Implementation is the marker: a plan or a
   review is good when the implementation it led to or let through holds up.
   Change a binding when the ledger's outcomes say so, and record why on the
   binding.
