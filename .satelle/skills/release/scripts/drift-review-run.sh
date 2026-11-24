session=<session>
cd <the main checkout>
git fetch -q origin --tags
task=tas_a45ba066
ver=$(sed -n 's/^satelle\.version: *//p' .version)
prev=$(git tag -l 'v*' | sed 's/^v//' | sort -V | awk -v v="$ver" '$0 == v { print p; exit } { p = $0 }')
head=$(git rev-parse HEAD); tag=$(git rev-parse -q --verify "v$ver^{commit}")
if [ -n "$ver" ] && [ -n "$tag" ] && [ "$head" = "$tag" ]; then
  [ -n "$prev" ] || { echo "no v* tag before v$ver: nothing to judge the release from" >&2; exit 1; }
elif [ -n "$ver" ] && [ -n "$tag" ] && git merge-base --is-ancestor "$tag" HEAD && [ "$head" = "$(git rev-parse origin/main)" ]; then
  prev=$ver
  satelle task get "$task" --session "$session" | grep -q 'binary-unchanged' || { echo "the drift review task $task does not state the head of a binary-unchanged release: restate its body (plan of sto_da9775c1, Configuration to apply) before this run" >&2; exit 1; }
else
  echo "the main checkout is neither at v$ver nor at origin/main ahead of it: the drift review judges the released tree or the main a binary-unchanged release fast-forwarded to" >&2; exit 1
fi
exe=$(satelle task run "$task" --base "v$prev" --json --session "$session" | jq -r '.id')
satelle execution set "$exe" --status in_progress --session "$session"
satelle execution seat "$exe" --base "v$prev" --session "$session"
[ "$(satelle execution diff "$exe" --json --session "$session" | jq -r .base)" = "$(git rev-parse "v$prev^{commit}")" ] || { echo "run $exe is not judged from v$prev" >&2; exit 1; }
