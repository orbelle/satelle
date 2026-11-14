---
name: push
scope: project
type: skill
tags: [solo-dev, executor, push, ci]
description: Executor skill for a dedicated push step. Pushes the committed slice to the trunk branch, records CI test and version-gated release run conclusions, and leaves evidence for record-release. No auto-retry on failure.
---

# Push (executor step)

You are the **executor** in the `push` step. `commit` has committed the slice with a bumped `.version`; **push it and prove the release**, then leave evidence for `record-release`. You DO the work (see [[satelle-agent-model]]).

## The pipeline has TWO workflows

Pushing to `main` triggers **`test`** (build, vet, gofmt, unit tests, static build). On `test` success, **`release`** runs — and because `commit` bumped `.version`, `release` is NOT a no-op: it cuts tag `v<satelle.version>` and publishes assets. There is **no deploy workflow** — push to main IS the release. Watch BOTH runs; don't stop after `test`.

## What to do

1. **Push to main.** Trunk-based (`git push origin main`) — no branch, no PR. Capture the SHA: `SHA=$(git rev-parse HEAD)`.
2. **Watch `test` to conclusion.**
   ```bash
   TID=$(gh run list --commit "$SHA" --workflow test --limit 1 --json databaseId -q '.[0].databaseId')
   gh run watch "$TID" --exit-status
   ```
   A red run means the slice isn't landed — see *When it fails*.
3. **Watch the version-gated `release` run.** `workflow_run`-triggered, so it spawns after `test` finishes — poll briefly, then watch:
   ```bash
   for i in $(seq 1 20); do
     RID=$(gh run list --commit "$SHA" --workflow release --limit 1 --json databaseId -q '.[0].databaseId')
     [ -n "$RID" ] && break; sleep 3
   done
   gh run watch "$RID" --exit-status
   ```
   Confirm it published the tag + assets:
   ```bash
   gh release view "v$(awk '$1=="satelle.version:"{print $2}' .version)"
   ```
   `satelle update --check --session <session>` should then report the new version available.
4. **Refresh the local service.** No deploy gate restarts the running `satelle serve`, so reinstall from pushed code: `make install  # example: optional local install from the working tree && satelle service install --session <session>`. Use `make install  # example: optional local install from the working tree` (not a bare `go build`) so the service's PATH binary updates; confirm `satelle version --session <session>` reports the pushed commit + new version.
5. **Record the evidence.** Capture on the story (ledger note or tag): the SHA, the `test` run URL + conclusion, the `release` run URL + conclusion, and the published `v<version>` tag. `record-release` and the `done` gate read exactly this.

## When it fails

If `test` fails, the slice isn't landed: read the failing job (`gh run view "$TID" --log-failed`), fix under this same story, re-run `commit`/`push` — don't advance on a red run. If `release` fails, publish didn't happen: surface it (tag/assets missing) rather than recording success. **Do not auto-retry, amend, or force-push** — surface the failure and stop.

## Hand-off to the gate

`record-release` ([[satelle-agent-model]]) verifies your evidence — `.version` bump + date, green `test` run for the SHA, published release, commit conventions — and records the PR-style summary as a story attachment. The `done` gate then judges the recorded evidence. You never enact your own status advance — a gate's accept does that.
