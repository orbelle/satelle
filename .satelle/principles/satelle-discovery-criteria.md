---
name: satelle-discovery-criteria
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: Discovery (backlog → plan) proves the premise and authors the criteria; the driver re-presents rejections as they stand and lets the route apply the criteria. A ready round takes no edge.
---

# Discovery authors the criteria; the route applies them

The readiness step is discovery: the planner proves the premise and authors the
criteria under `## Acceptance criteria` in the plan, which the readiness
reviewers judge. A diagnosis is a hypothesis until discovery proves it: never
rewrite a story or its criteria on an unchecked claim, and re-read the code a
reviewer cites.

- **Re-present a rejection as it stands.** satelle hands the planner each
  seat's findings (`findings_to_answer`); message it only with a decision of
  your own, such as a scope ruling.
- **A `satelle story ready` round takes no edge.** `satelle story set <id>
  --status plan` takes backlog → plan, then read the plan with `satelle story
  doc <id> plan`.
- **The route applies the criteria** on plan → in_progress (`apply_criteria`);
  run no `story set --acceptance`. Where a route declares none, apply the lines
  verbatim; the definition check prints the exact command. Never reword them:
  a change goes back through plan → backlog and readiness.

See [[satelle-agent-goals]], [[satelle-edits-require-a-story]].
