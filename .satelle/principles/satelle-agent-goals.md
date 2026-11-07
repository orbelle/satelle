---
name: satelle-agent-goals
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: Drive a story to the terminal state of its configured workflow with every gate accepted. Status is the sole proof of done. Never work around a block (gate, hook, fence); raise a story for the work, or surface a structural gap and stop. One story at a time outside an epic; an epic's children are driven to approved, and the driving agent fires the release when every open child is approved, then closes the epic.
---

# Agent goals

Drive a story only to the **terminal state** of its configured workflow, with
every reviewer gate on the path accepted. **Status is the sole proof of done.**

Do not patch status to skip a gate or invent process the workflow did not
configure. When a **structural** gap blocks the loop (bad config, a missing gate
skill), **surface it and stop**.

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

**One story at a time — outside an epic.**
Drive a single engaged story to its terminal state before engaging another. An
epic's children end DEV at `approved` and reach `done` together through the
release run; see *Driving an epic*. Reason: a child reaches `done` only through
a release run, and the release fires only once every open child is approved, so
driving one child to its terminal state before engaging the next would
deadlock. This is that rule's consequence, not an ordering rule.

## Driving an epic

**Children carry the work.** An epic has no plan step of its own. Each child is
readied and delivered by its own category workflow, where its own gates judge
it. Move the epic `backlog → underway`, its workflow's only route to `done`.

**Drive epic `<id>` to done:**

1. Drive each child on its own category workflow to `approved`, where DEV ends.
2. Run the readiness block below with `EPIC_ID` replaced by the epic's id. It
   exits 0 printing `every open child of <epic> is approved: fire the release`
   when the epic has open children and every one is `approved`. It exits 1,
   listing `<id> <status>` for each open child not yet approved (an earlier DEV
   state, or blocked), when any is not: drive those first and do not fire. It
   exits 0 printing `no open child of <epic>: close it` when no child is open.
   It exits 2 when the epic id does not resolve or the list fails.
3. On "fire the release", follow `.satelle/skills/release.md` from step 0 (it
   fires the `release` queue) through step 11 (the drift review). The run
   takes every approved story in the queue, the epic's children among them.
4. After the release run and the drift run are `done`, run the readiness block
   again. When it says "close it", present
   `satelle story set <epic> --status done`. The children-closed and
   done-review gates judge the move.

```sh
epic=EPIC_ID
satelle story get "$epic" >/dev/null || exit 2
open=$(satelle story list --parent "$epic" --open --json) || exit 2
if [ "$(printf '%s\n' "$open" | jq length)" -eq 0 ]; then
  echo "no open child of $epic: close it"
  exit 0
fi
pending=$(printf '%s\n' "$open" | jq -r '.[] | select(.status != "approved") | "\(.id) \(.status)"')
if [ -n "$pending" ]; then
  printf '%s\n' "$pending"
  echo "drive these to approved before the fire" >&2
  exit 1
fi
echo "every open child of $epic is approved: fire the release"
```

**A child the release sends back** is `in_progress` again, with a
`release-sendback` attachment naming the reason (read it with
`satelle story attachment <id> release-sendback`). The epic stays `underway`,
because children-closed refuses the epic's done move while that child is open.
Drive that child again to `approved` before the next fire. Then rerun the
readiness block and fire again before closing the epic.

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

See [[satelle-operator-rulings]] and the constitution (`.satelle/constitution.md`).
