---
name: release
type: skill
description: CI/CD — the procedure a run of the release task (tas_42301d36), fired from the release queue, follows over the approved items it took: fast-forward main to each tested pull-request head without re-testing, send back any head that no longer fast-forwards, bump the version once, dispatch release.yml, confirm both releases, back up and install the published build here, check it, only then move each item to done, and finally clear the finished worktrees. Landing is local; GitHub runs no tests. Never forces, never merges or rebases main.
---

# Release (CI/CD)

DEV ends at `approved` (operator, 2026-11-06; modelled on satelle-server's
release skill): an open, auto-approved pull request whose head contains
`origin/main` and passed the full local suite on exactly that tree (the
integration check). The suite runs once, so a release run does not test
again: it ships only heads that fast-forward `main`, which are the trees that
were tested. A head that no longer fast-forwards goes back through the loop
(rebase, coding gates, the full suite, a pull request), with the reason
recorded on the item. A pushed-back item reopens; it never becomes a new
story.

The run's items reach `done` only through it: the `release-run` gate on
`approved → done` refuses any item no in-progress run lists. Every step below
that fails stops the run: record the reason in the run's `release-evidence`,
leave the items as they are and end the turn. Nothing installed is touched
before step 7.

Never: run a test suite, merge or rebase onto main, force-push, take an item
not in the run's inputs, or close an item before step 8 passes.

## 0. Start and read the run

```sh
task=tas_42301d36
que=release
git fetch -q origin
satelle queue get "$que" --json 2>/dev/null | jq -e --arg t "$task" '.trigger.task == $t' >/dev/null || { echo "release queue $que (.satelle/queues/$que.toml) is missing or does not trigger $task; a satelle older than the build origin/main carries cannot read queue files: install that build (step 7), then start again" >&2; exit 1; }
fired=$(satelle queue fire "$que" --json) || exit $?
exe=$(printf '%s\n' "$fired" | jq -r '.run // empty')                  # created in backlog
[ -n "$exe" ] || { echo "release queue $que has nothing to fire" >&2; exit 1; }
satelle execution set "$exe" --status in_progress                     # validate-before
satelle execution seat "$exe"
satelle ledger list "$exe" --kind task.run --json | jq -r '.[0].payload.inputs[]'
```

The printed base must be origin/main's head (`git rev-parse origin/main`):
`--base main` would resolve a local `main` branch first, which may be stale.

The release run is fired from the release queue, defined in
`.satelle/queues/release.toml` (filter: approved stories; trigger: the release
task, base `origin/main`, min size 0). The delivery and chore workflows enqueue
into it by name on their `approved` state, so a story reaching approved joins it
with the transition as the cause. Nothing fires on enqueue: this step is the
fire, and the file's `base` is the run's base. `satelle queue show release`
shows its members and last fire. A fire refused because the last run is still
open exits 2 naming that run: finish or cancel it first.

For each input item find its pull request: the PR against main whose title
ends `(<id>)` (`gh pr list --state open --base main --json number,headRefOid,title`).
Record `<id> PR #<n> head <sha>`. An item with no open pull request goes on the
send-back list.

### Reviews on the heads

Then read the satelle GitHub app's review on each item's PR head, once per
input item (`id=` each in turn). The app's latest decision on the head
(APPROVED, CHANGES_REQUESTED or DISMISSED; a comment decides nothing) is
matched by its account's numeric id, read from the approval check, never by
login. When it requested changes, the item reopens here: the review body is
attached as `pr-review`, the item moves approved → in_progress, and it is
dropped from the run and recorded as sent back with "the satelle app requested
changes on head <sha>". The block calls gh and satelle's local commands only,
never satelle.dev, and changes nothing until the app reviews this repo's pull
requests.

```sh
id=<id>
app_account=$(sed -n 's/^app_account=\([0-9][0-9]*\).*/\1/p' .satelle/skills/satelle-pr-approval-check.md)
[ -n "$app_account" ] || { echo "no app_account= line in .satelle/skills/satelle-pr-approval-check.md" >&2; exit 1; }
pr=$(gh pr list --state open --base main --json number,headRefOid,title --limit 100 |
  jq -c --arg id "($id)" '[.[] | select(.title | endswith($id))][0] // empty')
if [ -z "$pr" ]; then echo "$id: no open pull request (send it back)"; exit 0; fi
n=$(printf '%s' "$pr" | jq -r .number)
head=$(printf '%s' "$pr" | jq -r .headRefOid)
review=$(gh api "repos/{owner}/{repo}/pulls/$n/reviews" --paginate | jq -s -c --argjson app "$app_account" --arg h "$head" '
  (add // []) | [.[] | select(.user.id == $app and .commit_id == $h and
    (.state == "APPROVED" or .state == "CHANGES_REQUESTED" or .state == "DISMISSED"))]
  | sort_by(.submitted_at) | last // empty')
if [ "$(printf '%s' "$review" | jq -r '.state // empty')" = CHANGES_REQUESTED ]; then
  printf '%s' "$review" | jq -j '.body // ""' | satelle story attach "$id" --name pr-review --type text/markdown --file -
  satelle story set "$id" --status in_progress      # approved -> in_progress
  echo "$id: sent back: the satelle app requested changes on head $head ($(printf '%s' "$review" | jq -r .html_url))"
else
  echo "$id: PR #$n head $head: no changes requested by the satelle app"
fi
```

Record `sha256sum` of `$(command -v satelle)` and of the
satelled binary in the `installed` line of `satelle service status`; the same
sums just before step 7 prove nothing installed was touched.

## 1. Order

