session=<session>
task=tas_42301d36
que=release
git fetch -q origin
satelle queue get "$que" --json --session "$session" 2>/dev/null | jq -e --arg t "$task" '.trigger.task == $t' >/dev/null || { echo "release queue $que (.satelle/queues/$que.toml) is missing or does not trigger $task; a satelle older than the build origin/main carries cannot read queue files: install that build (step 7), then start again" >&2; exit 1; }
satelle task get "$task" --session "$session" | grep -q 'release-binary' || { echo "the release task $task does not name the run's release-binary record: restate its body (plan of sto_da9775c1, Configuration to apply) before firing" >&2; exit 1; }
fired=$(satelle queue fire "$que" --json --session "$session") || exit $?
exe=$(printf '%s\n' "$fired" | jq -r '.run // empty')
[ -n "$exe" ] || { echo "release queue $que has nothing to fire" >&2; exit 1; }
satelle execution set "$exe" --status in_progress --session "$session"
satelle execution seat "$exe" --session "$session"
satelle task get "$task" --session "$session" | satelle execution attach "$exe" --name task --type text/plain --file - --session "$session" || exit 1
satelle ledger list "$exe" --kind task.run --json --session "$session" | jq -r '.[0].payload.inputs[]'
