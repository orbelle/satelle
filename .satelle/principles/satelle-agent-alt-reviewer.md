---
name: satelle-agent-alt-reviewer
type: principle
tags: [type:principle]
applies_to: ["*"]
description: No step of the work is reviewed solely by judges of its author's model family, the interactive session included; the ruling is satelle-operator-rulings "Never reviewed solely by the author's family" (2026-11-17). [rules] reviewer_differs_from_performer checks only an exact model id; the binding author keeps the rest. Bindings change on evidence, and implementation outcome is the marker.
---

# The reviewer is alt to the author

1. **Never solely the author's family.** Which reviews a step needs is the
   ruling "Never reviewed solely by the author's family" in
   satelle-operator-rulings. A family is the model's vendor (claude, gpt,
   gemini, grok). The reason: a model shares its author's blind spots, and a
   smaller model of the same family shares most of them, so a judge from
   another family catches what the author's family cannot see. A cold judge
   of the author's family, with none of the author's context, still reads the
   work fresh.
2. **The session coder counts.** When the interactive session performs a
   state (`executor = session`), its harness's model is the author, though no
   binding names it. The ruling covers it as well.
3. **The rule checks only part of it.** `[rules]
   reviewer_differs_from_performer` refuses a judge whose model id equals the
   dispatched performer's. It does not see families or the session's model,
   so whoever edits `agents.toml` keeps the rest, and says in a comment on
   each judge binding why it is alt. Until sto_9203d7ac replaces it with a
   check of point 1, it still refuses a panel member running the performer's
   exact model, even when another member is of another family.
4. **Bindings change on evidence.** Which model plans, codes and judges is a
   loop of trial and outcome. Implementation is the marker: a plan or a
   review is good when the implementation it led to or let through holds up.
   Change a binding when the ledger's outcomes say so, and record why on the
   binding.
5. **A reviewer is named after its model.** A judge binding is
   `reviewer-<model>`, its `model` value verbatim, so a workflow's gates and
   every gate result say which model judged (operator, 2026-11-14;
   sto_e65b2d30). A binding changed to another model is renamed, with every
   gate that names it, in the same change.
