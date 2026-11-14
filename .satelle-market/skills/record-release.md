---
name: record-release
scope: project
type: skill
tags: [solo-dev, executor, release, evidence]
description: Executor skill that verifies release evidence (version bump, green CI, published tag) and attaches a PR-style implementation summary to the story. Verification-plus-recording is executor work; the done gate judges the recorded evidence.
---

# Record release (executor step)

You are the **executor** in the `committed` step. `push` has pushed the slice and watched CI; **verify the release evidence and record it** — the mutating half a reviewer must never do (see [[satelle-agent-model]]: executors mutate, reviewers judge). The `done` gate then judges what you recorded.

## 1. Verify the evidence

Confirm, and stop (don't advance) if any fails:

- **The bump**: `.version` carries the incremented `satelle.version` and a fresh `satelle.build` stamp, both in `HEAD` (`git show HEAD --stat`).
- **The commit convention**: a conventional-commit subject ending with the story id in parens; **no AI attribution** — inspect the actual trailers (`git log -1 --format='%(trailers)'`) and body for `Co-Authored-By` / "generated with" lines.
- **The CI runs**: the `test` run for the pushed SHA concluded success, and the version-gated `release` run published the tag — `gh release view "v$(awk '$1=="satelle.version:"{print $2}' .version)"`.

## 2. Record the summary WITH the story

Write a short PR-style summary (what shipped, why, the SHA, run URLs/conclusions, the published tag) to a temp file, then attach it via the CLI — the binary stores it on the home-keyed runtime plane (`~/.satelle/<repo-key>/stories/<story-id>/`), not under in-repo `.satelle/stories/` (<story-id>):

```bash
satelle story attach <story-id> \
  --name "commit-summary-<story-id>" \
  --type story-implementation-summary \
  --file /tmp/summary-<story-id>.md --session <session>
```

Do NOT write into `.satelle/documents/` — the old `story-implementation-summary` sub-bundle is retired; summaries belong with their story.

## Hand-off to the gate

You never enact your own status advance. The `done` gate (`satelle-story-done-review`) reads what you recorded — the attached summary (`satelle story docs <id> --session <session>`), the ledger, and the op-log — and judges the close.
