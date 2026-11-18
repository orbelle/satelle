---
name: satelle-agent-alt-reviewer
type: principle
tags: [type:principle]
applies_to: ["*"]
description: No step of the work is reviewed solely by judges of its author's model family, the interactive session included; the ruling is satelle-operator-rulings "Never reviewed solely by the author's family" (2026-11-17). The check skill satelle-judge-differs-from-performer-check judges point 1 for dispatched performers on the gates that name it; the session's model is the binding author's to keep alt. Bindings change on evidence, and implementation outcome is the marker.
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
3. **The check covers dispatched performers.** The check skill
   `satelle-judge-differs-from-performer-check`, on the gates that name it
   (this repo: the first group of substrate in_progress -> done and of
   delivery and chore in_progress -> integration), fails a gate where no
   judge is of another family than the dispatched performer of the state the
   move leaves. A single judge must differ; a panel passes when one member
   differs, so a cold same-family judge may sit beside another family's
   (point 1). A session performer has no model in config and is not
   compared, so whoever edits `agents.toml` keeps the judges alt to the
   session's model (point 2), and says in a comment on each judge binding
   why it is alt.
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
