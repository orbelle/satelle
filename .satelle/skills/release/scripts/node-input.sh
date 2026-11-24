session=<session>
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
[ -d "$wt" ] && [ -s "$rd/ff" ] || { echo "no candidate at $wt: build it (step 2a) first" >&2; exit 1; }
cand=$(git -C "$wt" rev-parse HEAD)
[ "$(cat "$rd/drift-checked" 2>/dev/null)" = "$cand" ] || { echo "the drift gate has not passed on the candidate $cand: run ### Drift gate on the candidate first" >&2; exit 1; }
git fetch -q origin || { echo "git fetch origin failed" >&2; exit 1; }
carried=$(bash "$wt/scripts/release-binary.sh" carried "$task" "$wt" "$cand") || { echo "cannot list the items earlier binary-unchanged runs carried: stop the run" >&2; exit 1; }
in="$rd/node-in"
rm -rf "$in" "$rd/node-out" && mkdir -p "$in/changelog" || exit 1
ids=
for id in $carried $(cut -d' ' -f1 "$rd/kept"); do
  case " $ids " in *" $id "*) continue ;; esac
  awk -v id="$id" '$1 == id { f = 1 } END { exit !f }' "$rd/released" 2>/dev/null && continue
  ids="${ids:+$ids }$id"
done
for id in $ids; do
  satelle story attachment "$id" changelog --session "$session" >"$in/changelog/$id" 2>/dev/null ||
    { rm -f "$in/changelog/$id"; echo "no changelog attachment on $id: a binary-changed run stops at the node's bump" >&2; }
done
printf '%s\n' $ids >"$in/notes-ids"
cp "$rd/kept" "$in/kept" || exit 1
repo=$(git config --get remote.origin.url | sed 's#^git@github\.com:#https://github.com/#')
ident=$(git var GIT_AUTHOR_IDENT) || { echo "git has no author identity" >&2; exit 1; }
{
  echo "run: $exe"
  echo "candidate: $cand"
  echo "drift-checked: $cand"
  echo "branch: release/$exe"
  echo "ff: $(cat "$rd/ff")"
  echo "base: $(git rev-parse origin/main)"
  echo "repo: $repo"
  echo "author: ${ident%% <*}"
  echo "email: $(printf '%s\n' "$ident" | sed -n 's/.*<\(.*\)>.*/\1/p')"
  echo "mode: release"
} >"$in/run"
if grep -rlE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' "$rd/kept" "$rd/release-drift" "$in"; then
  echo "a token-shaped string is in the node's input (the files above): stop the run" >&2; exit 1
fi
satelle execution attach "$exe" --name release-kept --type text/plain --file "$rd/kept" --session "$session" || exit 1
satelle execution attach "$exe" --name release-drift --type text/plain --file "$rd/release-drift" --session "$session" || exit 1
if [ -n "$(git ls-remote origin "refs/heads/release/$exe")" ]; then git push -q origin --delete "release/$exe" || exit 1; fi
git push -q origin "$cand:refs/heads/release/$exe" || exit 1
echo "the node's input is $in; release/$exe is the candidate $cand"
