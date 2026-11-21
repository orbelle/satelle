---
name: satelle-pr-ready-check
type: check
description: DEV's hand-off to approval and then the release run (integration → in_review); the session then fires the release queue (satelle queue fire release --session <session>) and drives the run, so approved is not the session's end. HEAD is committed and pushed, contains the base branch, leaves CHANGELOG.md as the base branch has it, the item carries a valid changelog attachment (its lines, which the release run writes into the changelog), and an open, non-draft pull request against the base branch has HEAD as its head and a title ending with the item id. The base branch is the trigger base of the queue the item's workflow enqueues into, or, when it enqueues into no queue with one, the default branch of the git remote (checkout.defaultRemote, else the only remote). Proves the head is ready for approval: satelle-pr-approve-from-review then publishes the satelle app's approving review on HEAD from the accepted code-ac verdict, and the approval gate enters approved only when the app's approving review on HEAD is present. No GitHub test run is read. Deterministic, no agent; reads GitHub with gh.
---

# PR ready (DEV → CI/CD hand-off; the session then fires the release)

DEV's work reaches an approved pull request (operator, 2026-11-06). Approved
is the hand-off to the release run, not the session's end: the session that
drove the item there fires the release queue
(`satelle queue fire release --session <session>`) and drives the run until
the item is done. The coding gates judged the change and the integration check
ran the full local suite on this HEAD; this check makes that head the one the
release takes: the pull request's head is HEAD, and HEAD already contains the
base branch. A release run fast-forwards to it or merges it onto the heads
landing before it, and tests the combined candidate once.

- **The base branch.** It is the trigger base (`base = "<remote>/<branch>"`
  under `[trigger]`) of the queue the item's workflow enqueues into, read with
  `satelle story route` and `satelle queue get`: the branch the release run
  lands approved heads on is the branch the pull request targets. A workflow
  that enqueues into no queue with a base (one that lands through its own pull
  request) takes the default branch of the git remote instead: the remote
  `checkout.defaultRemote` names, else the only remote, asked with
  `git ls-remote --symref`. Queues naming more than one base, a base that is
  not `<remote>/<branch>`, and a fallback that cannot be read (no remote,
  several remotes and no `checkout.defaultRemote`, a remote naming no default
  branch) are refused, naming what to fix.
- **Waiting state.** When the target state declares `waits`, the last line
  quotes it.

This check proves the head is ready; it does not approve. On the same move,
`satelle-pr-approve-from-review` runs after it and publishes the satelle GitHub
app's approving review on HEAD from the accepted `code-ac` verdict. The approval
gate, on the move out of review, passes only on the app's approving review of
HEAD. No GitHub review is read here, and GitHub runs no tests. The pending
gate runs first on the same move, and each pull-request gate's result is
posted on the head as `satelle/<gate>` (`satelle-gate-status-publish`).

Before presenting the move into review, the session:

