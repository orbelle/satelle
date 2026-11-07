---
name: release
type: skill
description: CI/CD — the procedure a run of the release task (tas_42301d36), fired from the release queue, follows over the approved items it took: build one release-local candidate from their tested pull-request heads in queue order (fast-forward a head that contains it, merge one that does not, send back only a head whose merge conflicts), run make test once on that candidate, bump the version once on top, fast-forward main to it, dispatch release.yml, confirm both releases, back up and install the published build here, check it, only then move each item to done, and finally clear the finished worktrees. Landing is local; GitHub runs no tests. main only ever fast-forwards to a tree the release tested; never forces or rebases, and merges only in the candidate.
---

# Release (CI/CD)

DEV ends at `approved` (operator, 2026-11-06; modelled on satelle-server's
release skill): an open, auto-approved pull request whose head contains
`origin/main` and passed the full local suite on exactly that tree (the
integration check). Items approved from the same main no longer fast-forward
one another, so a run lands them together: it builds one release-local
candidate from the run's items in queue order, starting at `origin/main`. A
head that contains the candidate so far is fast-forwarded to; a head that does
not is merged onto it (a merge commit in the candidate only, never a rebase or
a force); a head whose merge conflicts is dropped and sent back alone, naming
the conflicting paths. The run then runs `make test` once on the final
candidate, the combined tree. main only ever fast-forwards to a tree the
release tested: the tested candidate, plus the bump commit, which changes only
the files step 3 commits. A sent-back item goes back through the loop (merge
origin/main into the branch with `git fetch origin && git merge origin/main`,
resolve any conflict in the real work, push, and come through integration
again: coding gates, the full suite, a pull request), with the reason recorded
on the item. A pushed-back item reopens; it never becomes a new story.

Items never edit `CHANGELOG.md`: each records its lines as an attachment named
`changelog` (format in `satelle-pr-ready-check.md`, owned by
`scripts/changelog-items.sh`), and this run writes the version section from
the items it lands (step 3).

The run's items reach `done` only through it: the `release-run` gate on
`approved → done` refuses any item no in-progress run lists. Every step below
that fails stops the run: record the reason in the run's `release-evidence`,
leave the items as they are and end the turn. Nothing installed is touched
before step 7.

Never: run any suite but step 2d's one run; rebase; force-push; merge on main
itself (merges exist only in the candidate); push to main anything but the
tested candidate and its bump commit; take an item not in the run's inputs; or
close an item before step 8 passes.

Shell variables do not survive between the session's separate commands, so
from step 2 on the run keeps its state in one run-local directory,
`rd="${TMPDIR:-/tmp}/satelle-release-$exe"`: the candidate worktree
`$rd/candidate` (`$wt` below) and the files `kept`, `sendback`, `ff`,
`tested` and `suite.log`. Each block finds the in-progress run and `rd` again
the same way. The durable record stays on the items (`release-sendback`) and
the run (`release-evidence`).

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

The run takes its items in queue order: the order of the `task.run` inputs
step 0 printed (explicit members in enqueue order, then filter candidates).
Nothing is sorted. A stacked head whose base comes later in the order still
lands: its base is merged with it, and when the base's turn comes its head is
already in the candidate.

## 2. Build the candidate, test it once

a. Judge each item's changelog, then build the candidate.

### Each kept item's changelog

Once per input item still `approved` after step 0, in queue order (`id=` each
in turn, with its `head` from step 0 and `tip=origin/main`). Its verdict is
its output, never its exit status: every item outcome prints exactly one line
and exits 0, so a send-back never stops the run. It exits 1 only when it
cannot judge at all (git, satelle or bash is not on PATH, named on stderr):
that is a failed step, which stops the run. The head's own
`scripts/changelog-items.sh` validates the attachment, so a legacy head
without the script is sent back too. The CHANGELOG.md test reads the head's
own commits only (`$tip...$head`, from their merge base), so a head cut from
an older main is not mistaken for one that edits the changelog.