Sort the items by `git rev-list --count origin/main..<head>`, fewest first, so
stacked heads fast-forward in turn.

## 2. Fast-forward without re-testing

a. Plan in git only. Start with `tip=origin/main`. For each item:
   - its head is already on main (`git merge-base --is-ancestor <head> origin/main`): keep it (a resumed run);
   - else `git merge-base --is-ancestor $tip <head>`: keep it and set `tip=<head>`;
   - else put it on the send-back list: "head <sha> does not contain main <tip> after <earlier ids>; rebase onto origin/main and come through integration again".

b. The tree that presents the moves below must carry the config being
   released: `git diff --quiet HEAD "$tip" -- .satelle/workflows .satelle/skills`,
   else stop with "check out the run's tip first: git checkout --detach $tip".

c. Send back each item on the list. It reopens:

```sh
printf '%s\n' "<reason>" | satelle story attach <id> --name release-sendback --file -
satelle story set <id> --status in_progress      # approved -> in_progress
```

   The coder's next run reads the reason from the item's record.

   When the installed satelle refuses the tip's workflows (an unknown key, such
   as a gate's `after`, from a satelle older than the build origin/main
   carries), build the tip in a temporary worktree as step 3 makes one
   (`wt=$(mktemp -d); git worktree add --detach "$wt" "$tip"; go build -C "$wt"
   -o "$wt/satelle" ./cmd/satelle`) and present the send-back with that build
   first on `PATH`.

If no item is kept, stop: "nothing fast-forwards".

## 3. Bump once

In a temporary worktree at the tip, so the session's own tree is untouched:

```sh
wt=$(mktemp -d); git worktree add --detach "$wt" "$tip"
```

For `satelle.version` against the `v*` tags and `satelled.version` against the
`serve-v*` tags: strip the prefix, `sort -V`, and compare with the latest tag
other than the value's own. Keep the current value when it is above that
latest and its own tag is absent or points at `$tip` (a resumed run already
published it); otherwise set it to the latest tag's patch + 1. Set
`satelle.build=$(date -u +%Y-%m-%dT%H:%M:%SZ)` when either changed.

Rename `## [Unreleased]` in CHANGELOG.md to `## [<ver>] - <date>`; with no
Unreleased heading, insert `## [<ver>] - <date>` / `### Changed` with one line
per released item, `- <title> (<id>)`.

Commit only `.version` and `CHANGELOG.md`, so the tested tree is otherwise
unchanged: `chore(release): v<ver>, satelled <sver> (<exe>: <ids>)`. Skip the
commit when nothing changed (a resumed run).

## 4. Push main once

```sh
git -C "$wt" push origin HEAD:refs/heads/main   # a plain push: refused unless it fast-forwards; never forced
git worktree remove "$wt"
```

GitHub marks each pull request merged, because main now contains its head.

## 5. Dispatch

```sh
gh workflow run release.yml --ref main
gh run list --workflow release.yml --limit 5 --json databaseId,headSha,url   # the run whose headSha is the pushed sha
gh run watch <id> --exit-status
```

## 6. Confirm both releases

```sh
gh release view v<ver> --json tagName,assets
gh release view serve-v<sver> --json tagName,assets
```

## 7. Back up, then install here

```sh
sbin=$(command -v satelle)
dbin=<the path in the [...] of the "installed" line of satelle service status>
bk="$(satelle runtime path)/backups/bin/$(date -u +%Y%m%dT%H%M%SZ)"; mkdir -p "$bk"; cp -p "$sbin" "$dbin" "$bk/"
printf 'restore: install -m 0755 %s %s && install -m 0755 %s %s && satelle service restart\n' "$bk/satelle" "$sbin" "$bk/satelled" "$dbin"
satelle update --version <ver>
"$sbin" update --version <ver>    # the new binary's own update: satelled serve-v<ver>
satelle service restart
```

The restore line is printed, with real paths and no placeholder, before
anything is replaced. The backup is never deleted by the run. The second
update runs the binary just installed, so a fix to `satelle update` itself
takes effect in the run that ships it; when the pair is already right it only
prints two `is current` lines.

## 8. Check

`satelle version` names `<ver>` and main's commit; `satelle service status`
shows satelled answering, `running` and `installed` at `<sver>` and main's
commit, and no `warning` line (the release-installed gate's patterns). A
mismatch stops the run and repeats the restore line.

## 9. Close

For each released item: `satelle story set <id> --status done`. Its five gates
(release-run, version-stamp, changelog-entry, ci-published, release-installed)
judge it while the run is still in progress. Then attach `release-evidence` to
the run (`satelle execution attach "$exe" --name release-evidence --file -`):

- per item: id, PR number, head sha, and released, or sent back with the reason
- main before and after, and the bump commit
- the release.yml run URL, head sha and conclusion
- both tags with their asset names
- the step-0 and pre-step-7 binary sha256s, the backup dir listing and the restore line
- the `satelle version` and `satelle service status` lines
- each item's final status

Then present the run `in_progress → done` (validate-after) and release its
seat.

## 10. Clear the finished worktrees

Clearing worktrees is part of the workflow (operator, 2026-11-06): from the
main checkout (not a linked worktree, which the cleanup may remove), run the
cleanup task and follow its run to done:

```sh
cd <the main checkout>
git fetch -q origin
satelle task run tas_b3424a1c --base origin/main
```

The cleanup removes each linked worktree that has no uncommitted or untracked
change and whose HEAD is on a remote branch (so every released item's
worktree), deletes its pushed local branch, keeps and reports any other, and
puts the main checkout on main at origin/main. It never removes a worktree
with work that is not pushed.
