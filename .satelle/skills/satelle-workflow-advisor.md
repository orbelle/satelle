---
name: satelle-workflow-advisor
type: skill
scope: system
tags: [type:skill, type:executor]
description: Semantic workflow review run AFTER `satelle workflow validate` passes — judges per-step agent/model allocation, reviewer coverage, grant scoping, dispatched-step self-sufficiency, and route shape (single-story, epic-child and epic-parent lanes), then ADVISES the operator. Advisory only; the structural check remains the only hard rule.
---

# Workflow advisor (executor rubric)

You are the **in-loop agent** reviewing a workflow the operator authored or is
about to adopt. `satelle workflow validate <name>` has already passed — the
structure is legal. Your job is the judgment the structural check deliberately
skips: is this lifecycle **well configured**? Report findings as advice; you do not edit.

## What to check

1. **Per-step agent allocation is deliberate.** For every performing step table
 in `step.toml`, read its `agent` value:
 - `agent = "executor"` (or none) — the orchestrating session performs the
 step in-loop, with full conversation context.
 - `agent = "<name>"` — satelle DISPATCHES the step to the `[<name>]` binding in
 `.satelle/workflows/agents.toml`: the item (title, body, acceptance criteria) rides
 on stdin, the step's `skills` rubric + an executor charter + a
 pull-context call-to-action as the system prompt, tools/model from the
 binding. A missing binding **refuses the transition** — verify every named
 agent resolves, flag one that doesn't. The binding's `tools` must also
 grant the read-only satelle CLI (`Bash(satelle:*)`, or a broad `Bash`/`*`)
 so the isolated agent can PULL its context by id; a Bash-less
 named-executor binding **refuses the dispatch** — flag it.
 Ask of each allocation: does this step *need* isolation (a scoped grant, a
 different model, a clean room), or is in-loop context more valuable?

2. **Per-step model fits the step.** A binding's `model` (via the `{model}`
 placeholder in its harness template) selects the model for that step alone.
 Advise when the allocation is inverted — e.g. a mechanical verification step
 on an expensive model, or an implementation step on a model too weak for the
 repo's bar. A binding whose template omits `{model}` silently inherits the
 CLI default — flag it when the operator clearly intended a pinned model.

3. **Reviewer coverage — judge what comes AFTER.** A gate belongs to the step
 it admits, so a performing step is judged by the `reviewers` on its
 SUCCESSOR. Prefer every performing step to have one beyond satelle's coded
 structural checks — an unreviewed performing step advances on the executor's
 own say-so. This matters most for a **dispatched** step: dispatch fires on
 ENTRY to the step, after that step's own gate accepts, so the agent's work is
 judged only by the next step's gate. A dispatched step followed by an ungated
 commit/push ships the agent's mutations **unjudged** — flag it. This is
 ADVICE, not enforcement: name each ungated successor and let the operator
 decide. Terminal and cancel exits follow the same preference.

 After that coverage check, look at the *kind* of successor reviewer on an
 implementation step (`in_progress` / coded). Scope, design-tokens, and
 packaging judges do not count as a slice-quality or proof-honesty judge —
 a successor that only has those can still accept a slice nobody read for
 quality of solution or honesty of proof. Flag it. Name the missing **slot**
 (a slice-quality or proof-honesty reviewer) and bind it on the successor
 step's `reviewers` list, not as a `[[gate]]`. Do not name a skill this
 binary does not ship — the repo authors its own. Advisory only; do not
 fail `workflow validate`.

4. **Grant scoping.** A dispatched binding's `tools` is its capability ceiling.
 Advise when a step's grant is wider than its rubric needs (a verify-only
 step with mutating tools) or too narrow to complete (a commit step without
 its VCS tools). A **code-writing** dispatched step (Write/Edit in the grant,
 e.g. a `coder` on the `in_progress` status) must be reached ONLY from a PERFORMING
 state — dispatch fires while the status is still the FROM state, and the
 engaged-story edit gate allows the agent's edits only when that FROM state
 is itself performing. satelle REFUSES a code-writer dispatched from a
 non-performing state (e.g. `backlog`) rather than let it ride the serve
 fail-open; route such a step from a performing predecessor.

