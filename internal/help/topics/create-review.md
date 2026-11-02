# Adding a create-review (worked example)

A **create-review** is an optional content/alignment reviewer that judges a
story draft at `satelle story create`, after the deterministic structure check
(title, clear goal, category). Acceptance criteria are not part of that check. It is **opt-in twice over**: the repo
enables create-gating (`[review] gate_create = true` in `.satelle/satelle.toml`),
and the governing workflow **declares** the reviewer via its `create_review`
frontmatter. Absent either — or if the declared skill does not resolve —
creation stays deterministic-only. This guide is the end-to-end recipe.

## 1. Author the reviewer rubric skill

Create `.satelle/skills/my-create-review.md`:

```markdown
---
name: my-create-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Create gate — judges a story draft is aligned with this repo's conventions before it is persisted.
---

# Story create review

You are an isolated reviewer judging whether a story DRAFT should be created.
You receive the draft (title, body, acceptance_criteria, category, tags) on
stdin. Judge alignment — the structural basics (title, goal, category) have
already passed deterministically. Acceptance criteria are not part of that check.

## Accept when

1. The story is one shippable slice (not several bundled mechanisms).
2. The body says what done looks like in this repo's terms.

## Reject when

Scope is bundled or the intent conflicts with an existing open story. On
reject, give a short, actionable list so the author can fix and resubmit.

## Verdict

Reply with exactly one JSON object, nothing else of that shape:

```json
{"decision": "accept", "notes": ""}
```

`decision` is `"accept"` or `"reject"`; `notes` is a brief actionable string
(may be empty on accept).
```

The three parts every reviewer skill needs: the frontmatter (`name` matching
the filename, `type: skill`, a `description`), the rubric body (accept/reject
criteria), and the verdict contract (the JSON object above — prose verdicts
like `Verdict: reject` are tolerated, but the JSON block is the contract).

## 2. Declare it on the governing workflow

Create review is one **lifecycle hook** — an operation that fires outside the
status graph, so it is not a step or an edge. Declare it on whatever governs the
story's category: `.satelle/workflows/done.toml` for a derived route (the half
that says what this repo means by finished, which is where a create gate
belongs), or `.satelle/workflows/<your-workflow>.md` for an authored graph.

**Shorthand on `done.toml`** — the skill only; the hook runs under the repo's
`[reviewer]`:

```toml
[meta]
name = "done"
type = "workflow"
scope = "project"
create_review = "my-create-review"   # <- the binding
```

**Shorthand on an authored graph** — same binding, YAML frontmatter spelling
(`create_review: my-create-review`):

```yaml
---
name: my-project-workflow
scope: project
type: workflow
applies_to: ["*"]
create_review: my-create-review   # <- the binding
---
```

**Full form** — declares the logical agent as well, so the allocation is a
choice you can read rather than a default you have to know about. On
`done.toml` that is a `[[meta.hooks]]` table; on an authored graph it is a
`hooks:` block:

```toml
[[meta.hooks]]
operation = "create_review"
skill     = "my-create-review"
agent     = "strict-reviewer"     # any role="reviewer" section in agents.toml
```

```yaml
---
name: my-project-workflow
scope: project
type: workflow
applies_to: ["*"]
hooks:
  - operation: create_review
    skill: my-create-review
    agent: strict-reviewer     # any role="reviewer" section in agents.toml
---
```

Both forms resolve to the same thing; `agent:` is optional and defaults to
`reviewer`. A hook declares **who** runs the skill, never **how** — model,
effort, command, transport and tool grant all stay in `.satelle/workflows/agents.toml`
(or a machine-wide profile it references). Keys like `model:` on a hook are
refused for exactly that reason.

Declaring the same operation both ways is an error: the `hooks:` /
`[[meta.hooks]]` entry wins and validation reports the duplicate.

The binding lives on the **workflow**, not in code or config-by-filename: the
draft's category selects the workflow, and that workflow names the reviewer.
Different categories can carry different create-reviews (or none).

## 3. Enable create-gating

In `.satelle/satelle.toml`:

```toml
[review]
gate_create = true
```

## Confirm it is wired

- Read the workflow frontmatter: `satelle doc get workflows my-project-workflow`
  should show `create_review: my-create-review`.
- Validate the skill: `satelle skill validate my-create-review` passes.
- Read the whole allocation: `satelle workflow show my-project-workflow` prints
  the hook with its skill, logical agent, the profile or local binding it
  resolves through, interface, model, effort, permission ceiling, and source
  files.
- Validate the binding: `satelle workflow validate` flags a hook whose skill
  does not resolve in the substrate, and `satelle agent validate` flags one
  whose agent has no binding, is not `role = "reviewer"`, or is `command =
  "in-loop"` (which cannot produce an isolated verdict). Both refuse **before**
  a story is ever created.
- Try it: `satelle story create --title … --body … --category …` now runs your
  reviewer after the structure check; a reject blocks creation and prints the
  reviewer's notes. `--acceptance "1. …"` is optional.

## What happens when it is NOT wired

This is a graceful degradation, not an error: no `gate_create`, no
`create_review` declaration, or an unresolved skill each mean `story create`
runs the deterministic structure check only. The `workflow validate` warning
above is how an *intended-but-broken* binding is surfaced.

Creating an **execution** (one run of a task, e.g. a task trigger or a remote
node's wake) always runs the structure check only: the task's intent was judged
when the task was authored, so the create-review never runs on its runs.

## Other lifecycle operations

`create_review` is the only lifecycle operation today. The `hooks:` grammar is
generic: a future operation is a new `operation:` value, declared the same way
and resolved through the same path. An operation this binary does not recognise
is reported by name — never silently ignored, and never granted verdict
authority.

See `satelle help workflows` for how hooks differ from transition gates,
deterministic structure checks, and agent judgments.
