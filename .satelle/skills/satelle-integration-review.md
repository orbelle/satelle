---
name: satelle-integration-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Gate on outcome → release. Isolated read-only reviewer validates PRESENTED integration tests against the story ACs/change (adequacy, not suite execution). satelle-integration-check runs the suite separately.
---

# Integration test review (outcome → release)

## Primary objective

Validate the **presented** integration tests against the **story** and the
change. Answer only: may we advance to `release` (from a test-adequacy view)?
Do **not** write tests. Do **not** run the suite (`satelle-integration-check`
does that). Do **not** invent a better test plan as the standard.

You get `{story, from, to}` on stdin. Read-only (especially `tests/`).

## Judge

1. **Locate presented tests** for this story's change (new/updated under
   `tests/` or co-located tests clearly for the slice).
2. For each **code/behavioural** AC path: do those tests drive the behaviour
   end-to-end and **assert** an outcome that would fail if the change
   regressed?
3. **Accept** when presented tests adequately cover the AC paths — **or** the
   change is docs/substrate/config with no integration behaviour to test.
4. **Reject** when a behavioural change has missing, trivial (`assert true`,
   empty, no-error-only), or unrelated tests — name the AC/behaviour gap. A
   self-confirming test (expected value from the code under test, or asserting
   only on data the test built) counts as trivial.

Fair gate: coverage of stated ACs, not maximality or a private ideal suite.

**Every criterion gets a verdict.** An unchecked criterion never accepts, so
never record one unchecked because it is off this gate's test-adequacy axis. A
criterion outside test adequacy (build or suite pass, a CHANGELOG entry, any
other non-behavioural line) is judged on the presented change and the story's
recorded evidence: accept it when they show it (the ac-evidence attachment, the
accepted `satelle-build-unit-check` on entry to integration, the CHANGELOG lines
in the change); reject it, naming the gap, when they show it unmet. You still do
not run the suite to judge it.

## Verdict

```json
{"verdict": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>"}
```

