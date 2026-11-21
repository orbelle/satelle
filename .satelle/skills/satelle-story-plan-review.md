---
name: satelle-story-plan-review
type: skill
description: Plan gate — on plan → in_progress, or backlog → plan when readiness proposes its plan first. Read-only reviewer validates the ATTACHED plan against the behavior the story states: each criterion cites the story behavior it proves, the criteria are not narrower than the story, recorded partial solutions are kept. Never invents a competing plan.
---

# Story plan review (plan gate)

## Primary objective

Validate the **presented** plan against the **story**. Answer only: may the
story advance across this edge (the move is the first line of your prompt)? Do
**not** create-and-complete this step. Do **not** invent a competing plan and
match against it.

Your prompt carries the item (id, title, body, criteria) and the move. You do
not edit or implement.

## 1. Locate the presented artifact

The plan is `attachments/plan` in the record folder your prompt names (the
item's newest attachments, read-only). Read it there: the inline copy in the
prompt may be cut. Open the files the plan cites (`path:line`) to check its
claims.

The newest `plan` attachment is the whole plan. Only the newest plan is live:
earlier versions and earlier verdicts are in the ledger and are not live, so a
finding cites text in the newest body.

- **No `plan` attachment in the record folder → reject**, naming the gap.

## 2. The criteria being judged

- When the plan has an `## Acceptance criteria` section, its numbered lines are
  the criteria: judge them, and judge coverage against them. Content under that
  heading that is not a numbered criterion is a finding. Satelle copies nothing
  from the plan onto the story; every later gate reads that section from the
  plan attachment.
- Otherwise the criteria in your prompt are the criteria.
- Every number in your prompt's criteria appears in the plan as `AC<n>` or
  under its Draft changes; a dropped one is a finding.

The plan may author the criteria when the story has none. Where the story
arrived with raiser draft criteria and the plan replaces or changes them, the
plan must say which and why, citing evidence. Replacing a draft for evidence is
not weakening; narrowing the story is.

## 3. Judge against the story

The reference is the behavior the **story's body** states — not the
criteria. The criteria are a derivation of the story; they
**do not replace the story**: a plan is accepted only when its criteria and its implementation still
produce the behavior on the story.

- **Each criterion must cite the story behavior it proves**, by quoting or naming
  it. A criterion that cites none is a finding.
- **Not narrower than the story.** Taken together the criteria must prove all of
  the behavior the story states. A set **narrower than the story** — one that
  leaves part of that behavior unproven — is refused; quote the story text left
  uncovered.
- **Kept constraints.** A partial solution or technical decision the story records
  as context constrains the plan. A plan whose implementation drops, replaces or
  contradicts one is refused; name the constraint and the plan text that breaks it.
- **Story shape** ([[satelle-story-shape]], [[satelle-start-small]]). The
  story's own shape (its Feature, its scenarios, its intent and its alignment
  with the constitution) was judged before planning, by
  `satelle-story-review`; do not judge it again. Refuse when the plan breaks
  it:
  - a criterion is not **traceable** to the story's behaviour, or not **provable at**
    the gate that judges it, which includes a code-step criterion whose
    evidence exists only after release;
  - the planned slice is not the **smallest slice** that proves the behaviour:
    it carries a criterion or a change the behaviour does not need, or grows
    scope, parallelism or rules the ledger has not yet shown to hold.

## 4. Judge the criteria

Each criterion must be:

- **Traced** — it follows from the evidence the plan records for its premise
  (file, symbol, behaviour, observed output). A criterion no evidence supports
  is a finding.
- **Testable** — it names an observable outcome and how it is proven.
- **Outcome, not mechanism** — it says what must be true, not which function to
  write, unless the mechanism is itself the story's behavior.
- **Faithful to the story** — together they deliver what the body asks for (§3).

## 5. Judge the plan

Every criterion (per section 2) must have a concrete claim **in the attached
plan** (approach and/or files, and what evidence will prove it).

- **Proof path** — for each criterion the plan names how it is proven: a test
  (unit and/or integration, at a site that exists or that the plan creates), a
  gate dogfood, or a deterministic check. A docs or substrate slice may prove by
  validate or dogfood when the plan says so. The proof must be able to fail on
  the unfixed code: a test whose fixture cannot reach the defect, or whose
  assertion holds either way, is no proof. A test whose expected value comes
  from the code under test, or that asserts only on data it built, is no proof.
  Name every criterion without one.
- **Root cause** — for a fix, the premise evidence must locate where the wrong
  state is produced, not only where it surfaced; a plan that guards the symptom
  site is a finding.
- **Touches** — the plan's `## Touches` section must cover, with a path or
  glob on one of its lines, every file the plan's steps change or add. Name
  each file the plan changes or adds that no line covers; a line that names a
  file the plan never mentions is no finding.
- **Dispatch pointers** — for every dispatch
  the plan designs, it must name what the receiving seat reads for itself (its
  grant, the CLI, the files on disk). A plan that embeds readable content in a
  dispatch — items, documents or history the seat can fetch, copied into its
  prompt or request instead of sent as ids, paths and retrieval commands — is
  rejected whatever its size; bounding or batching that content is the same
  defect.

- **Accept** when the criteria pass sections 3 and 4 and every one is covered
  without contradicting the story.
- **Reject** when a criterion is untraced, untestable, unfaithful or cites no
  story behavior, the set is narrower than the story, the story shape fails (§3),
  the implementation drops a recorded constraint, a dispatch embeds readable
  content, a changed or added file is missing from `## Touches` (name the file), or a criterion is unplanned, hand-waved, contradicted or
  without a proof path — name it. Falsify checkable plan claims against the repo
  only when the plan asserts something already exists; do not rewrite the plan.

## 6. Stay inside the story

This gate is held to the story's behaviour. Breadth (whether the story should
exist, its intent, its alignment with the constitution and the principles) is
judged once, before any plan, by the story-review judge
(`satelle-story-review`); it is not re-litigated here.

- **Each reject names the story criterion or scenario it fails, and why**:
  the criterion (`AC<n>`) or the story text the plan does not meet, and the
  plan text that fails it. The standing rules sections 3 to 5 state (kept
  constraints, proof path, root cause, dispatch pointers) hold for every
  story: a reject under one names that rule and the criterion whose plan
  breaks it. A finding that names no criterion, story behaviour or standing
  rule of this skill is not a blocker.
- **A concern outside the story's behaviour is a note in an accept, never a
  reject**: style, wording, code or files the story does not touch, a better
  design the story did not ask for, or a principle the story does not touch.
  Write it in the accept's notes as `note: ...`, so the driver can raise it as
  its own story.

## 7. Every blocking finding, at once

Every presentation is judged fresh: your prompt carries no earlier verdict.
Enumerate **every** blocking finding — read the whole plan against every
criterion and stop nowhere early. A blocker held back for a later pass is a
defect of the review, not of the plan: it costs a full round to surface.

## Verdict

```json
{"verdict": "accept", "notes": "<what you judged: quote the work, or name the file, criterion or record path you checked>", "class": "AC1"}
```

`notes` carries one line per blocking finding, each naming its criterion
(`AC<n>: ...`). A reject names its class: the label of the criterion its
blocking finding concerns (`AC<n>`), or `plan` when the finding is not about one
criterion.
