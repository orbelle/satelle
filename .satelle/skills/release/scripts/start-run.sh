session=<session>
task=tas_42301d36
que=release
git fetch -q origin
satelle queue get "$que" --json --session "$session" 2>/dev/null | jq -e --arg t "$task" '.trigger.task == $t' >/dev/null || { echo "release queue $que (.satelle/queues/$que.toml) is missing or does not trigger $task; a satelle older than the build origin/main carries cannot read queue files: install that build (step 7), then start again" >&2; exit 1; }
satelle task get "$task" --session "$session" | grep -q 'release-binary' || { echo "the release task $task does not name the run's release-binary record: restate its body (plan of sto_da9775c1, Configuration to apply) before firing" >&2; exit 1; }
fire() { satelle queue fire "$que" --json --session "$session"; }
fired=$(fire)
rc=$?
if [ "$rc" -eq 2 ]; then
  shown=$(satelle queue show "$que" --json --session "$session") || exit 2
  woken=$(printf '%s\n' "$shown" | jq -r 'select(.last_fire.cause == "wake" and .last_run_status == "backlog") | .last_fire.run // empty')
  [ -n "$woken" ] || exit 2
  members=$(printf '%s\n' "$shown" | jq -c '[.members[].id]')
  qid=$(printf '%s\n' "$shown" | jq -r '.id')
  satelle ledger list "$woken" --json --session "$session" | jq -e --arg t "$task" --arg q "$qid" --argjson m "$members" '
    ([.[] | select(.kind == "item.created")] | length == 1 and all(.[]; .actor == "satelle-sync.service" and .payload.origin.role == "sync" and .payload.parent == $t))
    and ([.[] | select(.kind == "task.run")] | length == 1 and all(.[]; .actor == "satelle-sync.service" and .payload.origin.role == "sync" and .payload.task == $t and .payload.queue == $q and (.payload.inputs | length > 0) and (.payload.inputs - $m == [])))' >/dev/null || {
    echo "refusing $woken: the queue's last fire names it as woken, but its ledger lacks the proof that a sync pass which proved the ledger in sync started it (one item.created and one task.run by satelle-sync.service with origin role sync, under $task, for $qid, over members the queue still holds); nothing is fired and it is neither taken nor cancelled: inspect it (satelle ledger list $woken --session <session>) and cancel it by hand if it is not yours" >&2
    exit 2
  }
  if [ "$(satelle execution get "$woken" --json --session "$session" | jq -r '.base')" = "$(git rev-parse origin/main)" ]; then
    fired=$(jq -n --arg run "$woken" '{run: $run}')
  else
    echo "the woken run $woken's base is not origin/main: cancelling it and firing again" >&2
    satelle execution set "$woken" --status cancelled --session "$session" || exit 1
    fired=$(fire) || exit $?
  fi
elif [ "$rc" -ne 0 ]; then
  exit "$rc"
fi
exe=$(printf '%s\n' "$fired" | jq -r '.run // empty')
[ -n "$exe" ] || { echo "release queue $que has nothing to fire" >&2; exit 1; }
satelle execution set "$exe" --status in_progress --session "$session"
satelle execution seat "$exe" --session "$session"
satelle task get "$task" --session "$session" | satelle execution attach "$exe" --name task --type text/plain --file - --session "$session" || exit 1
satelle ledger list "$exe" --kind task.run --json --session "$session" | jq -r '.[0].payload.inputs[]'