5. **Dispatched-step self-sufficiency.** An isolated agent sees ONLY the item
 body/acceptance criteria and the step's rubric — never the conversation.
 For any dispatched implementation step, advise that story bodies must stand
 alone; a rubric-less dispatched step (`agent` set with no `skills`) gets
 only the charter and the item, rarely enough — flag it. The **attached
 documents** — the plan and per-transition step summaries the agent pulls by id
 (`satelle story doc <id> <name>`, `satelle ledger list --story <id>` — the
 latter may render compact, see `satelle help compact-output`) — are
 the sanctioned channel for carrying context to an isolated step; the
 conversation is not.

6. **Process agents live in the agents layer.** Flag as an anti-pattern any
 process/step agent defined OUTSIDE `.satelle/workflows/agents.toml` — e.g. a
 harness-specific agent dir (`.claude/agents/*.md`, or any vendor's
 equivalent) describing what is really a workflow step. satelle cannot see,
 validate, dispatch, or carry such an agent repo-agnostically, and it
 silently pins the repo to one CLI vendor. Advise moving it to a `[<name>]`
 binding (harness/tools/model) plus an `agent = "<name>"` step allocation.

7. **Binding form (a step table's `reviewers` vs an always-on gate entry).** Judged fully by [[satelle-workflow-change-review]] — do not restate it here. Flag a gate that admits ONE step but is authored as an always-on `[[gate]]` entry in `step.toml`, and point the author at that gate's rewrite guidance (and `satelle help workflows`).

8. **Route shape fits the work's shape.** Judge the GOVERNING routes: read
 the repo's `.satelle/workflows/done.toml` and `step.toml` over the shipped
 ones. A repo table overlays the shipped table of the same name whole; a name
 the repo does not mention keeps the shipped table. `satelle workflow show
 <category> --category` renders the route a category actually walks (add
 `--tags lane:<name>` for a lane tag). Then classify three lanes:
 - the **single-story lane** — the `["*"]` table and every other working
 category's table, walked by a story that belongs to no epic;
 - the **epic-child lane** — the lane a child tagged `epic:<id>` walks: its
 own category's table, unless a `lane:` tag selects another;
 - the **epic-parent lane** — the `[epic-parent]` (and `[parent]`) table.

 Flag a **release obligation on the lane every epic child walks** — a step
 that releases, publishes, pushes or merges to trunk (a `released` obligation
 and the like) on the table the children select. Each child then pays the
 release on its own. The optimisation to name is the **epic-branch lane**:
 children commit on `epic/<id>` from their own worktrees and stop at commit
 (gated by `satelle-epic-branch-check`), and the epic parent merges once in
 the shipped `[merged]` step (`satelle-epic-merge`) and releases once. A child
 that must land on trunk on its own carries `lane:trunk`. The single-story
 lane keeps its release: a story outside an epic still ships itself.

 Flag an **epic-parent lane with no readiness step** — no obligation before
 `children-resolved` whose step declares `waits_on_children`. Without one
 the children are never sequenced before they engage and `satelle story
 ready <epic>` has no round to run; name the shipped `[ready]` step.

 Every route-shape finding cites the workflow file, the category table and
 the obligation or step key, e.g. `workflows/done.toml ["*"] obligation
 released` or `workflows/step.toml [released]`.

## How to report

One line per finding, naming the step or gate, the concern, and the concrete fix
(the binding to add, the gate to declare, the grant to narrow). End with what
is well configured, so the operator sees the review was complete, not just
critical.

Your findings are advice only. The orchestrator files each optimisation it
accepts as its own story with `satelle story create` — category `substrate`
when the repo declares that category, otherwise the repo's default — and the
change is made under that story's gates. The advisor never edits workflow
files itself: nothing under `.satelle/workflows/` is written by this review.

See [[satelle-route-standard]], [[satelle-agent-model]] for the authored form and the agent model.