1. works the item on its own branch, named by its id, cut from the base branch
   (or stacked on another approved item's head, to ship together);
2. commits without touching `CHANGELOG.md` or `.version` (the release run
   writes the version section and bumps the version once), and records its
   changelog lines on the item as an attachment named `changelog`:
   `satelle story attach <id> --name changelog --type text/markdown --file - --session <session>`;
3. pushes: `git push -u origin <branch>`;
4. opens the pull request against the base branch:
   `gh pr create --base <base branch> --title "<title> (<id>)" --body "<id>: <criteria>"`.

The `changelog` attachment holds the item's lines in this format, owned by
`scripts/changelog-items.sh`:

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
two items cut from the same base never conflict over it.

```sh
set -u
for tool in gh jq git satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the pull request cannot be checked"; exit 1; }
done
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "the tree has uncommitted changes to tracked files; commit them, push, then present the move again"
  exit 1
fi
story=$SATELLE_ITEM
resolve_base_branch() {
  route=$(satelle story route "$story" --json 2>&1) || { echo "could not read the route of $story: $route"; exit 1; }
  wf=$(printf '%s' "$route" | jq -r '.workflow // empty')
  bases=
  for q in $(printf '%s' "$route" | jq -r '[.states[]?.enqueue[]?] | unique | .[]'); do
    b=$(satelle queue get "$q" --json 2>/dev/null | jq -r '.trigger.base // empty')
    [ -n "$b" ] && bases="$bases$q $b
"
  done
  n=$(printf '%s' "$bases" | awk 'NF { print $2 }' | sort -u | grep -c .)
  if [ "$n" -gt 1 ]; then
    echo "the queues the workflow $wf of $story enqueues into name $n trigger bases ($(printf '%s' "$bases" | awk 'NF { printf "%s%s base %s", s, $1, $2; s = "; " }')), so the pull request's base branch is ambiguous: give them one base"
    exit 1
  fi
  if [ "$n" -eq 0 ]; then
    why="the workflow $wf of $story enqueues into no queue with a trigger base"
    alt="or give a queue its states enqueue into base = \"<remote>/<branch>\" under [trigger]"
    remotes=$(git remote)
    remote=$(git config --get checkout.defaultRemote)
    if [ -n "$remote" ]; then
      printf '%s\n' "$remotes" | grep -qxF -e "$remote" || { echo "$why, and checkout.defaultRemote names $remote, which git remote does not list, so the pull request's base branch cannot be read: git config checkout.defaultRemote <remote> naming one git remote lists, $alt"; exit 1; }
    else
      case $(printf '%s' "$remotes" | grep -c .) in
        0) echo "$why, and this repo has no git remote, so the pull request's base branch cannot be read: git remote add <remote> <url>, $alt"; exit 1 ;;
        1) remote=$remotes ;;
        *) echo "$why, and this repo has several git remotes ($(printf '%s' "$remotes" | tr '\n' ' ')) and no checkout.defaultRemote, so the pull request's base branch cannot be read: git config checkout.defaultRemote <remote>, $alt"; exit 1 ;;
      esac
    fi
    heads=$(GIT_TERMINAL_PROMPT=0 git ls-remote --symref -- "$remote" HEAD 2>&1) || { echo "$why, and could not read the default branch of the remote $remote: $heads; $alt"; exit 1; }
    branch=$(printf '%s\n' "$heads" | awk -F '\t' '$2 == "HEAD" && index($1, "ref: refs/heads/") == 1 { print substr($1, 17); exit }')
    if [ -z "$branch" ] || ! printf '%s\n' "$heads" | awk -F '\t' '$2 == "HEAD" && $1 !~ /^ref: / { f = 1 } END { exit !f }'; then
      echo "$why, and the remote $remote names no default branch, so the pull request's base branch cannot be read: set the default branch of the repository $remote names, $alt"
      exit 1
    fi
    base_queue=
    base_ref="$remote/$branch"
  else
    base_queue=$(printf '%s' "$bases" | awk 'NF { print $1; exit }')
    base_ref=$(printf '%s' "$bases" | awk 'NF { print $2; exit }')
    case "$base_ref" in
      ?*/?*) remote=${base_ref%%/*} branch=${base_ref#*/} ;;
      *) echo "the queue $base_queue has trigger base $base_ref, which is not <remote>/<branch>, so the pull request's base branch cannot be read: set base = \"<remote>/<branch>\" under its [trigger]"; exit 1 ;;
    esac
  fi
}
resolve_base_branch
waits=$(printf '%s' "$route" | jq -r --arg s "$SATELLE_TO" '[.states[]? | select(.name == $s) | .waits // empty][0] // empty')
git fetch -q "$remote" || { echo "git fetch $remote failed"; exit 1; }
head=$(git rev-parse HEAD)
if [ -z "$(git branch -r --contains "$head")" ]; then
  echo "HEAD ${head:0:12} is not pushed: git push -u $remote <branch>"
  exit 1
fi
if ! git merge-base --is-ancestor "$base_ref" "$head"; then
  echo "HEAD ${head:0:12} does not contain $base_ref $(git rev-parse --short=12 "$base_ref"): move the item back to in_progress, merge $base_ref into the branch (git fetch $remote && git merge $base_ref; never rebase, so the pushed head is not rewritten and the push needs no force), resolve any conflict in the real work, push, and come through integration again"
  exit 1
fi
if ! git diff --quiet "$base_ref" "$head" -- CHANGELOG.md; then
  echo "HEAD ${head:0:12} edits CHANGELOG.md; the release run writes it. Restore it (git checkout $base_ref -- CHANGELOG.md), commit, and record your lines as the item's changelog attachment: satelle story attach $SATELLE_ITEM --name changelog --file - --session <session>"
  exit 1
fi
if ! cl=$(satelle story attachment "$SATELLE_ITEM" changelog 2>/dev/null); then
  echo "no changelog attachment on $SATELLE_ITEM: satelle story attach $SATELLE_ITEM --name changelog --file - --session <session> (### Added|Changed|Deprecated|Removed|Fixed|Security|Breaking, then one - line per entry)"
  exit 1
fi
if ! why=$(printf '%s\n' "$cl" | bash scripts/changelog-items.sh lines "$SATELLE_ITEM" 2>&1 >/dev/null); then
  echo "the changelog attachment on $SATELLE_ITEM is refused: $why"
  exit 1
fi
pr=$(gh pr list --state open --base "$branch" --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($SATELLE_ITEM)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against $branch has head ${head:0:12} and a title ending ($SATELLE_ITEM): gh pr create --base $branch --title \"<title> ($SATELLE_ITEM)\""
  exit 1
fi
line="ready: PR #$pr (${head:0:12}) open against $branch, contains $base_ref $(git rev-parse --short=12 "$base_ref")"
if [ -n "$waits" ]; then
  line="$line; $SATELLE_ITEM waits at $SATELLE_TO for $waits"
fi
echo "$line"
```
