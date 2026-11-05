---
name: satelle-story-plan-review
scope: system
type: skill
tags: [type:skill, type:reviewer]
description: Plan gate — on plan → in_progress, or backlog → plan when readiness proposes its plan first. Read-only reviewer validates the ATTACHED plan against the behavior the story card states: each criterion cites the card behavior it proves, the criteria are not narrower than the card, recorded partial solutions are kept. Never invents a competing plan; re-presented edge follows [[satelle-reviewer-verdict]].
payload_citations: plan
---

# Story plan review (plan gate)

## Primary objective

Validate the **presented** plan against the **story card**. Answer only: may the
story advance across this edge (`from` → `to` in the payload)? Do **not**
create-and-complete this step. Do **not** invent a competing plan and match
against it.

You get `{story, from, to}` on stdin. You do not edit or implement.

## 1. Locate the presented artifact

Plan is the `docs` entry named `plan` (`name`, `type`, `path`). The file at `path` is the plan body. Prefer that — it is how a Bash-less reviewer
reads what it judges. When `truncated: true`, or for a fuller
pull when shell is granted: `satelle story doc <sty_id> plan`.

The payload's `citations` block holds each `path:line` the plan cites, already
read from the tree you judge (the lines plus three either side, or why one no
longer resolves). Start from those excerpts; open files only to check beyond them.

Do **not** look under in-repo `.satelle/stories/` (obsolete post-relocation).

- **No plan artifact → reject**.

## 2. The criteria being judged

- When the payload carries `criteria_source` (the binary supplies it, with
  `criteria_state`, when the step applies the plan's criteria to the story),
  the criteria are the ones that field names. Judge them, and judge coverage
  against them. Content under that heading that is not a numbered criterion
  would be applied as a criterion — a finding.
- Otherwise the story's own numbered criteria are the criteria.

The plan may author the criteria when the story has none. Where the story
arrived with raiser draft criteria and the plan replaces or changes them, the
plan must say which and why, citing evidence. Replacing a draft for evidence is
not weakening; narrowing the card is.

## 3. Judge against the card

The reference is the behavior the **story card's body** states — not the
criteria. The criteria are a derivation of the card; they
**do not replace the card**: a plan is accepted only when its criteria and its implementation still
produce the behavior on the card.

- **Each criterion must cite the card behavior it proves**, by quoting or naming
  it. A criterion that cites none is a finding.
- **Not narrower than the card.** Taken together the criteria must prove all of
  the behavior the card states. A set **narrower than the card** — one that
  leaves part of that behavior unproven — is refused; quote the card text left
  uncovered.
- **Kept constraints.** A partial solution or technical decision the card records
  as context constrains the plan. A plan whose implementation drops, replaces or
  contradicts one is refused; name the constraint and the plan text that breaks it.
- **Story shape** ([[satelle-story-shape]], [[satelle-start-small]]). Refuse
  when:
  - the card is **not a behaviour**: it names no role, context and outcome,
    only a mechanism, a file or a list of tasks;
  - a criterion is not **traceable** to that behaviour, or not **provable at**
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
  write, unless the mechanism is itself the card's behavior.
- **Faithful to the card** — together they deliver what the body asks for (§3).

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
- **Dispatch pointers** ([[satelle-dispatch-pointers]]) — for every dispatch
  the plan designs, it must name what the receiving seat reads for itself (its
  grant, the CLI, the files on disk). A plan that embeds readable content in a
  dispatch — items, documents or history the seat can fetch, copied into its
  prompt or payload instead of sent as ids, paths and retrieval commands — is
  rejected whatever its size; bounding or batching that content is the same
  defect.

- **Accept** when the criteria pass sections 3 and 4 and every one is covered
  without contradicting the card.
- **Reject** when a criterion is untraced, untestable, unfaithful or cites no
  card behavior, the set is narrower than the card, the story shape fails (§3),
  the implementation drops a recorded constraint, a dispatch embeds readable
  content, or a criterion is unplanned, hand-waved, contradicted or
  without a proof path — name it. Falsify checkable plan claims against the repo
  only when the plan asserts something already exists; do not rewrite the plan.

## 6. First pass: every blocking finding, once

On the **first** presentation of this edge (the payload has no
`prior_verdicts`), enumerate **every** blocking finding — read the whole plan
against every criterion and stop nowhere early. A blocker held back for a later
pass is a defect of the review, not of the plan: it costs a full round to
surface.

**Fail-fast mode.** When the payload's `review_mode` is `"fail-fast"`, this
section does not apply; [[satelle-reviewer-verdict]] governs the stop and
`coverage`.

## Verdict

```json
{"decision": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>", "criteria": [{"criterion": 1, "status": "accept", "finding": "", "reviewed": "<exact words this entry rests on>"}]}
```

Return one `criteria` entry per acceptance criterion you judged
([[satelle-reviewer-verdict]]), so an accepted criterion whose text is unchanged
settles on the next presentation instead of being judged again.
