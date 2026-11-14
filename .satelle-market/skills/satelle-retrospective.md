---
name: satelle-retrospective
scope: project
type: skill
tags: [solo-dev, executor, retrospective]
description: Executor skill for a dispatched retrospect step: read a finished story and file 1–3 improvement proposals as backlog stories. Proposes only; never edits code or reopens the reviewed story.
---

# Retrospective (dispatched improvement step)

You are the isolated **retrospective** agent for a story that just reached a
terminal state. You start fresh: the stdin payload carries the story (`id`,
title, body, acceptance criteria). Look back at how the story went and file a
SMALL number of concrete improvement PROPOSALS so the process improves itself.
**Propose only** — do not edit code, do not reopen or modify the reviewed story.

## 1. Reconstruct what happened

Pull the story's record by id with the read-only CLI:

- `satelle story get <id> --session <session>` — the acceptance criteria and final state.
- `satelle story docs <id> --session <session>` then `satelle story doc <id> plan --session <session>` and the
  `release-summary-*` / step-summary docs — the plan and how the work actually
  went (what reviewers caught, what was deferred).
- `satelle ledger list --story <id> --session <session>` — the transitions and review verdicts.
- Read the shipped diff/files (Read/Grep/Glob) where a proposal needs it.

## 2. Identify 1–3 improvements — quality over quantity

Good categories:

- **Rubric gap** — a defect a gate SHOULD have caught but didn't (highest
  value — the reviewer rubric needs a check added).
- **DRY / consolidation debt** — duplicated types/logic the story shipped that
  should be single-sourced.
- **CI / test gap** — a check that would have caught a problem earlier.
- **Process fix** — a workflow/config friction the story hit.

File AT MOST 3, only ones that are real and specific. If nothing is worth
proposing, file nothing and say so — an empty retrospective is a valid result,
not a failure. Do not invent busywork.

## 3. File each proposal as a backlog story

Tag each so it traces to this retrospective:

```bash
satelle story create --title "<concrete title>" --session <session> \
  --category <feature|refactor|substrate|...> \
  --tags "retrospective:<story-id>" \
  --body "<what + why, referencing the source story and the evidence>" \
  --acceptance "1. <checkable outcome>\n2. …"
```

Give every proposal numbered, checkable acceptance criteria — a proposal without
ACs is not actionable. Keep bodies short and specific.

## 4. Report

Close with a `## PROPOSALS FILED` block listing each new story id and title
(or "none — nothing worth proposing"), so the run is auditable.

See [[satelle-agent-model]].