```sh
id=<id>
for tool in git satelle bash; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not on PATH, so the changelog of $id cannot be judged" >&2; exit 1; }
done
why=
if ! cl=$(satelle story attachment "$id" changelog 2>/dev/null); then
  why="no changelog attachment"
elif ! git cat-file -e "$head:scripts/changelog-items.sh" 2>/dev/null; then
  why="head has no scripts/changelog-items.sh"
elif ! out=$(printf '%s\n' "$cl" | bash <(git show "$head:scripts/changelog-items.sh") lines "$id" 2>&1 >/dev/null); then
  why=${out:-the attachment is refused}
fi
if [ -n "$why" ]; then
  echo "$id: send back: no valid changelog attachment on head $head: merge origin/main and record the lines with satelle story attach $id --name changelog --file - ($why)"
elif ! git diff --quiet "$tip...$head" -- CHANGELOG.md; then
  echo "$id: send back: head $head edits CHANGELOG.md; restore it (git checkout origin/main -- CHANGELOG.md) and move the lines to the changelog attachment"
else
  echo "$id: keep head $head"
fi
```

### Build the candidate

Once, after the changelog verdicts, with every `<id>: send back: <reason>`
line they printed pasted into `held` (nothing when none was). It recreates
`$rd`, adds the candidate worktree detached at `origin/main` and walks the
run's inputs in queue order. It skips an item no longer `approved` (step 0's
review sent it back) or held by its changelog. For the rest, with the
candidate's current HEAD as the tip:

