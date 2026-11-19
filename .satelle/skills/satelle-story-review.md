---
name: satelle-story-review
type: skill
description: Story gate on a story's first move out of backlog (delivery backlog → plan, chore backlog → in_progress), before any plan exists. Isolated read-only reviewer critical of the card itself - a Feature with a Background, each criterion a Given/When/Then scenario, one behaviour small enough for one plan, achievable and provable at its gates, aligned with the constitution. A refusal names each failed property and its fix - split, narrow, rewrite as scenarios, or cancel as misaligned. Verdict only.
---

# Story review (the card, before any plan)

You judge the **card**: the story as raised, before anyone plans or codes it.
Your prompt carries the item (id, title, body, criteria) and the move, and
names the read-only record folder holding its attachments
(`attachments/<name>`); there is no plan yet, and you design none. You may
Read, Grep and Glob the repo to check the card's claims and its alignment.

This is the review with breadth. The later judges (plan review, code review)
are held to the card's behaviour; whether the story should exist at all, what
it intends, and whether it fits the constitution are judged **here**, once,
before work starts.

The shape is [[satelle-story-shape]]; read it. Then judge each property below
and enumerate every failure in one pass: a finding held back costs a round.

## 1. A Feature with a Background

The body states one **Feature** in the form *As a* (a role), *I want* (a
capability), *so that* (an outcome the role gets), and a **Background**: the
context the scenarios share (what exists now, the evidence that raised the
story, a constraint or decision already made). A card that names only a
mechanism, a file or a list of tasks has no Feature. A card with no
Background leaves its scenarios unanchored.

## 2. Each criterion a scenario

Every acceptance criterion is a scenario: **Given** a context, **When** an
action or event, **Then** an observable outcome (with **And** for more of
the same). A criterion that is a task ("add X", "update Y"), a mechanism
("call Z") or an untestable quality ("robust", "clear") is not a scenario.
The scenarios together meet the Feature, no narrower and no wider.

## 3. One behaviour, small enough for one plan

The card is one behaviour ([[satelle-start-small]]). Refuse a card that holds
two behaviours (two Features, or scenarios that serve different outcomes), or
one too large for a single plan to slice and prove. Name the split.

## 4. Achievable and provable at its gates

Each scenario's Then can be judged at the gate that will judge it: a test, a
check's script or a reviewer's evidence at that step
(`satelle workflow show <workflow> --session <session>` names the gates). An
outcome that exists only after release is the release step's, not a code-step
scenario. A scenario no wired gate can judge is narrowed until one can, or
removed. A card that depends on something not built yet names that
dependency.

## 5. Aligned with the constitution

Read `.satelle/constitution.md` and [[satelle-mechanism-vs-substrate]]. satelle
is a neutral kernel: the binary is mechanism, and process, gates and opinions
are substrate under `.satelle/`. Refuse a card that asks the binary to know
this repo's (or any one user's) process, workflow, gate, skill, vendor or
rule, or that moves process into Go; refuse a card whose Feature works against
a principle. When the card's intent cannot be met without that, its fix is to
cancel it as misaligned, or re-raise it as a substrate change.

## Refuse with the fix

A reject names **each** failed property (1 to 5) with the card text that
fails it, and its fix:

- **split**: two behaviours; name the two stories;
- **narrow**: too large, or a scenario no gate can judge; name what to cut;
- **rewrite as scenarios**: a missing Feature or Background, or a criterion
  that is not Given/When/Then; show the shape for one of them;
- **cancel as misaligned**: the constitution or a principle forbids the
  intent; name the principle.

Judge the card as written; do not add requirements it never stated.

## Verdict

```json
{"verdict": "reject", "notes": "criterion 2: \"update the gate list\" is a task, not a scenario: rewrite as scenarios, Given a delivery story in backlog, When it is presented to plan, Then ...", "class": "scenarios"}
```

`notes` carries one line per finding, each naming the property and quoting the
card. An accept quotes the Feature and names each scenario it checked. A
reject names its class: the property its first finding concerns (`feature`,
`scenarios`, `size`, `provable` or `alignment`).
