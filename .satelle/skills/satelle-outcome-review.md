---
name: satelle-outcome-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Gate on integration → outcome, after satelle-integration-check has run the integration suite (fail fast). Isolated read-only reviewer judges, per numbered AC, that the integrated behaviour meets the story's BDD and ACs, citing integration-level evidence, and rejects an AC with no integration-level proof. Comes before release, or before the PR on the pr route. Verdict only; never runs the suite.
---

# Outcome review (integration → outcome)

## Primary objective

The code review judged the code against the ACs and the unit tests passed on
the way into integration. The integration suite has now passed on this edge
(`satelle-integration-check` runs first and fails fast). Answer one question:
**does the integrated behaviour actually achieve the story's BDD and each
numbered AC?** Judge the outcome, not the code shape.

Do **not** run the suite, write tests, or design a better test plan. Your
prompt carries the item, the move, the changed paths and the diff since the
item's base. Read-only.

## Evidence

- The story: title, body (its behaviour, the BDD), numbered ACs. The numbered
  ACs are the `## Acceptance criteria` section of the `plan` attachment in the
  record folder your prompt names when the plan has that section, else the
  criteria in your prompt.
- The integration evidence: the integration suite's result on this edge, the
  presented integration tests for this change (under `tests/` or co-located
  end-to-end tests), and the item's attachments such as `ac-evidence`, read
  from the record folder (`attachments/<name>`).
- The changed paths and diff in your prompt only to locate which tests and
  paths realise each AC.

## Judge, per numbered AC

1. **Find its integration-level proof.** A test that drives the behaviour end
   to end through the real entry point (the CLI, the service, the route) and
   asserts the outcome the AC states. A unit test of an internal function is
   not integration-level proof of a behavioural AC.
2. **Check it proves the outcome.** It must assert what the AC promises would
   be observed, and it must fail if the behaviour were absent. A test that only
   asserts no error, asserts on data it built itself, or computes its expected
   value with the code under test proves nothing.
3. **Check the BDD.** The integrated behaviour matches the story body's stated
   behaviour, not a narrower reading of one AC.
4. **Non-behavioural ACs** (a CHANGELOG line, a build passing, a docs change,
   substrate with no runtime behaviour) are judged on the presented change and
   recorded evidence; they need no integration test.
5. **Live or post-release evidence** an AC names for the release step is not
   judged here; say so for that AC.

**Accept** only when every behavioural AC has integration-level proof that the
outcome holds. **Reject** naming each AC that has none, or whose proof does not
show the stated outcome, and what proof is missing.

Every criterion gets a verdict; an unchecked criterion never accepts.

## Verdict

```json
{"verdict": "accept", "notes": "<what you judged: quote the work, or name the file, criterion or record path you checked>"}
```
