---
name: commit
scope: project
type: skill
tags: [solo-dev, executor, commit, release]
description: Executor skill for a dedicated commit step. Stages the slice, bumps the version file (example: .version) and stamps the build date, then makes a conventional commit ending in the story id with no AI attribution. Does not push.
---

# Commit (executor step)

You are the **executor** in the `commit` step. The slice is built and prior gates accepted it; **stage it, bump the version, and commit** — leave the push to the next step. You DO the work (see [[satelle-agent-model]]: executor mutates, reviewer only judges). You do **not** push here.

## Why the bump is mandatory

`.version` is the **single source of truth** for the release tag (`v<satelle.version>`) and the build identity baked into the binary. `release` cuts a tag **only when `.version` changed** — a commit that doesn't bump leaves the released binary (what `satelle update --session <session>` serves) stale (binary-drift trap). **Every** commit on this step bumps the patch version and stamps the build date.

## What to do

1. **Stage and format.** Format Go, then stage the STORY'S SLICE — the files this story changed (read the story body/acceptance criteria on stdin and `git status --short`):
   ```bash
   gofmt -s -w internal/ cmd/ 2>/dev/null; git add <the story's files>
   ```
   Do NOT `git add -A`: the tree may carry ANOTHER session's in-flight changes — sweeping an untouched file into the commit is a defect. Confirm the staged set is exactly the slice (`git diff --cached --stat`) and anything left unstaged genuinely belongs elsewhere.
2. **Bump `.version`** — MANDATORY. Carries one canonical version plus a build date:
   ```
   satelle.version: <x.y.z>
   satelle.build:   <UTC>
   ```
   - Increment the **patch** of `satelle.version` (`0.0.11` → `0.0.12`).
   - Set `satelle.build` to `date -u +"%Y-%m-%d-%H-%M-%S"`.
   - `git add .version`.
   Release tag will be `v<satelle.version>`; a missed bump means no tag is cut.
3. **Commit.** A **conventional commit** subject ending with the story id in parens, e.g. `feat(web): add the X view (<story-id>)`. **No AI attribution** — no `Co-Authored-By`, no "generated with" trailer (the adopting repo's convention). Verify the commit captured every intended file (`git show --stat HEAD`) — a partial commit is a defect.
4. **Do NOT push.** `push` pushes to `main` and watches CI. Leave `HEAD` committed locally with the bumped `.version`.

## Hand-off to the next step

`push` pushes `HEAD`, watches the `test` run, then the version-gated `release` run, and confirms the tag/assets. `record-release` then verifies the bump, green CI, and the published release. You never enact your own status advance — the workflow's gates do that (see [[satelle-agent-model]]).
