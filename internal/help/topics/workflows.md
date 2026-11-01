# Workflows — how a story's lifecycle is chosen

A **workflow** is a story's lifecycle: the steps it moves through and the
reviewers that gate entry to each. satelle does not hardcode a lifecycle — the
operator authors it as substrate under `.satelle/workflows`, and satelle enforces
it.

The authored form is a **derived route**: two files, `done.toml` and `step.toml`.
`satelle help workflow-convert` is the guide for a repo still on the retired
markdown route source — or still carrying a retired DOT graph — and the
key-by-key mapping that converts them.

## The two halves

**`done.toml` — what DONE means.** One category table per story category, each an
ordered list of obligations, plus the exits the binary synthesises. `["*"]`
governs any category with no table of its own.

```toml
[meta]
name = "done"
type = "workflow"
scope = "project"
description = "What done means per category."

["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked", gate = "satelle-story-blocked-review" }
cancel = { state = "cancelled", gate = "satelle-story-cancel-review" }

[docs]
obligations = ["raised", "doc-authored", "docs-verified"]
cancel = { state = "cancelled", gate = "satelle-story-cancel-review" }
```

**`step.toml` — what discharges each obligation.** One obligation-keyed table per
step, one `[[gate]]` entry per always-on gate. **The table key IS the obligation**
the step discharges — there is no `provides` key.

```toml
[meta]
name = "step"
type = "workflow"
scope = "project"
description = "Step catalogue and always-on gates."

[coded]
status = "in_progress"
agent = "executor"
skills = ["code"]
reviewers = ["satelle-story-plan-review", "satelle-story-architecture-review"]
reviewer_agent = "reviewer"
parallel = 0
requires = ["raised"]
```

A step that hands the story to a **team remote** and waits for it declares
`remote_wait = { remote = "<team remote name>", on = { <outcome> = "<status>", … } }`
(and no `agent`: nobody performs a waiting step). The local drive reads the
remote's run for the story from satelle.dev — queued with its position, running
with its step, concluded with its outcome — and `satelle story get` / `story
route` show the same line. While the run is queued or running, `satelle story
drive` ends waiting (exit 0) and records nothing. When it concludes, the drive
takes the edge `on` maps the outcome to: through the step's gates like any move,
or the park for a park status, relaying the run's findings to the performer of the
step it leads to. An outcome `on` does not map parks the story. Each target must
be an edge the route declares from the waiting step — spine successor, recover,
park or cancel — and `satelle validate` fails (`FAIL  workflows (route)`) when it
is not. Which remote, which outcomes and which statuses are all the repo's
configuration; satelle names none of them.

Both files carry a `[meta]` table (`name`, `type = "workflow"`, `scope`,
`description`) and **must not** carry `applies_to` — done.toml's category tables
are the selector, and a second one would be a second precedence rule. A lifecycle
hook (`create_review` / `[[meta.hooks]]`) rides on **done.toml**'s `[meta]`.

**The binary owns topology.** Order is a topological sort of `requires` against
the obligation each step's table key discharges; cancel from every non-terminal
step, park from anywhere, backward movement and park → cancel are all
synthesised. Authoring a `cancelled` or `blocked` step by hand is the most
common conversion mistake.

## Precedence

A repo's own route governs the categories its `done.toml` claims. The route the
binary SHIPS is order zero: it governs a category only when no authored workflow
claims it, so upgrading the binary never re-routes a repo behind its back.

```
satelle workflow list --category <category>
```

The head of that list is the active choice. A workflows doc that declares no
route governs nothing: satelle REFUSES transitions under it, naming
`satelle help workflow-convert`, rather than falling back and silently dropping
every gate the repo authored.

## The choice is stamped

At create, the governing lifecycle is **stamped** on the story — a
`workflow:<name>` tag plus a `workflow_stamped` ledger entry — so the trail
records what governed. With a derived route the stamp is `workflow:default` for
every category; which LANE applies is chosen by the story's category, and
`satelle story restamp` re-resolves it after a re-categorisation.

## Avoiding misconfiguration

`satelle workflow validate` flags what the operator should fix:

- **An unresolved reviewer skill** — a step or gate names a reviewer that does
  not resolve in the substrate. WARN, not FAIL, on the named form: a repo
  mid-authoring writes its route before its gate skills.
