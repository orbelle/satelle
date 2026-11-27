---
name: satelle-workflow-standard
type: skill
description: The session agent follows this before creating, changing or checking any workflow the user asks about. It names where the repo's workflow standard is; the repo edits its Standard line to name its own.
---

# Workflow standard

Standard: none set

The repo edits the Standard line above to name where its workflow standard
is: a URL, a page, or a path in this repo.

**When.** Before you create, change or check any workflow the user asks about:
this repo's satelle workflows and any other workflow the standard governs, a
CI/CD pipeline for example. The standard defines which workflows it covers;
read it to find out.

**A standard is named.** Read it with your own tools; satelle does not fetch
it. If it covers the workflow in hand, it is the guidance for a change and the
rubric for a check: cite the standard's rule ids for each decision and each
finding. If it does not cover that workflow, say so and carry on. If you
cannot read it, say so and stop; do not guess its rules.

**None is set.** Say that no workflow standard is set for this repo, and carry
on with the request.

**Who judges.** satelle blocks nothing here. The repo's own workflow review
judges compliance, and a workflow that does not comply fails that review.
