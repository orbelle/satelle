session=<session>
cd <the main checkout>
git fetch -q origin
git merge --ff-only origin/main || { echo "the main checkout cannot fast-forward to origin/main: the cleanup run's base must be in its history" >&2; exit 1; }
task=tas_b3424a1c
exe=$(satelle task run "$task" --base origin/main --json --session "$session" | jq -r '.id // empty')
[ -n "$exe" ] || { echo "could not start a run of $task" >&2; exit 1; }
satelle execution set "$exe" --status in_progress --session "$session"
satelle execution seat "$exe" --base origin/main --session "$session"
satelle task get "$task" --session "$session" | satelle execution attach "$exe" --name task --type text/plain --file - --session "$session" || exit 1