- **An unresolved executor rubric** — a step's `skills:` that does not resolve.
  That IS a hard failure: the step cannot be performed, so the story can never
  reach its terminal state.

That command also prints each gate's **effective model** (the binding's model, or
the CLI default when the binding pins none). `satelle agent validate` prints the
same surface under its grant listing. To review one gate on a different model,
define a second `role = "reviewer"` binding in `.satelle/workflows/agents.toml` and name it
as that step's `reviewer_agent:` — see `satelle help agent-dispatch` and the
`satelle-route-standard` principle.

## Binding a reviewer: a step's `reviewers` vs an always-on `[[gate]]`

How a reviewer is **bound** matters as much as which skill it runs.

### A step's `reviewers` — gate-specific reviewers (prefer this)

A gate belongs to the step it ADMITS. Bind a gate-specific reviewer to that
step:

```toml
[integrated]
status = "integration"
reviewers = ["satelle-code-ac-review"]
reviewer_agent = "reviewer"
requires = ["coded"]
```

- **List order = execution order** (and ledger order). By default reviewers run
  **sequentially**, **all-must-accept**, with **first-reject short-circuit**
  (later reviewers are not invoked once one rejects) — but only when the step
  says so with `parallel = 0`.
- **Concurrency is the default for 2+ reviewers.** Unset `parallel` runs the
  list concurrently with no short-circuit, so a rejected round spends tokens on
  every reviewer. Set `parallel = 0` for sequential, or `parallel = N` (cap 4) to
  bound the fan-out. Aggregation stays all-must-accept in the binary; a
  multi-reject refusal names every rejecting reviewer.
- **`bundle = true` shares a session.** The LLM reviewers that share binding,
  model, effort and tool grant run as one session with a verdict per rubric,
  instead of one session each (see `satelle help workflow-convert`). Off by
  default; `parallel` then schedules the bundles and any reviewer left alone.
  A rubric that must judge alone sets `independent: true` in its frontmatter.
- **`panel` judges one skill on several seats.** `panel` is an ordered list of
  `agents.toml` section names. Each reviewer skill on the step runs once per
  seat, cold and one-shot. `combine` names a functional-check skill whose
  check reads the seat records on stdin and whose exit code is the combined
  decision. The binary ships `satelle-panel-all-accept` and
  `satelle-panel-majority` as markdown; a repo authors another rule as a new
  check skill. There is no compiled rule. A one-seat `panel` is the same shape
  as `reviewer_agent` and may omit `combine`. `panel` together with
  `reviewer_agent`, `combine` without `panel`, or two or more seats without
  `combine` is refused by name. `panel` together with `bundle` is an error: a
  panel cannot share a session. An unavailable seat is recorded and handed to
  the check as unavailable; it is never an accept. The same keys on a `[[gate]]`
  apply only to that skill (`panel` together with that gate's `agent` is
  refused the same way).

### An always-on `[[gate]]` — multi-step only

A `[[gate]]` entry with `on` is an always-on gate for every entry into the steps
it names:

```toml
[[gate]]
skill = "satelle-estimate-actual-review"
on = ["in_progress", "done"]
for = ["*"]

[[gate]]
skill = "satelle-step-summary"
agent = "reviewer"
mandatory = true
for = ["*"]
```

Use it **only** when the gate genuinely belongs on every entry into those steps
(estimate at begin-work and close; step summaries). Authoring a gate-specific
check as a single-step `[[gate]]` is the common misuse.

`for` names the categories whose route the gate belongs to. In a shared
catalogue, omitting it fires the gate on every lane — including ones with no
release to verify.

### The over-fire trap

A gate on one step matches **every** transition into it, including recovery
(`integration → in_progress`). The gate then re-fires on every fix-loop
re-entry — an extra reviewer invocation per rework cycle. Bind it to the step's
own `reviewers` instead unless always-on is what you mean.

See the embedded `satelle-workflow-change-review` gate for a content judgment of
route edits; structural validate stays PASS/FAIL only.

## Four things a lifecycle governs — don't confuse them

They differ in WHERE they are declared, WHEN they fire, and WHO decides:

| | Declared in | Fires | Decided by |
| --- | --- | --- | --- |
| **Step gates** | a step's `reviewers`, or a `[[gate]]` entry | on a status change | a reviewer skill's verdict (or a coded ```check) |
| **Lifecycle hooks** | done.toml's `[meta]` (`create_review` / `[[meta.hooks]]`) | outside the status graph — at story creation today | a reviewer skill's verdict |
| **Deterministic structure checks** | nowhere — they are the binary's contract | always, before anything else | code (`internal/structure`); no LLM, never flaky |
| **Agent judgments** | a reviewer skill's rubric | wherever a gate or hook names it | an isolated agent returning accept/reject |

**Step gates** are the status graph. They can only judge a move from one state to
another, so anything off the graph is out of their reach.

**Lifecycle hooks** exist for exactly that gap. Story creation has no `from`
state — the item does not exist yet — so it cannot be a transition. A hook
declares the operation, the skill that judges it, and the logical agent that runs
the skill:

```toml
# Shorthand — default agent = reviewer:
[meta]
name = "done"
type = "workflow"
scope = "project"
create_review = "my-create-review"

# Or the array form, when a non-default agent is needed (do not set both):
# [[meta.hooks]]
# operation = "create_review"
# skill     = "my-create-review"
# agent     = "strict-reviewer"
```

The scalar `create_review = "my-create-review"` is the documented shorthand for the
same thing with the default agent. A hook declares **who** runs a skill, never
**how**: model, effort, command, transport and tool grant stay in
`.satelle/workflows/agents.toml`. `satelle workflow show done` prints each hook's full
resolved allocation; `satelle agent validate` refuses one whose agent is missing,
is not `role = "reviewer"`, or is `command = "in-loop"`.

**Deterministic structure checks** are not configuration at all — a story needs a
title, a clear goal body, and a category, judged by code so the result is
harness-independent and identical every run. Acceptance criteria are not part of
that check. A structural failure pre-empts: no hook or gate reviewer is reached
on a malformed draft.

**Agent judgments** are the rubrics themselves. A gate or hook says *which* skill
judges; the skill says *what* the judgment is. That split is why adding a
criterion is a markdown edit rather than a release.

## Beyond validate: the semantic review

`workflow validate` is deterministic structure only. The judgment it deliberately
does not make — is each performing step's `agent:` allocation (and its binding's
model) deliberate; does each performing step have a reviewer on the step that
follows it; is a dispatched binding's grant scoped to its step; is a dispatched
implementation step self-sufficient (an isolated agent sees only the item and its
rubric, never the conversation); is each gate bound where it belongs — is the
**agent's** review, guided by the embedded `satelle-workflow-advisor` skill
(`satelle doc get skills satelle-workflow-advisor`). Its findings are ADVICE to
the operator; the coded structural check stays the only hard rule. satelle
prints a one-line prompt to run it at the end of `satelle init` and of a passing
`satelle validate` or `satelle workflow validate`, and when the repo's first
epic readiness round is accepted under a readiness step that declares `advise`
(the shipped `[ready]` step does). It runs nothing.

For the full contract a dispatched (`agent: <name>`) step runs under — how it is
briefed, how it pulls the story/documents/ledger by id, what makes it
self-sufficient, and why the step that follows it must carry the review — see
`satelle help agent-dispatch`.

## Reading a story's route — `satelle story route <id>`

The two halves answer "what is the lifecycle". They do **not** answer "what is
THIS story's lifecycle" — which tag-scoped gates are on for it, where it is now,
and why each gate it has already passed decided as it did.

`satelle story route <id>` is that answer, as one artifact:

- the **plan half** — the ordered steps between the story and done, each with the
  obligation it discharges, who performs it under which rubrics, and the
  reviewers gating entry, marked when a gate is present only because the story
  carries a tag (and marked `skipped` when it is absent for want of one);
- the **outcome half** — appended as each step resolves: every reviewer's
  verdict, its reasoning, and a pointer to the full output on the ledger.

They are deliberately the **same document**. A route read separately from the
verdicts is two things that drift.

The route renders live before a story has moved, so it is answerable from
backlog, and it never requires opening a workflow file. That matters because the
graph is no longer authored: there is no diagram to read, and refusals carry the
same weight — an engine refusal names the **rule** that fired, why it fired on
this story, and the legal moves it leaves open.
