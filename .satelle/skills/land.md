---
name: land
scope: project
type: skill
tags: [type:skill]
description: In-loop executor skill for the chore lane's `landing` step: commit and push only the story's tracked files to main, with no .version bump and no changelog entry, or record that there is nothing to land. Never cuts a release, tag or dogfood install, and never self-advances status.
---

# Land (in-loop executor step, chore lane)

You are the **executor** for the `landing` step of a `chore`.
Integration already proved the slice builds and passes. A chore ships nothing,
so there is **no release**: no `.version` bump, no `CHANGELOG.md` entry, no tag,
no dogfood install. Your only job is to get the story's tracked changes onto
main, or to record that it has none.

## Do

1. **Enumerate the slice:** `satelle story diff <id>`. Drop paths git
   ignores (`git check-ignore -q <path>` exits 0); a deleted gitignored file is
   housekeeping, not a change to land.
2. **Nothing tracked left:** record it —
   `echo outcome=nothing-to-land | satelle story attach <id> --name land --type text/plain --file -` — and
   request `done`.
3. **Tracked files changed:** stage exactly those paths (never `git add -A`),
   commit with a conventional subject ending in the story id and no AI
   attribution, then publish onto main the way release.md §2 does, never a
   bare `git push`.
   Do not touch `.version` or `CHANGELOG.md`.
4. **Record CI, do not watch it:** find the `test` run for the pushed head with
   `gh run list --commit <sha> --workflow test` and record it —
   `printf 'commit=<sha>\nci_run=<id>\n' | satelle story attach <id> --name land --type text/plain --file -` —
   then request `done`. If the run has not finished, the close gate rejects;
   retry the edge. A red run sends the slice back to `in_progress`.

Engage before `git commit` / `git push` in a prior tool call: commitgate judges
the whole Bash payload before any line runs.

## Do not

- Bump `.version`, edit `CHANGELOG.md`, cut a tag, or run `satelle update`.
- Commit files outside the story's slice, or push a commit whose CI you did not
  see finish.
- Enact `landing → done` yourself; `satelle-chore-landed-check` gates it.