- the head is already in the candidate (`git merge-base --is-ancestor <head> <tip>`; a resumed run's head already on main too): kept, contained;
- the tip is an ancestor of the head: kept, the candidate fast-forwards to it;
- else the head is merged onto the tip in the candidate (`--no-ff`, a merge commit; the head itself is never rewritten): kept, merged. The tip before the first merge is recorded in `$rd/ff` (the fast-forward prefix);
- a merge that conflicts is aborted, the item is dropped from the candidate and goes on the send-back list naming the conflicting paths: "head <sha> conflicts with the release candidate after <kept ids> in: <paths>; merge origin/main into the branch, resolve, and come through integration again".

An item with no open pull request goes on the send-back list too. Each kept
item is recorded `<id> <head>` in `$rd/kept`, in queue order (the landing
order). Each item prints one line, `<id>: <fast-forward|merged|contained|sent
back|skipped> ...`; the block ends `candidate <sha> at <wt>`, or exits 1 with
"nothing lands" when no item is kept (send back, step 2c, and stop).

```sh
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
held=$(cat <<'HELD'
<each "<id>: send back: <reason>" line the changelog verdicts printed>
HELD
)
git fetch -q origin || { echo "git fetch origin failed" >&2; exit 1; }
[ ! -e "$wt" ] || git worktree remove --force "$wt"
rm -rf "$rd" && mkdir -p "$rd" || exit 1
git worktree add -q --detach "$wt" origin/main || exit 1
: >"$rd/kept"
printf '%s\n' "$held" | sed -n 's/^\([a-z]*_[0-9a-f]*\): send back: /\1\t/p' >"$rd/sendback"
prs=$(gh pr list --state open --base main --json number,headRefOid,title --limit 100) || { echo "gh pr list failed" >&2; exit 1; }
kept=
for id in $(satelle ledger list "$exe" --kind task.run --json | jq -r '.[0].payload.inputs[]'); do
  st=$(satelle story get "$id" --json | jq -r .status)
  if [ "$st" != approved ]; then echo "$id: skipped: $st, not approved"; continue; fi
  if cut -f1 "$rd/sendback" | grep -qx "$id"; then echo "$id: sent back: its changelog"; continue; fi
  head=$(printf '%s' "$prs" | jq -r --arg id "($id)" '[.[] | select(.title | endswith($id))][0].headRefOid // empty')
  if [ -z "$head" ]; then
    printf '%s\tno open pull request against main has a title ending (%s)\n' "$id" "$id" >>"$rd/sendback"
    echo "$id: sent back: no open pull request"; continue
  fi
  tip=$(git -C "$wt" rev-parse HEAD)
  if git -C "$wt" merge-base --is-ancestor "$head" "$tip"; then
    how=contained
  elif git -C "$wt" merge-base --is-ancestor "$tip" "$head"; then
    git -C "$wt" merge -q --ff-only "$head" || exit 1
    how=fast-forward
  elif git -C "$wt" merge -q --no-ff --no-edit -m "release candidate: merge $id head $head" "$head" >/dev/null 2>&1; then
    [ -s "$rd/ff" ] || echo "$tip" >"$rd/ff"
    how=merged
  else
    paths=$(git -C "$wt" diff --name-only --diff-filter=U | paste -sd ' ' -)
    git -C "$wt" merge --abort 2>/dev/null
    printf '%s\thead %s conflicts with the release candidate after %s in: %s; merge origin/main into the branch, resolve, and come through integration again\n' \
      "$id" "$head" "${kept:-origin/main}" "${paths:-(the merge failed without unmerged paths)}" >>"$rd/sendback"
    echo "$id: sent back: head $head conflicts in: $paths"; continue
  fi
  echo "$id $head" >>"$rd/kept"; kept="${kept:+$kept }$id"
  echo "$id: $how head $head"
done
[ -s "$rd/ff" ] || git -C "$wt" rev-parse HEAD >"$rd/ff"
[ -s "$rd/kept" ] || { echo "nothing lands: send back (step 2c) and stop the run" >&2; exit 1; }
echo "candidate $(git -C "$wt" rev-parse HEAD) at $wt"
```

b. The tree that presents the moves below must carry the config being
   released: `git diff --quiet HEAD "$tip" -- .satelle/workflows .satelle/skills scripts/changelog-items.sh`,
   with `tip` the candidate's HEAD, else stop with "check out the run's tip
   first: git checkout --detach $tip".

c. Send back each item on the list. It reopens, and the coder's next run
   reads the reason from the item's record.

### Send back

Runs over `$rd/sendback` and empties it, so a second pass after 2d sends
back only what 2d added. An item no longer `approved` is not moved again.

```sh
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"
[ -f "$rd/sendback" ] || { echo "no $rd/sendback: build the candidate (step 2a) first" >&2; exit 1; }
while IFS=$'\t' read -r id reason <&3; do
  [ -n "$id" ] || continue
  if [ "$(satelle story get "$id" --json | jq -r .status)" != approved ]; then echo "$id: not approved, not moved"; continue; fi
  printf '%s\n' "$reason" | satelle story attach "$id" --name release-sendback --file - || exit 1
  satelle story set "$id" --status in_progress || exit 1      # approved -> in_progress
  echo "$id: sent back: $reason"
done 3<"$rd/sendback"
: >"$rd/sendback"
```

   When the installed satelle refuses the tip's workflows (an unknown key, such
   as a gate's `after`, from a satelle older than the build origin/main
   carries), build the tip in a temporary worktree
   (`bt=$(mktemp -d); git worktree add --detach "$bt" "$tip"; go build -C "$bt"
   -o "$bt/satelle" ./cmd/satelle`) and present the send-back with that build
   first on `PATH`.

d. Test the candidate once.

### Test the candidate once

`make test`, the full suite, runs once, on the final candidate and on no other
tree, in the foreground. On a pass the candidate's sha is recorded in
`$rd/tested`, which step 4 pushes only if nothing but the bump changed. On a
failure every item that entered the candidate by merge (its head is not in
`$rd/ff`) goes on the send-back list; an item in the fast-forward prefix stays
`approved` and the next fire takes it. Then run 2c, attach the tail of
`$rd/suite.log` to the run's `release-evidence` and stop the run: nothing is
pushed.

```sh
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
[ -d "$wt" ] && [ -s "$rd/ff" ] || { echo "no candidate at $wt: build it (step 2a) first" >&2; exit 1; }
rm -f "$rd/tested"
cand=$(git -C "$wt" rev-parse HEAD)
if make -C "$wt" test >"$rd/suite.log" 2>&1; then
  echo "$cand" >"$rd/tested"
  echo "make test passed on the candidate $cand"
  exit 0
fi
ff=$(cat "$rd/ff")
while read -r id head; do
  git merge-base --is-ancestor "$head" "$ff" && continue
  printf '%s\tthe release candidate %s failed make test (log in release-evidence); merge origin/main into the branch and come through integration again\n' "$id" "$cand" >>"$rd/sendback"
done <"$rd/kept"
echo "make test failed on the candidate $cand (log $rd/suite.log): send back (step 2c), attach the log's tail to release-evidence and stop the run" >&2
exit 1
```

