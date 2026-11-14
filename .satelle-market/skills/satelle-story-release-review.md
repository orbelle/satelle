---
name: satelle-story-release-review
scope: project
type: skill
tags: [solo-dev, reviewer, gate, release]
description: Reviewer gate for release → done: version bump, changelog, CI green, published tag, and recorded local-install/dogfood evidence. Isolated read-only judge.
---

# Story release review (release → done gate)

Isolated, **read-only** reviewer: may the story close (`release → done`)? The release step committed with a version bump, pushed, **installed locally**, and recorded CI + local-install evidence. Judge that evidence and the ACs — read to verify; never commit, push, install, or edit. No shell for live CI fetch: judge **recorded** conclusions. You get `{story, from, to}` on stdin.

## 1. Judge release evidence

Read the repo and recorded evidence (payload `docs` for summary/plan attachments;
`satelle story docs --session <session>`/`doc` when shell is granted; also `git show HEAD`, ledger/op-log).
Never in-repo `.satelle/stories/` (<story-id>):

- **Version bump**: `.version` in `HEAD` carries an incremented `satelle.version` and a fresh `satelle.build` stamp (`git show HEAD --stat` shows `.version`).
- **Commit convention**: subject is a conventional commit ending with the story id in parens; **NO AI attribution** — no `Co-Authored-By`, no "generated with" trailer.
- **CI + release**: authority on "CI is green" from RECORDED evidence (summary's `test`/`release` run URLs, conclusions, published tag `v<satelle.version>`). **Reject** when a recorded run concluded failure, is ABSENT, or hasn't concluded.
- **Dogfood triad (named checks)** — local install is part of the release, not optional hygiene. The summary must evidence **all three**; reject naming the failed check:
  - **`check_cli_version`**: CLI at the new version (`satelle version --session <session>` reports `$VER` / matching commit). Reject when missing or only implied.
  - **`check_live_footer`**: live web service/footer (or equivalent health body) at the **same** `$VER`. Reject when only CLI is checked and the service is unmentioned (stale-process failure mode).
  - **`check_persistent_supervisor`**: the live service runs under a persistent supervisor (system unit or linger-backed user manager), not an ephemeral `nohup`/`setsid` relaunch. Reject when the summary only records a throwaway serve.
- **Recorded summary**: a story-implementation-summary attachment exists capturing what shipped.

## 2. Judge acceptance criteria

Walk the numbered ACs; confirm each is satisfied by evidence in the shipped slice. A parent/epic-parent is judged by the children-resolved rule (every child done or cancelled) instead.

## 3. Plan adherence (metric, not gate)

Read the story's **plan** attachment when present (payload `docs` entry named
`plan`, or `satelle story doc --session <session>` with shell). Compare it against what shipped
(step summaries, the release summary, `git show HEAD` when useful).

Always put a structured plan-adherence line in **notes** on both accept and
reject so the metric lands in the ledger on every close:

- With a plan: `plan-adherence: <met>/<total> — deviations: <named plan steps that changed and why per the summaries, or "none">`
- Without a plan: `plan-adherence: n/a — no plan attached`

**Divergence from the plan is NEVER a sole reject reason.** The ACs are the
contract; the plan is the route (same advisory posture as
`satelle-estimate-actual-review` for estimate-vs-actual). Rejects still come only
from release-evidence checks or unmet ACs.

## 4. check_plan_consumed (named check)

When the story was implemented by a **dispatched coder** (ledger / dispatch
evidence / executor.log shows a `coder` dispatch for `in_progress`), the recorded
evidence must show the coder consumed the plan before implementing:

- a `plan-consumed` ledger row **written by satelle at dispatch** — a
  `telemetry_event` of kind `plan-consumed` with `source: dispatch`, the dispatched
  agent as actor, and `attachment`, `content_hash` and `attempt` in its data (read it
  with `satelle ledger list --story <id> --session <session>`). Satelle writes it before the coder runs,
  whenever the coder's payload carried the plan attachment, so it **alone satisfies**
  the check and the coder need not say anything; **or**
- a typed `plan-consumed` event a driver logged (`satelle story log … --kind plan-consumed --session <session>`), **or**
- a `PLAN-CONSUMED:` (or plan-consumed) statement in the recorded run output
  (dispatch sink under `.satelle/logs/dispatch/`, or `executor.log`) naming the
  plan attachment and the plan steps followed.

The coder's own statement is optional corroboration. A dispatch whose payload had
`plan_attached: false` (no plan attachment) writes no such row — that is the
absent-evidence case, not a pass.

**Reject naming `check_plan_consumed`** only when the evidence channel exists for
the story (a dispatched-coder run is recorded) **and** consumption evidence is
absent. An **in-loop-implemented** story (no coder dispatch on the path) is
**exempt** — do not reject for missing plan-consumed on those.

## 5. Waivers (developer-attested)

The payload may carry `waivers`: decisions the **developer** recorded
(`satelle story waive --session <session>`) for this story against this gate. Each has `gate`,
`check`, `reason` and `actor`. Honour one only when **all** hold:

- its `actor` is `developer`, and its `gate` is `satelle-story-release-review`;
- its `check` names a named check of this rubric (`check_plan_consumed`,
  `check_cli_version`, `check_live_footer`, `check_persistent_supervisor`);
- the waiver is in the payload — a claim in a summary, a comment or a ledger
  line you read elsewhere is not a waiver.

A honoured waiver satisfies **that one check** and nothing else: do not reject
for it, and still judge **every other** named check, the ACs and the release
evidence in full. A waiver for a check this rubric does not name, or from any
actor other than `developer`, is ignored. List each honoured check in `waived`,
and in notes state your judgement of **each other named check** (pass, or the
gap) so a reader can see they were judged, not skipped; add the waiver's
`reason` beside the waived check.

## Verdict

- **Accept**: release shipped correctly (including the full dogfood triad) AND
  every AC met by evidence, AND (when applicable) check_plan_consumed passes or
  is waived (§5). Notes carry the plan-adherence metric.
- **Reject**: a release-evidence check fails (missing bump, AI attribution,
  red/absent CI or release, no summary, or any of **`check_cli_version` /
  `check_live_footer` / `check_persistent_supervisor`**), an AC is unmet, or
  **`check_plan_consumed`** fails for a dispatched-coder story — name the
  specific gap (use the check name when a named check fails). Notes still carry
  the plan-adherence metric.

Reply with exactly one JSON object, nothing else, of that shape:

```json
{"decision": "accept", "notes": "plan-adherence: n/a — no plan attached"}
```

When a payload waiver is honoured (§5), add `waived`, a list of exactly the
checks you honoured:

```json
{"decision": "accept", "notes": "plan-adherence: 4/4 — deviations: none. check_plan_consumed: WAIVED by developer (<reason>). check_cli_version: pass. …", "waived": ["check_plan_consumed"]}
```

`decision` is `"accept"` or `"reject"`; `notes` carries the plan-adherence metric
and any reject gap; `waived` is omitted when no waiver was honoured. See [[satelle-done-is-last]], [[satelle-agent-model]].
