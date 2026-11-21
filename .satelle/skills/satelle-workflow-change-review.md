---
name: satelle-workflow-change-review
type: skill
description: Workflow gate judging edits to .satelle/workflows/ and internal/kernel/template/workflows/ — states, gates, transitions, skill and agent resolution, exits preserved, and reviewer order ([[satelle-reviewer-order]]). Fast-accepts workflow:n/a when the slice touches no workflow file. Read-only.
---

# Workflow-change review

Read-only. You judge whether this change's edits to workflow files are sound,
and nothing else: whether the work meets its criteria, stays in scope or is
secure is another reviewer's gate ([[satelle-reviewer-order]]). Your prompt
carries the item (title, body, criteria, tags), the move, the changed paths and
the diff since the item's base. The item's attachments are in the record folder
your prompt names (`attachments/<name>`): read `attachments/plan` there when
the story has one. Read the repository with Read, Grep and Glob; you do not
edit.

## Scope first — n/a fast-accept

This gate judges workflow TOML files:

- Project/repo workflows: `.satelle/workflows/**`
- Embedded default workflows shipped by `satelle init --session <session>`:
  `internal/kernel/template/workflows/**`

Other files under `.satelle/` (skills, principles, tasks, queues) are not
workflow files; `satelle-workflow-change-trigger` runs this gate only when the
story's diff touches a workflow path.

- **No workflow file changed** → accept immediately:
  ```json
  {"verdict": "accept", "notes": "workflow: n/a; no file under .satelle/workflows/ or internal/kernel/template/workflows/ changed"}
  ```
  Never reject for missing evidence of a surface the change never touched.
- **Touches a workflow file** → read every touched workflow file in full,
  `.satelle/principles/satelle-reviewer-order.md`, `.satelle/agents.toml`,
  `.satelle/satelle.toml`, and any skill added or referenced by the change,
  then judge below.

## How to judge (when the change edits a workflow)

A workflow is one TOML file, `.satelle/workflows/<name>.toml`, declaring its
states (`[states.<name>]`), its gates (`[gates.<name>]`), and its explicit
transitions (`[[transition]]` with `from`, `to`, and `groups`). See
`satelle help workflows --session <session>` and [[satelle-workflow-standard]].
Judge the change against these rules:

1. **Reviewer order ([[satelle-reviewer-order]]).** Read
   `.satelle/principles/satelle-reviewer-order.md` in full. A transition runs
   its `groups` in order, and a `sequential` group stops at its first reject
   (fail-fast). Every change that adds, removes or moves a reviewer gate on a
   transition must follow [[satelle-reviewer-order]]:
   1. Deterministic checks (no judge) first.
   2. Scope.
   3. Classifiers a later reviewer reads, before the reviewer that reads them.
   4. The costliest misses (security, then constitution, then domain process
      guards such as workflow-change).
   5. Fit to the story or criteria, judged last.

   A reviewer that reads another's output always runs after it. **Reject** any
   change that adds, removes or moves a reviewer gate against
   [[satelle-reviewer-order]]: name [[satelle-reviewer-order]], the workflow,
   transition and gate, and the exact place in the order where the reviewer
   belongs.

2. **States and exits are valid and preserved.**
   - Exactly one state sets `start = true` (neither `terminal` nor `park`),
     and at least one `terminal = true` state is reachable from start.
   - `park = true` sets `resumes = true` when a transition returns to
     `"@prior"`; a `hold = { rounds = N, to = "<park>" }` sits on a non-park,
     non-terminal state with `rounds >= 1` and a declared, gateless transition
     to that park state.
   - `edits` (`"tree"` or `"attachments"`) appears only on states with an agent
     `performer`; `waits` is non-blank and appears only on non-terminal,
     non-park states; every queue in `enqueue` exists under `.satelle/queues/`.
   - Do not silently drop a park, hold, rework, resume (`"@prior"`), or cancel
     transition without a stated reason in the story or plan.

3. **Gates are well-formed, wired, and not weakened.**
   - A check gate names a `type: check` skill and no `agent` or `panel`. A
     judged gate names a non-check skill, a `panel = [...]` with
     `combine = "all"|"any"|"majority"` (this repo names judges in `panel`, not
     bare `agent`), at least one judge of another model family than the
     from-state's dispatched performer ([[satelle-agent-alt-reviewer]]), and
     the repo's `after` verdict checks (`satelle-verdict-notes-check`, plus
     `satelle-verdict-reference-check` on gates judging a plan or change).
   - Every gate added or intended to guard a transition is listed in a
     `[[transition]]` group on every workflow transition that changes what it
     guards — a declared `[gates.<name>]` listed on no transition never fires.
   - Relevance (`paths`, `trigger`), `carry`, `timeout`, and `placement` match
     `satelle help workflows --session <session>`: `trigger` and `after` list
     only `type: check` skills; `placement = "remote:<id>"` is valid only on a
     check gate; `timeout` is a positive duration only on gates that run a
     check script. A gate should not over-fire on unrelated transitions.
   - Do not silently remove an existing gate, weaken `combine`, drop a `hold`,
     or bypass guards such as `tree-unchanged`, `judge-differs`, or
     `change-judged` without a stated reason in the story or plan.

4. **Transitions, references, and comments.**
   - Every state named in `from` (except `"*"`) and `to` (except `"@prior"`)
     exists under `[states.<name>]`; `"*"` never targets `"@prior"`; no
     terminal state appears in `from`; no `(from, to)` pair is duplicated.
   - Every gate in `groups` exists under `[gates.<name>]`; every group sets
     `mode` to `"sequential"` or `"parallel"` and lists at least one gate.
   - Every skill named by a state `performer`, gate `skill`, `trigger`, or
     `after` exists in `.satelle/skills/`, every agent named by a `performer`,
     `agent`, or `panel` exists in `.satelle/agents.toml`, and
     `[[workflows.rule]]` in `.satelle/satelle.toml` matches the workflow.
   - Workflow TOML files carry no `#` comments ([[satelle-no-comments]]).

Fair gate: judge the change as written, not style preferences.

- **Accept** when states, gates, transitions, skill and agent references,
  exits, and [[satelle-reviewer-order]] all hold.
- **Reject** with a specific, fixable note naming the workflow file, the
  state, gate or transition, the rule violated, and where the gate belongs or
  the rewrite needed.

## Verdict

```json
{"verdict": "accept", "notes": "<what you judged: name the workflow file, transition, gates and rules checked, or workflow: n/a when no workflow file changed>"}
```
