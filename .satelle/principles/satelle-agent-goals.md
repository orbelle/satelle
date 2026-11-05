---
name: satelle-agent-goals
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: Drive a story to the terminal state of its configured workflow with every gate accepted. Status is the sole proof of done. Never work around a block (gate, hook, fence); raise a story for the work, or surface a structural gap and stop. One story at a time outside a container or under its sequential = true override; otherwise the wave says who may start. Given an epic, ready it first.
---

# Agent goals

Drive a story only to the **terminal state** of its configured workflow, with
every reviewer gate on the path accepted. **Status is the sole proof of done.**

Do not patch status to skip a gate or invent process the workflow did not
configure. When a **structural** gap blocks the loop (bad config, a missing gate
skill), **surface it and stop**: [[satelle-review-structure-not-process]].

**A block is never worked around:** when a gate, hook deny, the edit gate, the
cross-repo fence or a harness refusal blocks an action, raise the work as a story
in the repo it belongs to, in the category whose route fits. Never do the blocked
work another way or hand the user a command that does it. When no category or
workflow fits, propose one and create it only with the user's approval.

**The workflow is the authority.** Follow every transition it declares — entry
gate, integration and deploy checks, close — without pausing to ask permission. A
declared step is authorised *by* the workflow, even when it builds, deploys, or
mutates local state; a block is a gap only if it *prevents* following it.

**This session is the orchestrator when both seats are in-loop** (both resolve
to the in-loop command: the same agent). `complete sty_<id>` starts that story's
drive; an epic, or `drive epic <id> to done`, starts *Driving an epic*.

**A named performer hands the drive back here.** The step's agent does the step
and does not change status. When it returns, this session runs the next
`satelle story set` the workflow names. A refusal there fences source edits. The
drive stays in this session.

**One story at a time — outside a container, or when it sets sequential = true.**
Drive a single engaged story to its terminal state before engaging another.
Inside a container, children run in parallel by default and the wave says who
may start; see *Driving an epic*.

## Driving an epic

**Plan the epic first, unprompted.** When you are given an epic, or a child of an
epic whose readiness does not hold, run `satelle story ready <epic>` first, before
engaging any child, without waiting to be told. You own each child's delivery:
first message the ready performer a line per child — `in-loop`, `local` for a
subagent, or `team:<path>`, and why. `satelle story wave <epic>` prints it.

**Drive epic `<id>` to ready** walks only the container to ready; it engages no
child. Its round is the epic's own judgement of order, parallelism and
achievability and changes no child's status. With `ready_children` absent or true
it also runs each backlog child's readiness round (each stays in backlog); with
`ready_children = false` each child is planned through its own backlog → plan step
when `satelle story wave` lets it start.

**Drive epic `<id>` to done** requires the container at its waiting step.
`satelle story wave <id>` returns the set that may be engaged. Drive each child
on its own category workflow to its own terminal state. Close the container only
after the wave is empty because every child is terminal
([[satelle-story-classification]] defines the children).

When `satelle story wave` exits non-zero, that is a **stop**: surface its reason.
A zero exit naming children is not — drive them. Do not pick a child by `order:`,
by the sprint, or by title; `satelle story wave` alone says who may start.

You open the worktrees. Cut an independent child from the epic base, the branch
the container was engaged on. Cut a child whose `depends-on` target is done and
already on trunk from trunk; so too a done target
with no branch of its own — never cut from a branch that does not exist. Cut a
child whose target is done but not yet on trunk from that target's branch, not
from main. A
cancelled dependency is a stop: cut no worktree from it, do not retarget the edge.

## When an engaged story cannot satisfy its ACs

Do **not** weaken the ACs (definition freeze). Diagnose:

1. **World not ready** (ACs correct; dependency or external gap): file a
 **dependency story** that removes the blocker. If the workflow offers a
 **blocked** state, select it (with a reason), tag this story
 `blocked-by:<dependency sty id>`, and later resume `blocked → in_progress` with
 the **same** ACs.
2. **AC wrong** (definition misconceived): `cancel` with a reason, then create a
 corrected story tagged `supersedes:<cancelled sty id>`.

Relations are **tags** only.

See [[satelle-agent-model]], [[satelle-done-is-last]], [[satelle-local-first]], [[satelle-constitution]].
