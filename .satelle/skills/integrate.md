---
name: integrate
scope: project
type: skill
tags: [type:skill]
evidence_commands: go vet
description: In-loop executor skill for the `integration` step: leave the slice gofmt/vet clean and repair only trivial fallout; the unit suite is proven by the gate entering this step and the integration suite by the gate leaving it, so this step runs neither. Never commits, pushes, bumps the version, or advances status.
---

# Integrate (in-loop executor step)

The code-ac gate accepted the slice; leave it formatted and vetted for `release`.

## What to do

1. **Format and vet.**
   ```bash
   gofmt -l internal cmd tests   # expect no output; run gofmt -s -w on offenders
   go vet ./...
   ```
2. **Run neither suite:** satelle-build-unit-check ran the unit suite on entry and satelle-integration-check runs the integration suite on exit.
3. **Repair trivial fallout only.** When you are handed a rejection from the gate on the way out, fix formatting, a stale test fixture, an import or a missed call site of the story's own change, and report what you repaired; the driving session presents the edge again and the gate re-runs its suite. Anything beyond trivial (a design flaw, a failing behaviour the story itself introduced) is NOT yours to redesign: leave the tree as-is and report the failure clearly so the orchestrator sees it — the integration→release gate holds the line.

## What you must NOT do

- Do **not** commit, push, or touch `.version` — the `release` step owns that.
- Do **not** change the story's scope or acceptance criteria.
- Do **not** advance the item's status — the workflow's gates govern every move.

## Hand-off

Report what you ran, repaired and what still fails.