## 3. Bump once

In the candidate worktree `$wt` (`$rd/candidate`), on top of the tested
candidate, so the session's own tree is untouched.

For `satelle.version` against the `v*` tags and `satelled.version` against the
`serve-v*` tags: strip the prefix, `sort -V`, and compare with the latest tag
other than the value's own. Keep the current value when it is above that
latest and its own tag is absent or points at the candidate (a resumed run already
published it); otherwise set it to the latest tag's patch + 1. Set
`satelle.build=$(date -u +%Y-%m-%dT%H:%M:%SZ)` when either changed.

### Changelog from the items

Write the version section from the kept items' `changelog` attachments, in
landing order (`$rd/kept`, queue order), in `$wt`. The section, `## [<ver>] - <date>`
(UTC date) with each item's lines under their `### <Section>` in fixed order
(Added, Changed, Deprecated, Removed, Fixed, Security, Breaking), each line
ending with its item id, is written as the file's first `## [` heading;
nothing is staged above it. The lines are copied verbatim; the run never
edits an item's content. A missing attachment stops the run. A resumed run
whose CHANGELOG.md already has the `## [<ver>] - ` heading changes nothing.

```sh
ids="<kept ids, landing order>"
ver=<satelle.version>
d=$(mktemp -d) || exit 1
args=
for id in $ids; do
  satelle story attachment "$id" changelog >"$d/$id" 2>/dev/null || { echo "no changelog attachment on $id: stop the run" >&2; rm -rf "$d"; exit 1; }
  args="$args $id:$d/$id"
done
if awk -v h="## [$ver] - " 'index($0, h) == 1 { f = 1 } END { exit !f }' "$wt/CHANGELOG.md"; then
  echo "CHANGELOG.md already has the [$ver] section (a resumed run)"
else
  { bash "$wt/scripts/changelog-items.sh" render "$ver" "$(date -u +%Y-%m-%d)" $args && cat "$wt/CHANGELOG.md"; } >"$d/CHANGELOG.md" || { echo "writing the [$ver] section failed: stop the run" >&2; rm -rf "$d"; exit 1; }
  cat "$d/CHANGELOG.md" >"$wt/CHANGELOG.md"
  echo "CHANGELOG.md: the [$ver] section written from $ids"
fi
rm -rf "$d"
```

Commit only `.version` and `CHANGELOG.md`, so the tested tree is otherwise
unchanged: `chore(release): v<ver>, satelled <sver> (<exe>: <ids>)`. Skip the
commit when nothing changed (a resumed run). Step 4's guard excludes exactly
these two files; a file added to the bump is added to both lists.

## 4. Push main once

Pushes only the tested candidate and the bump on top of it. It stops with
"the candidate changed after the suite ran" when the suite has not passed on
this candidate (`$rd/tested` is missing), the tested commit is not under HEAD,
or HEAD differs from it outside `.version` and `CHANGELOG.md`.

```sh
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
tested=$(cat "$rd/tested" 2>/dev/null)
if [ -z "$tested" ] || ! git -C "$wt" merge-base --is-ancestor "$tested" HEAD ||
  ! git -C "$wt" diff --quiet "$tested" HEAD -- . ':!.version' ':!CHANGELOG.md'; then
  echo "the candidate changed after the suite ran (or the suite has not passed on it): stop the run" >&2
  exit 1
fi
git -C "$wt" push origin HEAD:refs/heads/main || exit 1   # a plain push: refused unless it fast-forwards; never forced
echo "main is $(git -C "$wt" rev-parse HEAD)"
git worktree remove --force "$wt"   # pushed; --force only drops the suite's build output
rm -rf "$rd"
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

- per item: id, PR number, head sha, and how it landed (fast-forward, merged or contained) or sent back with the reason
- the candidate sha and the `make test` result (with the log's tail when it failed)
- main before and after, and the bump commit
- the version section written (its heading and the ids it names)
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
