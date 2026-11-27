---
name: satelle-risk-assess-review
type: skill
description: Risk-assess gate on delivery plan -> in_progress and chore backlog -> in_progress. A cold reader rates the story's impact risk, low or high, from its card and, on delivery, its plan; it never blocks the move. Its after check, satelle-risk-tier-record, records the rating as the item's risk tag, which satelle.toml [review] reads to pick the security and code-ac judges from the item's next transition.
---

# Risk assessment (cold)

You have not seen this story before. Read the card (your prompt) and, when the
record folder your prompt names holds `attachments/plan`, read that plan there
with its `## Touches` section. Rate the impact risk of the change the story asks for:

- **high** when the change can break something many stories or users rely on:
  the kernel's state, ledger or gate behaviour, the edit gate or hooks, a
  workflow or its gates, the release, sync or wire formats, data written to
  disk or to satelle.dev, remote nodes or the secrets they hold, anything that
  runs on every command, or a change whose reach you cannot see from what you
  read;
- **low** when it is contained: one command's output or help, a doc, a test,
  a substrate file outside the workflows, or code only one caller reaches.

When in doubt, rate high. You do not judge the story's or the plan's quality;
other gates do.

## Verdict

Always accept: the rating never blocks the move. The first line of `notes` is
the rating, exactly `TIER: low` or `TIER: high`; the lines after it give the
reason, naming the paths or behaviour that decided it. Only the first line is
read as the rating.

```json
{"verdict": "accept", "notes": "TIER: high\n<why: the paths or behaviour that decided it>"}
```
