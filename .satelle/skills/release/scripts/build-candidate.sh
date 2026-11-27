session=<session>
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
held=$(cat <<'HELD'
<each "<id>: send back: <reason>" line the changelog verdicts printed>
HELD
)
git fetch -q origin --tags || { echo "git fetch origin failed" >&2; exit 1; }
[ ! -e "$wt" ] || git worktree remove --force "$wt"
rm -rf "$rd" && mkdir -p "$rd" || exit 1
git worktree add -q --detach "$wt" origin/main || exit 1
: >"$rd/kept"
: >"$rd/released"
printf '%s\n' "$held" | sed -n 's/^\([a-z]*_[0-9a-f]*\): send back: /\1\t/p' >"$rd/sendback"
prs=$(gh pr list --state open --base main --json number,headRefOid,title --limit 100) || { echo "gh pr list failed" >&2; exit 1; }
mprs=$(gh pr list --state merged --base main --json number,headRefOid,title --limit 200) || { echo "gh pr list failed" >&2; exit 1; }
kept=
for id in $(satelle ledger list "$exe" --kind task.run --json --session "$session" | jq -r '.[0].payload.inputs[]'); do
  st=$(satelle story get "$id" --json --session "$session" | jq -r .status)
  if [ "$st" != approved ]; then echo "$id: skipped: $st, not approved"; continue; fi
  if cut -f1 "$rd/sendback" | grep -qx "$id"; then echo "$id: sent back: its changelog"; continue; fi
  if [ -f "$rd.isolate/out" ] && grep -qx "$id" "$rd.isolate/out"; then echo "$id: left out: isolating the failed suite (trial $(cat "$rd.isolate/trial"))"; continue; fi
  head=$(printf '%s' "$prs" | jq -r --arg id "($id)" '[.[] | select(.title | endswith($id))][0].headRefOid // empty')
  if [ -z "$head" ]; then
    mhead=$(printf '%s' "$mprs" | jq -r --arg id "($id)" '[.[] | select(.title | endswith($id))][0].headRefOid // empty')
    [ -n "$mhead" ] && git -C "$wt" merge-base --is-ancestor "$mhead" origin/main 2>/dev/null && head=$mhead
  fi
  if [ -z "$head" ]; then
    printf '%s\tno open pull request against main has a title ending (%s); merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force), push, open one and come through integration again\n' "$id" "$id" >>"$rd/sendback"
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
    printf '%s\thead %s conflicts with the release candidate after %s in: %s; merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force), resolve, and come through integration again\n' \
      "$id" "$head" "${kept:-origin/main}" "${paths:-(the merge failed without unmerged paths)}" >>"$rd/sendback"
    echo "$id: sent back: head $head conflicts in: $paths"; continue
  fi
  echo "$id $head" >>"$rd/kept"; kept="${kept:+$kept }$id"
  rel=
  [ "$how" = contained ] && rel=$(git -C "$wt" tag -l 'v*' --contains "$head" | sort -V | head -n1)
  if [ -n "$rel" ]; then echo "$id $rel" >>"$rd/released"; echo "$id: contained, released in $rel head $head"; else echo "$id: $how head $head"; fi
done
[ -s "$rd/ff" ] || git -C "$wt" rev-parse HEAD >"$rd/ff"
[ -s "$rd/kept" ] || { echo "nothing lands: send back (step 2c) and stop the run" >&2; exit 1; }
echo "candidate $(git -C "$wt" rev-parse HEAD) at $wt"
