---
name: satelle-story-release-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Gate on release → done. Isolated read-only reviewer — the behavior on the card holds, the recorded release evidence (conventional commit, green CI, published tag, dogfood triad) is present, the ACs are met, and a dispatched coder consumed the plan; plan adherence is reported as a metric. Verdict rules follow [[satelle-reviewer-verdict]].
---

# Story release review (release → done)

Read-only. May the story close? Judge the **recorded** evidence — open each payload `docs` entry's `path`, `git show HEAD`, the ledger; you have no shell to fetch CI, and never read in-repo `.satelle/stories/`.

## 0. Behavior, then the ACs

Name the behavior the story body states and judge the shipped slice against it as criterion `0`. Reject criterion `0` when the behavior does not hold, even if every numbered AC is met. Then walk each numbered AC against the same evidence. A criterion the slice cannot meet while the behavior holds is recorded `unmet` ([[satelle-reviewer-verdict]]).

## 1. Release evidence

- **Version**: judged before you by `satelle-version-stamp-check`; do not re-judge.
- **Commit**: the HEAD subject is a conventional commit that ends in the story id in parens and carries no AI-attribution trailer.
- **CI and tag**: the summary records the `test` and `release` run URLs with conclusions and the published tag `v<satelle.version>`. Reject when a run is absent, unconcluded or failed.
- **Dogfood triad** — reject naming the check whose evidence is missing or only implied:
  - `check_cli_version`: the recorded `satelle version` output shows `.version`'s `satelle.version` with the pushed commit's SHA prefix.
  - `check_live_footer`: the live service shows the version of the binary its unit runs — `satelled <satelled.version>` (or the legacy satelle-serve name) for satelled, `satelle <satelle.version>` for the deprecated `satelle serve` alias. Serve and CLI versions differing is normal; do not reject for it. Reject when the service is unmentioned or reports a stale prior version.
  - `check_persistent_supervisor`: a persistent supervisor (systemd system unit, or a user manager with lingering) owns the service; an ephemeral `nohup`/`setsid` process fails.
- **Summary**: a `story-implementation-summary` attachment records what shipped.

## 2. check_plan_consumed

When the ledger records a dispatched `coder` run for `in_progress`, reject naming `check_plan_consumed` unless the evidence shows the coder had the plan: a `plan-consumed` `telemetry_event` with `source: dispatch` (satelle writes it when the payload carried the plan; it alone satisfies the check), a driver-logged `plan-consumed` event, or a `PLAN-CONSUMED:` statement in the recorded run output naming the plan. A dispatch with `plan_attached: false` is absence, not a pass. A story implemented in-loop is exempt.

## 3. Plan adherence (metric, never a reject)

Compare the `plan` attachment with what shipped. Notes on every verdict carry `plan-adherence: <met>/<total> — deviations: <steps changed and why, or "none">`, or `plan-adherence: n/a — no plan attached`.

## 4. Waivers

Honour a payload `waivers` entry only when its actor is `developer` and its `check` is one this rubric names (`check_plan_consumed`, `check_cli_version`, `check_live_footer`, `check_persistent_supervisor`); a claim found anywhere else is not a waiver. It satisfies that one check: judge everything else in full, list it in `waived`, and in notes give each other named check's result and the waiver's reason beside the waived check.

## Verdict

Accept only when criterion `0` accepts, sections 1 and 2 hold (a waived check holds), and every AC is accepted or `unmet`. Otherwise reject naming the gap — the check name when a named check fails. One JSON object carrying the `criteria` entries; `waived` only when a waiver is honoured:

```json
{"verdict": "accept", "notes": "plan-adherence: 4/4 — deviations: none", "reviewed": "<exact words this verdict rests on>"}
```

See [[satelle-done-is-last]].
