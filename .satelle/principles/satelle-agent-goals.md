---
name: satelle-agent-goals
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: Drive a story to the terminal state of its configured workflow with every gate accepted. Status is the sole proof of done. Never work around a block (gate, hook, fence); raise a story for the work, or surface a structural gap and stop. One story at a time outside an epic; a session working alone does not end a story's drive at approved: it fires the release and follows the run to done. In a fleet (satelle-fleet-roles) a worker stops at approved (done for a substrate story), reports to its orchestrator, and never fires a release or lands; the orchestrator fires releases and lands substrate stories. An epic's children are driven to approved; the driving agent or the operator decides when to fire a release, so an epic may ship in one release or several, and the epic closes when every child is done. A hold parks the item for the operator: run the block breaker on it, then report it and stop, unless its resolution changes only the plan or a driver note.
---

# Agent goals

Drive a story only to the **terminal state** of its configured workflow, with
every reviewer gate on the path accepted. **Status is the sole proof of done.**

Do not patch status to skip a gate or invent process the workflow did not
configure. When a **structural** gap blocks the loop (bad config, a missing gate
skill), **surface it and stop**.

**A block is never worked around:** when a gate, hook deny, the edit gate, the
cross-repo fence or a harness refusal blocks an action, raise the work as a story
in the control repository (in a single-repo project, the repo itself), its `repo`
naming the repository the work belongs to, in the category whose route fits. Never do the blocked
work another way or hand the user a command that does it. When no category or
workflow fits, propose one and create it only with the user's approval.

**The workflow is the authority.** Follow every transition it declares — entry
gate, integration and deploy checks, close — without pausing to ask permission. A
declared step is authorised *by* the workflow, even when it builds, deploys, or
mutates local state; a block is a gap only if it *prevents* following it.
One declared move is not the session's: a state's `hold` parks the item for the
operator, and only the operator presents the move out of that park; the session
first runs the block breaker on it (`.satelle/session.md`), then reports the hold
and stops. The one exception is the restart a block resolution that changes only
the plan or a driver note allows the session.

**This session drives its own steps when both seats are in-loop** (both resolve
to the in-loop command: the same agent); this is not the fleet orchestrator of
[[satelle-fleet-roles]]. `complete sty_<id>` starts that story's
drive; an epic, or `drive epic <id> to done`, starts *Driving an epic*.

**A named performer hands the drive back here.** The step's agent does the step
and does not change status. When it returns, this session runs the next
`satelle story set` the workflow names, unless a hold has parked the item
(blocked): then it runs the block breaker, reports the hold and stops, unless
the resolution lets it restart the item (`.satelle/session.md`). A refusal there fences source edits. The
drive stays in this session.

**One story at a time — outside an epic.**
Drive a single engaged story to its terminal state before engaging another. A
session working alone does not end a story's drive at `approved`: it fires the
release (`satelle queue fire release --session <session>`, release/SKILL.md
step 0) and follows the run until the story is `done`; it waits only at a park
state or a state that declares `waits`.

**A worker in a fleet stops at `approved`.** A session given its story by an
orchestrator ([[satelle-fleet-roles]]) drives it to `approved`, or to `done` for
a substrate story, reports to the orchestrator and stops there. It never fires
a release and never lands a substrate story unless the orchestrator tells it
to land one; the orchestrator fires releases, lands substrate stories, runs
block breakers and carries operator decisions. A hold it reports and stops at,
as above.

**When to release is a call, the agent's or the operator's.** The driving
agent (in a fleet, the orchestrator) or the operator decides when to fire the
release queue: one approved
story, or several together, by the number of approved stories and the impact
of shipping them. A run takes every approved story in the queue, whoever
drove it. Within an epic, children reach `approved` and are released when that
call is made, so an epic may ship in one release or several; see *Driving an
epic*.

## Driving an epic

**Children carry the work.** An epic has no plan step of its own. Each child is
readied and delivered by its own category workflow, where its own gates judge
it. Move the epic `backlog → underway`, its workflow's only route to `done`.

**Drive epic `<id>` to done:**

1. Drive each child on its own category workflow to `approved`.
2. Release when the driving agent (in a fleet, the orchestrator) or the
   operator decides to: after one
   child, or once several are approved, by their number and the impact of
   shipping them. Follow `.satelle/skills/release/SKILL.md` from step 0 (it fires the
   `release` queue) through step 11 (the drift review). The run takes every
   approved story in the queue, the epic's children among them. An epic may
   have one release or several.
3. When the epic's work is done, run the block below with `EPIC_ID` replaced
   by the epic's id. It exits 0 printing `no open child of <epic>: close it`
   when no child is open; then present
   `satelle story set <epic> --status done --session <session>`, and the
   children-closed and done-review gates judge the move. It exits 1, listing
   `<id> <status>` for each open child, when any is open. It exits 2 when the
   epic id does not resolve or the list fails.

```sh
session=<session>
epic=EPIC_ID
satelle story get "$epic" --session "$session" >/dev/null || exit 2
open=$(satelle story list --parent "$epic" --open --json --session "$session") || exit 2
if [ "$(printf '%s\n' "$open" | jq length)" -eq 0 ]; then
  echo "no open child of $epic: close it"
  exit 0
fi
printf '%s\n' "$open" | jq -r '.[] | "\(.id) \(.status)"'
echo "these children of $epic are open: the epic stays underway" >&2
exit 1
```

**A child the release sends back** is `in_progress` again, with a
`release-sendback` attachment naming the reason (read it with
`satelle story attachment <id> release-sendback --session <session>`). The epic stays `underway`,
because children-closed refuses the epic's done move while that child is open.
Drive that child again to `approved`; a later release takes it.

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
