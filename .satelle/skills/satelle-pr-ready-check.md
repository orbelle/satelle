---
name: satelle-pr-ready-check
type: check
description: The agent's DEV end (integration → confirm). HEAD is committed and pushed, contains origin/main, leaves CHANGELOG.md as origin/main has it, the item carries a valid changelog attachment (its lines, which the release run writes into the changelog), and an open, non-draft pull request against main has HEAD as its head and a title ending with the item id. Proves the head is ready for review; the item enters confirm only when pr-review (the judge, published by the satelle GitHub app), pr-approval (the app's approving review on HEAD) and result-summary accept after it, and approved only on the operator's word. No GitHub test run is read. Deterministic, no agent; reads GitHub with gh.
---

# PR ready (DEV → CI/CD hand-off)

DEV ends at an approved pull request (operator, 2026-11-06; modelled on
satelle-server's pr-ready). The coding gates judged the change and the
integration check ran the full local suite on this HEAD; this check makes that
head the one the release takes: the pull request's head is HEAD, and HEAD
already contains `origin/main`. A release run fast-forwards to it or merges it
onto the heads landing before it, and tests the combined candidate once.

This check proves the head is ready for review; it does not approve. On
integration → confirm it runs before `pr-review` and `pr-approval`
(sto_efe81afd): `pr-review` is the code-ac judge, whose verdict the satelle
GitHub app publishes on this pull request's head, and `pr-approval` passes only
on the app's approving review of HEAD. The item enters `confirm`, the
operator's state, only when both and `result-summary` accept after this check,
and reaches `approved` only on the operator's word. No GitHub review is read here, and GitHub runs
no tests.

Before presenting integration → confirm, the session:

1. works the item on its own branch, named by its id, cut from `origin/main`
   (or stacked on another approved item's head, to ship together);
2. commits without touching `CHANGELOG.md` or `.version` (the release run
   writes the version section and bumps the version once), and records its
   changelog lines on the item as an attachment named `changelog`:
   `satelle story attach <id> --name changelog --type text/markdown --file -`;
3. pushes: `git push -u origin <branch>`;
4. opens the pull request:
   `gh pr create --base main --title "<title> (<id>)" --body "<id>: <criteria>"`.

The `changelog` attachment (sto_04c03be7) holds the item's lines in this
format, owned by `scripts/changelog-items.sh`:

```markdown
### Added
- One line per entry, without the item id (the release run appends it).
### Fixed
- ...
```

A heading is `### <Section>`, Section one of Added, Changed, Deprecated,
Removed, Fixed, Security, Breaking. Every entry is a single `- <text>` line
under a heading; blank lines are ignored and any other line is refused, named.
At least one entry; a section may repeat. Items never edit `CHANGELOG.md`, so
two items cut from the same main never conflict over it.

```sh
set -u
for tool in gh jq git satelle; do
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
  echo "HEAD ${head:0:12} does not contain origin/main $(git rev-parse --short=12 origin/main): move the item back to in_progress, merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the pushed head is not rewritten and the push needs no force), resolve any conflict in the real work, push, and come through integration again"
  exit 1
fi
if ! git diff --quiet origin/main "$head" -- CHANGELOG.md; then
  echo "HEAD ${head:0:12} edits CHANGELOG.md; the release run writes it. Restore it (git checkout origin/main -- CHANGELOG.md), commit, and record your lines as the item's changelog attachment: satelle story attach $SATELLE_ITEM --name changelog --file -"
  exit 1
fi
if ! cl=$(satelle story attachment "$SATELLE_ITEM" changelog 2>/dev/null); then
  echo "no changelog attachment on $SATELLE_ITEM: satelle story attach $SATELLE_ITEM --name changelog --file - (### Added|Changed|Deprecated|Removed|Fixed|Security|Breaking, then one - line per entry)"
  exit 1
fi
if ! why=$(printf '%s\n' "$cl" | bash scripts/changelog-items.sh lines "$SATELLE_ITEM" 2>&1 >/dev/null); then
  echo "the changelog attachment on $SATELLE_ITEM is refused: $why"
  exit 1
fi
pr=$(gh pr list --state open --base main --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($SATELLE_ITEM)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against main has head ${head:0:12} and a title ending ($SATELLE_ITEM): gh pr create --base main --title \"<title> ($SATELLE_ITEM)\""
  exit 1
fi
echo "ready: PR #$pr (${head:0:12}) open against main, contains origin/main $(git rev-parse --short=12 origin/main); the satelle app's review decides approval"
```
