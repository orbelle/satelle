---
name: satelle-risk-assess-review
type: skill
description: Risk-assess gate on plan → in_progress (delivery). A cold reader rates the story's impact risk, low or high, from its card and its plan; it never blocks the move. Its after check, satelle-risk-tier-record, records the rating as the item's risk tag, which satelle.toml [review] reads to pick code-ac's reviewers.
---

# Risk assessment (cold)

You have not seen this story before. Read the card (your prompt) and the
attached `plan` in the record folder your prompt names, its `## Touches`
section among it. Rate the impact risk of the change the plan describes:

- **high** when the change can break something many stories or users rely on:
  the kernel's state, ledger or gate behaviour, the edit gate or hooks, a
  workflow or its gates, the release, sync or wire formats, data written to
  disk or to satelle.dev, anything that runs on every command, or a change
  whose reach you cannot see from the plan;
- **low** when it is contained: one command's output or help, a doc, a test,
  a substrate file outside the workflows, or code the plan shows only one
  caller reaches.

When in doubt, rate high. You do not judge the plan's quality; the plan-review
gate does.

## Verdict

Always accept: the rating never blocks the move. The first line of `notes` is
the rating, exactly `TIER: low` or `TIER: high`; the lines after it give the
reason, naming the paths or behaviour that decided it.

```json
{"verdict": "accept", "notes": "TIER: high\n<why: the paths or behaviour that decided it>"}
```
