# Pull-request review

A task a session other than the author's runs, on demand, for one story whose
pull request is open: the story's pull request head is judged cold, from a
checkout that is not the author's worktree, and the verdict is published on
that head as the satelle GitHub app. Its runs follow
`.satelle/workflows/pr-review.toml`: the judge is the one the delivery
workflow's pr-review gate binds (`satelle workflow show pr-review --session
<session>`), and `satelle-pr-review-run-publish` publishes and records the
verdict as a `github.review` event on the story's own chain.

The author's session reads the result through `satelle-pr-approval-check`, as
it does today: an approval passes it; requested changes attach the review as
`pr-review` and reopen the story there. The review run never moves the story.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Review a story'"'"'s pull request head") | .id'
```

When it prints nothing, create it once, from the main checkout:

```sh
session=<session>
satelle task create --title "Review a story's pull request head" --input-kind story --body - --session "$session" <<'EOF'
A story's open pull request is judged cold by a session other than the author's, from a checkout of its head that is not the author's worktree.

ACTION: follow .satelle/tasks/pr-review.md, section "Run it", for the one story the session names: check out its pull request head apart from the author's worktree, tag the run pr-review, reviews:<story> and head:<sha>, reselect it into the pr-review workflow, give it the story's record, seat it there on origin/main and present it done.
VERIFICATION: the run's pr-review gate.run holds the judge's verdict on that head, the satelle app's review of the head is on the pull request, and the story carries the github.review event satelle-pr-review-run-publish appended for it.
EOF
```

Its input selection takes every story; a run reviews only the one story its
`reviews:` tag names.

## Run it

`<story>` is the reviewed story. Run this as a session that did not author
the story's work, from the main checkout:

```bash
session=<session>
story=<story>
task=<the task id found above>
git fetch origin
pr=$(gh pr list --state open --base main --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg id "($story)" '[.[] | select(.isDraft == false and (.title | endswith($id)))][0] | "\(.number) \(.headRefOid)"')
read -r num head <<<"$pr"
[ -n "$head" ] && [ "$head" != null ] || { echo "no open pull request titled (… $story)" >&2; exit 1; }
dir=$(git rev-parse --show-toplevel)/../review-$story-${head:0:12}
git fetch origin "pull/$num/head"
git worktree add --detach "$dir" "$head"
exe=$(satelle task run "$task" --json --session "$session" | jq -r '.id // empty')
[ -n "$exe" ] || { echo "could not start a run of $task" >&2; exit 1; }
satelle execution set "$exe" --add-tag pr-review --add-tag "reviews:$story" --add-tag "head:$head" --session "$session"
satelle -C "$dir" execution set "$exe" --reselect pr-review --session "$session"
```

Give the run the story's record. The judge's skill reads the criteria in its
prompt (the run's) and the `plan` and `ac-evidence` attachments of its record
folder, so the run carries the story's under the same names:

```bash
crit=()
while IFS= read -r c; do crit+=(--criteria "$c"); done < <(satelle story get "$story" --json --session "$session" | jq -r '.criteria[]')
satelle story get "$story" --json --session "$session" | jq -r '.body' | satelle execution set "$exe" --title "Review of $story pull request #$num, head ${head:0:12}" --body - "${crit[@]}" --replace --session "$session"
satelle story get "$story" --session "$session" | satelle execution attach "$exe" --name reviewed-item --type text/plain --file - --session "$session"
for name in plan ac-evidence; do
  if satelle story attachments "$story" --json --session "$session" | jq -e --arg n "$name" 'any(.[]; .name == $n)' >/dev/null; then
    satelle story attachment "$story" "$name" --session "$session" | satelle execution attach "$exe" --name "$name" --type text/markdown --file - --session "$session"
  fi
done
```

Seat the run in the review checkout on `origin/main`, so the judge sees the
pull request's change, and present it done:

```bash
satelle -C "$dir" execution seat "$exe" --base origin/main --session "$session"
satelle -C "$dir" execution set "$exe" --status done --session "$session"
```

An accept moves the run to done with the approval published. A reject leaves
the run at backlog with its verdict on its gate.run and the request for changes
published; close it with `satelle execution set "$exe" --status cancelled
--session "$session"`. Either way release the seat
(`satelle -C "$dir" execution release "$exe" --session "$session"`) and
remove the review checkout (`git worktree remove "$dir"`).
