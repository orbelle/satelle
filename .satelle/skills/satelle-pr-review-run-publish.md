---
name: satelle-pr-review-run-publish
type: check
description: The after check of the review workflow's gate. Publishes the judge's verdict on the reviewed story's pull request head as the satelle GitHub app (satelle github review) and records it as a github.review event on the story's own chain, where satelle-pr-approval-check reads it. The story and head come from the run's tags (reviews:<story>, head:<sha>), never from SATELLE_ITEM or the checkout alone. The pull request is selected against the base branch, read as satelle-pr-ready-check reads it. It only publishes and records: on requested changes it neither attaches to nor moves the story; the approval check, in the author's session, does that. Deterministic, no agent; reads GitHub with gh.
---

# PR review publish, from a review run

The run is an execution of the pull-request review task (the one the
story's waiting state's `waits` text names), so `SATELLE_ITEM` is the run, not
the story.
Its tags carry the input: `reviews:<story>` names the reviewed story and
`head:<sha>` the pull request head the run was started for.

1. Both tags must be present, once each. The checkout's HEAD must be that
   head, so what the judge read is what is published on.
2. The pull request is selected as satelle-pr-ready-check selects it: open,
   not a draft, against the base branch, head == the tagged head, and a title
   ending `(<story>)`. The base branch is the trigger base of the queue the
   story's workflow enqueues into, read as satelle-pr-ready-check reads it
   (the lines between `# base branch: begin` and `# base branch: end` are the
   same in both).
3. accept is `APPROVE`, reject is `REQUEST_CHANGES`. The body's first line
   names the project, the gate, its binding, the story, the head and the run;
   the judge's notes follow.
