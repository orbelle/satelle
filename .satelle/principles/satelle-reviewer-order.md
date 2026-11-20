---
name: satelle-reviewer-order
type: principle
tags: [type:principle]
applies_to: ["*"]
description: The reviewers on a transition run in sequence and fail fast, in a priority order: deterministic checks, then scope, then classifiers later reviewers read, then the costliest misses, then fit to the card or criteria. An agent adding or moving a reviewer places it by this order.
---

# Reviewer order

Operator, 2026-11-20: "The sequential processing is valid, in a fail fast
process, that also indicates a priority order. I'm happy for that to remain,
but expand to the agent ordering the reviewers accordingly." See
[[satelle-operator-rulings]] (2026-11-20) and `satelle help workflows --session <session>`
for how groups run: a sequential group stops at its first reject, and a group
that refuses stops every later group.

Each review concern is its own gate with its own skill, judged cold and
isolated. On a transition they run in this order:

1. **Deterministic checks** (no judge): they are cheap and certain, and a
   change they refuse is not worth a judge's tokens.
2. **Scope** (binary-scope): a plan or change in the wrong place makes every
   later verdict moot.
3. **Classifiers a later reviewer reads** (risk-assess, whose tier sets the
   code-ac panel): before the reviewer that reads them.
4. **The costliest misses** (security, then the constitution reviewer): a miss
   here ships a risk no later fix fully undoes.
5. **Fit to the card or the criteria** (plan-review, code-ac): judged last, on
   a change that is in scope and safe.

A reviewer that reads another's output always runs after it. The reason for
each reviewer's place is this rule: workflow files carry no comment
([[satelle-no-comments]]). An agent adding or moving a reviewer places it by
this order.
