session=<session>
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
[ -d "$wt" ] && [ -s "$rd/ff" ] || { echo "no candidate at $wt: build it (step 2a) first" >&2; exit 1; }
rm -f "$rd/drift-checked"
cand=$(git -C "$wt" rev-parse HEAD)
mkdir -p "$rd/bin" || exit 2
go build -C "$wt" -o "$rd/bin/satelle" ./cmd/satelle || { echo "cannot build the candidate binary: stop the run" >&2; exit 2; }
SATELLE_BIN="$rd/bin/satelle" bash "$wt/scripts/drift-check.sh" "$wt" >"$rd/drift.txt"
code=$?
case $code in
  0)
    echo "$cand" >"$rd/drift-checked"
    { echo "drift-check exit 0 on $cand"; echo "fixed in this run:"; if [ -s "$rd/drift-fixed" ]; then cat "$rd/drift-fixed"; else echo none; fi; } >"$rd/release-drift"
    echo "the drift gate passed on the candidate $cand"
    ;;
  1)
    cat "$rd/drift.txt" >>"$rd/drift-fixed"
    cat "$rd/drift.txt"
    echo "the candidate $cand names what its binary lacks: fix each line in $wt, commit, and run this block again" >&2
    exit 1
    ;;
  *)
    echo "drift-check could not check the candidate (exit $code): stop the run" >&2
    exit 2
    ;;
esac
