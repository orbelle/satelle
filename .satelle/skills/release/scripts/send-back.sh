session=<session>
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"
[ -f "$rd/sendback" ] || { echo "no $rd/sendback: build the candidate (step 2a) first" >&2; exit 1; }
while IFS=$'\t' read -r id reason <&3; do
  [ -n "$id" ] || continue
  if [ "$(satelle story get "$id" --json --session "$session" | jq -r .status)" != approved ]; then echo "$id: not approved, not moved"; continue; fi
  printf '%s\n' "$reason" | satelle story attach "$id" --name release-sendback --file - --session "$session" || exit 1
  satelle story set "$id" --status in_progress --session "$session" || exit 1
  echo "$id: sent back: $reason"
done 3<"$rd/sendback"
: >"$rd/sendback"