4. The story's own `github.review` events on the head decide whether to
   publish: an approval is never published over requested changes on the same
   head (the approval check reads the app's review and reopens), nor twice; a
   request for changes with the same body is not published again; any other
   request for changes is published, including a repeat on the same head with
   different findings.
5. After publishing, `github.review` is appended to the story with `event`,
   `pull_number`, `commit_sha`, `review_id`, `html_url`, `repository` and
   `body_sha256`. It is a ledger event, not an attachment, so the story's
   judged coverage (which hashes attachments) is unchanged.

It fails only when it cannot read its input or cannot publish or record, and
then the run does not reach done.

```sh
#!/usr/bin/env bash
set -u
for tool in gh jq git satelle sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the review cannot be published"; exit 1; }
done
in=$(cat)
result=$(printf '%s' "$in" | jq -r '.result // empty')
notes=$(printf '%s' "$in" | jq -r '.notes // empty')
tags=$(satelle execution get "$SATELLE_ITEM" --json | jq -r '.tags[]?') ||
  { echo "could not read the run $SATELLE_ITEM"; exit 1; }
story=$(printf '%s\n' "$tags" | sed -n 's/^reviews://p')
head=$(printf '%s\n' "$tags" | sed -n 's/^head://p')
if [ "$(printf '%s' "$story" | grep -c .)" != 1 ] || [ "$(printf '%s' "$head" | grep -c .)" != 1 ]; then
  echo "the run $SATELLE_ITEM needs exactly one reviews:<story> tag and one head:<sha> tag"
  exit 1
fi
short=${head:0:12}
here=$(git rev-parse HEAD)
if [ "$here" != "$head" ]; then
  echo "this checkout is at ${here:0:12}, not the reviewed head $short: the judge read another tree, so nothing is published"
  exit 1
fi
# base branch: begin
route=$(satelle story route "$story" --json 2>&1) || { echo "could not read the route of $story: $route"; exit 1; }
wf=$(printf '%s' "$route" | jq -r '.workflow // empty')
bases=
for q in $(printf '%s' "$route" | jq -r '[.states[]?.enqueue[]?] | unique | .[]'); do
  b=$(satelle queue get "$q" --json 2>/dev/null | jq -r '.trigger.base // empty')
  [ -n "$b" ] && bases="$bases$q $b
"
done
n=$(printf '%s' "$bases" | awk 'NF { print $2 }' | sort -u | grep -c .)
if [ "$n" -eq 0 ]; then
  echo "the workflow $wf of $story enqueues into no queue with a trigger base, so the pull request's base branch is not configured: give the queue its states enqueue into base = \"<remote>/<branch>\" under [trigger]"
  exit 1
fi
if [ "$n" -gt 1 ]; then
  echo "the queues the workflow $wf of $story enqueues into name $n trigger bases ($(printf '%s' "$bases" | awk 'NF { printf "%s%s base %s", s, $1, $2; s = "; " }')), so the pull request's base branch is ambiguous: give them one base"
  exit 1
fi
base_queue=$(printf '%s' "$bases" | awk 'NF { print $1; exit }')
base_ref=$(printf '%s' "$bases" | awk 'NF { print $2; exit }')
case "$base_ref" in
  ?*/?*) remote=${base_ref%%/*} branch=${base_ref#*/} ;;
  *) echo "the queue $base_queue has trigger base $base_ref, which is not <remote>/<branch>, so the pull request's base branch cannot be read: set base = \"<remote>/<branch>\" under its [trigger]"; exit 1 ;;
esac
# base branch: end
pr=$(gh pr list --state open --base "$branch" --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($story)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against $branch has head $short and a title ending ($story), so the verdict cannot be published"
  exit 1
fi
case "$result" in
  accept|carried) event=APPROVE; verdict=approve ;;
  reject) event=REQUEST_CHANGES; verdict="request changes" ;;
  *) echo "unexpected gate result '$result'"; exit 1 ;;
esac
slug=$(satelle project show 2>&1 | sed -n 's/^slug: *//p')
[ -n "$slug" ] || { echo "satelle project show named no project: bind this repo (satelle project bind <project> --session <session>)"; exit 1; }
body=$(printf 'satelle · project %s · gate %s (%s) on %s head %s, review run %s: %s\n\n%s' "$slug" "$SATELLE_GATE" "$SATELLE_GATE_BINDING" "$story" "$short" "$SATELLE_ITEM" "$verdict" "$notes")
sum=$(printf '%s' "$body" | sha256sum | cut -d' ' -f1)
recorded=$(satelle ledger list "$story" --kind github.review --json | jq -c --arg h "$head" '[.[] | .payload | select(.commit_sha == $h)]') ||
  { echo "could not read the github.review events of $story"; exit 1; }
if [ "$event" = APPROVE ]; then
  if printf '%s' "$recorded" | jq -e 'any(.[]; .event == "REQUEST_CHANGES")' >/dev/null; then
    echo "not publishing an approval: changes were requested on head $short of $story"
    exit 0
  fi
  url=$(printf '%s' "$recorded" | jq -r '[.[] | select(.event == "APPROVE")] | last | .html_url // empty')
else
  url=$(printf '%s' "$recorded" | jq -r --arg s "$sum" '[.[] | select(.event == "REQUEST_CHANGES" and .body_sha256 == $s)] | last | .html_url // empty')
fi
if [ -n "$url" ]; then
  echo "already published on head $short of $story ($url)"
  exit 0
fi
out=$(printf '%s' "$body" | satelle github review --pr "$pr" --sha "$head" --event "$event" --body-file - --json 2>&1) ||
  { echo "publishing the review failed: $out"; exit 1; }
url=$(printf '%s' "$out" | jq -r '.html_url')
payload=$(printf '%s' "$out" | jq -c --arg e "$event" --argjson pr "$pr" --arg sha "$head" --arg sum "$sum" \
  '{event: $e, pull_number: $pr, commit_sha: $sha, review_id: .review_id, html_url: .html_url, repository: .repository, body_sha256: $sum}')
satelle ledger append "$story" --kind github.review --payload "$payload" >/dev/null ||
  { echo "published $url but could not record github.review on $story"; exit 1; }
echo "published $event on $story head $short: $url"
```
