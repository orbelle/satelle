---
name: satelle-pr-ready-check
type: check
description: DEV's end (integration → approved). HEAD is committed and pushed, contains origin/main, and has an open, non-draft pull request against main whose head is HEAD and whose title ends with the item id. Approved automatically; no review or GitHub test run is read. Deterministic, no agent; reads GitHub with gh.
---

# PR ready (DEV → CI/CD hand-off)

DEV ends at an approved pull request (operator, 2026-11-06; modelled on
satelle-server's pr-ready). The coding gates judged the change and the
integration check ran the full local suite on this HEAD; this check makes that
test count for the release: the pull request's head is HEAD, and HEAD already
contains `origin/main`, so a release run fast-forwards it without testing
again.

"Approved automatically": the item enters `approved` when this check accepts.
No reviewer agent is bound and no GitHub review is requested or read (GitHub
refuses an approval from the PR's own author; the satelle GitHub app,
satelle-server sto_f736956c, is the later reviewer). The code-ac gate is the
review until then. GitHub runs no tests.

Before presenting integration → approved, the session:

1. works the item on its own branch, named by its id, cut from `origin/main`
   (or stacked on another approved item's head, to ship together);
2. commits, adding its CHANGELOG lines under `## [Unreleased]` and leaving
   `.version` alone (the release run bumps it once);
3. pushes: `git push -u origin <branch>`;
4. opens the pull request:
   `gh pr create --base main --title "<title> (<id>)" --body "<id>: <criteria>"`.

```sh
set -u
for tool in gh jq git; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the pull request cannot be checked"; exit 1; }
done
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "the tree has uncommitted changes to tracked files; commit them, push, then present the move again"
  exit 1
fi
git fetch -q origin || { echo "git fetch origin failed"; exit 1; }
head=$(git rev-parse HEAD)
if [ -z "$(git branch -r --contains "$head")" ]; then
  echo "HEAD ${head:0:12} is not pushed: git push -u origin <branch>"
  exit 1
fi
if ! git merge-base --is-ancestor origin/main "$head"; then
  echo "HEAD ${head:0:12} does not contain origin/main $(git rev-parse --short=12 origin/main): move the item back to in_progress, rebase onto origin/main and come through integration again"
  exit 1
fi
pr=$(gh pr list --state open --base main --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($SATELLE_ITEM)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against main has head ${head:0:12} and a title ending ($SATELLE_ITEM): gh pr create --base main --title \"<title> ($SATELLE_ITEM)\""
  exit 1
fi
echo "approved: PR #$pr (${head:0:12}) open against main, contains origin/main $(git rev-parse --short=12 origin/main); no review or GitHub test is read"
```
