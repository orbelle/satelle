---
name: satelle-pr-ready-check
type: check
description: DEV's end (integration → approved). HEAD is committed and pushed, contains origin/main, leaves CHANGELOG.md as origin/main has it, the item carries a valid changelog attachment (its lines, which the release run writes into the changelog), and an open, non-draft pull request against main has HEAD as its head and a title ending with the item id. Approved automatically; no review or GitHub test run is read. Deterministic, no agent; reads GitHub with gh.
---

# PR ready (DEV → CI/CD hand-off)

DEV ends at an approved pull request (operator, 2026-11-06; modelled on
satelle-server's pr-ready). The coding gates judged the change and the
integration check ran the full local suite on this HEAD; this check makes that
head the one the release takes: the pull request's head is HEAD, and HEAD
already contains `origin/main`. A release run fast-forwards to it or merges it
onto the heads landing before it, and tests the combined candidate once.

"Approved automatically": the item enters `approved` when this check accepts.
No reviewer agent is bound and no GitHub review is requested or read (GitHub
refuses an approval from the PR's own author; the satelle GitHub app,
satelle-server sto_f736956c, is the later reviewer). The code-ac gate is the
review until then. GitHub runs no tests.

Before presenting integration → approved, the session:

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
echo "approved: PR #$pr (${head:0:12}) open against main, contains origin/main $(git rev-parse --short=12 origin/main); no review or GitHub test is read"
```
