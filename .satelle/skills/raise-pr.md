---
name: raise-pr
scope: project
type: skill
tags: [type:skill]
description: In-loop executor skill for the pr route's `pr_raising` step. Commit the integrated slice on branch epic/<id>, push that branch, open its pull request with the story's criteria block (`satelle pr body <id>`) as its body, tag the story pr:<n> and record the branch. Main is never committed to or pushed. Never merges, releases or self-advances status; the team PR-approval remote approves the PR from pr_open.
---

# Raise PR (in-loop executor step, pr route)

You are the **executor** for the `pr_raising` step of a story in category `pr`.
Integration already proved the slice formats, vets and passes
its suites. On this route the developer does not merge or release. From
`pr_open` the team PR-approval remote (`tsk_pr-approve`) runs a cold review of
the pull request and, on accept, approves its exact head as the repo's GitHub
App. Merge and release are not this step's. Your job is to hand it a pull
request it can find and read.

The remote's node holds no story store, so the pull request itself must carry
what it needs:

- it resolves the open pull request whose head is `epic/<id>`;
- it reads the story's acceptance criteria from the hashed criteria block in
  the pull request body, which `satelle pr body <id>` prints. A PR whose block
  is missing, names another story or fails its hash is rejected.

## Do

1. **Enumerate the slice:** `satelle story diff <id> --recorded`, minus paths
   git ignores (`git check-ignore -q <path>` exits 0).
2. **Branch without touching main:** on a first presentation,
   `git switch -c epic/<id>` from the current HEAD (a story worktree is
   already on `epic/<id>`: stay on it). The working changes come with it; main
   is left as it was. On a re-presentation the tree is already on `epic/<id>`
   (see *A denied pull request*): stay on it, so the same pull request is
   updated.
3. **Commit the slice only:** stage exactly the slice paths (never
   `git add -A`) and commit with a conventional subject ending in the story id
   and no AI attribution. No `.version` bump and no `CHANGELOG.md` entry: the
   remote's release step owns both.
4. **Push the branch:** `git push -u origin epic/<id>`. Never push main, and
   never force-push.
5. **Open the pull request** (first presentation only):
   `satelle pr body <id> > <scratch>/pr-body.md`, then
   `gh pr create --base main --head epic/<id> --title "<story title> (<id>)" --body-file <scratch>/pr-body.md`.
   The body is exactly the criteria block; write no criteria by hand. On a
   re-presentation the push updates the existing PR; do not open a second one.
   If the story's criteria changed since the PR opened, replace the body with
   a fresh `satelle pr body <id>` (`gh pr edit <n> --body-file ...`).
6. **Record it on the story:**
   `satelle story set <id> --add-tags pr:<n>,branch:epic/<id>` where `<n>` is
   the PR number, and
   `satelle story log <id> --kind pr --data pr=<n> --data branch=epic/<id> --data commit=<sha>`.
7. **Return to main:** `git switch main`. The tree is clean, because the slice
   is committed on the branch.
8. **Request `pr_open`.** `satelle-pr-open-check` gates it.

Engage before `git commit` / `git push` in a prior tool call: commitgate judges
the whole Bash payload before any line runs ([[satelle-edits-require-a-story]]).

## A denied pull request

A rejecting review posts no approval and concludes the remote's run with its
findings; the route moves the story back to `in_progress` (or parks it at
`blocked` with origin `pr_open` when the run stops). A parked
story leaves `blocked` only to its origin, so the way back is two moves, both
declared, with the same acceptance criteria:

1. `satelle story set <id> --status pr_open` (resume to origin), then
   `satelle story set <id> --status in_progress` (the route's recovery).
2. Before any rework, `git switch epic/<id>`: the slice lives only on that
   branch, so reworking on main would start from code without the PR in it.
3. Read the run's findings (`list_remote_nodes` with the remote and story id)
   and relay them to the coder with
   `satelle story message <id> --from orchestrator --to coder`.
4. After integration this step runs again on the same branch and pushes to
   the same pull request.

## Do not

- Commit to or push `main`, bump `.version`, edit `CHANGELOG.md`, tag a release
  or run `satelle update`.
- Merge the pull request, or open a second one for the same story.
- Enact `pr_raising → pr_open` yourself.
