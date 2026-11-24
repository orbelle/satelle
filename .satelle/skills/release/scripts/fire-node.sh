session=<session>
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"
cand=$(cat "$rd/drift-checked" 2>/dev/null)
[ -n "$cand" ] && [ -d "$rd/node-in" ] || { echo "no node input at $rd/node-in: run ### The node's input first" >&2; exit 1; }
[ "$(git rev-parse HEAD)" = "$cand" ] || { echo "check out the run's candidate first: git checkout --detach $cand" >&2; exit 1; }
ntask=$(sed -n 's/^task = "\(tas_[0-9a-f]*\)"$/\1/p' .satelle/remotes/release-sprite/remote.toml)
[ -n "$ntask" ] || { echo ".satelle/remotes/release-sprite/remote.toml names no [drive] task" >&2; exit 1; }
SATELLE_RELEASE_IN="$rd/node-in" SATELLE_RELEASE_OUT="$rd/node-out" satelle remote fire release-sprite "$ntask" --session "$session"
