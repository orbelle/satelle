---
name: satelle-pr-review-publish
type: check
description: The after check of the pr-review gate (sto_ebcaa5aa). Publishes the repo's own judge verdict on the item's pull request head as the satelle GitHub app, through satelle.dev (satelle github review); accept or carried is APPROVE, reject is REQUEST_CHANGES. Records what was published as a github.review event. On a reject it attaches the body as pr-review and reopens the item at in_progress. Never publishes an approval over requested changes on the same head. Deterministic, no agent; reads GitHub with gh.
---

# PR review publish (the after of pr-review)

The `pr-review` gate is the repo's own judge: the code-ac skill on
`reviewer-gemini-3.8-flash-high`, the same model, prompt and inputs as `code-ac`. satelle.dev
decides nothing. It publishes what this check sends, on the repository the
project is bound to by the satelle GitHub app. This check runs once the judge
has a verdict, with `SATELLE_GATE`, `SATELLE_GATE_RESULT` and
`SATELLE_GATE_BINDING` set and the judge's notes on stdin.

1. The pull request is selected as pr-ready selects it: open, not a draft,
   against main, head == HEAD, and a title ending `(<item id>)`. The server
   never parses titles. Without such a PR the check fails, and the gate
   rejects.
2. The event follows the verdict: accept or carried is `APPROVE`, reject is
   `REQUEST_CHANGES`. The body's first line names the project, the gate, its
   binding, the item and the head. The judge's notes follow.
3. The item's own `github.review` events (`satelle ledger list <id> --kind
   github.review --session <session>`) decide whether to publish on this head. Only an approval is
   ever withheld:
   - an `APPROVE` after a `REQUEST_CHANGES` on the same head is not published
     (never override requested changes; the approval check reads the app's
     review on GitHub and reopens);
   - an `APPROVE` already published on the head is not published again;
   - a `REQUEST_CHANGES` with the same body already published on the head is
     not published again, but still attaches and reopens;
   - any other `REQUEST_CHANGES` is published, including a repeat reject of the
     same head with different findings.
4. After publishing, `github.review` is appended to the item with `event`,
   `pull_number`, `commit_sha`, `review_id`, `html_url`, `repository` and
   `body_sha256`. It is a ledger event, not an attachment, so the judge's
   coverage (which hashes attachments) still carries on a re-present.
5. On every reject the body is attached as `pr-review` (unless the newest
   `pr-review` already holds it), and the item, still at `$SATELLE_FROM`, moves
   to `in_progress` on the rework edge. The coder's next run reads it from the
   record. The gate's result stays reject. This check fails only when it could
   not publish, attach or move, so the notes say why.

```sh
#!/usr/bin/env bash
set -u
for tool in gh jq git satelle sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the review cannot be published"; exit 1; }
done
in=$(cat)
result=$(printf '%s' "$in" | jq -r '.result // empty')
notes=$(printf '%s' "$in" | jq -r '.notes // empty')
head=$(git rev-parse HEAD)
short=${head:0:12}
pr=$(gh pr list --state open --base main --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($SATELLE_ITEM)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against main has head $short and a title ending ($SATELLE_ITEM), so the verdict cannot be published: gh pr create --base main --title \"<title> ($SATELLE_ITEM)\""
  exit 1
fi
case "$result" in
  accept|carried) event=APPROVE; verdict=approve ;;
  reject) event=REQUEST_CHANGES; verdict="request changes" ;;
  *) echo "unexpected gate result '$result'"; exit 1 ;;
esac
slug=$(satelle project show 2>&1 | sed -n 's/^slug: *//p')
[ -n "$slug" ] || { echo "satelle project show named no project: bind this repo (satelle project bind <project> --session <session>)"; exit 1; }
body=$(printf 'satelle · project %s · gate %s (%s) on %s head %s: %s\n\n%s' "$slug" "$SATELLE_GATE" "$SATELLE_GATE_BINDING" "$SATELLE_ITEM" "$short" "$verdict" "$notes")
sum=$(printf '%s' "$body" | sha256sum | cut -d' ' -f1)
recorded=$(satelle ledger list "$SATELLE_ITEM" --kind github.review --json | jq -c --arg h "$head" '[.[] | .payload | select(.commit_sha == $h)]') ||
  { echo "could not read the item's github.review events"; exit 1; }
url=
if [ "$event" = APPROVE ]; then
  if printf '%s' "$recorded" | jq -e 'any(.[]; .event == "REQUEST_CHANGES")' >/dev/null; then
    echo "not publishing an approval: changes were requested on head $short; the approval check reads the app's review and reopens"
    exit 0
  fi
  url=$(printf '%s' "$recorded" | jq -r '[.[] | select(.event == "APPROVE")] | last | .html_url // empty')
  if [ -n "$url" ]; then
    echo "approval already published on head $short ($url)"
    exit 0
  fi
else
  url=$(printf '%s' "$recorded" | jq -r --arg s "$sum" '[.[] | select(.event == "REQUEST_CHANGES" and .body_sha256 == $s)] | last | .html_url // empty')
fi
if [ -z "$url" ]; then
  out=$(printf '%s' "$body" | satelle github review --pr "$pr" --sha "$head" --event "$event" --body-file - --json 2>&1) ||
    { echo "publishing the review failed: $out"; exit 1; }
  url=$(printf '%s' "$out" | jq -r '.html_url')
  payload=$(printf '%s' "$out" | jq -c --arg e "$event" --argjson pr "$pr" --arg sha "$head" --arg sum "$sum" \
    '{event: $e, pull_number: $pr, commit_sha: $sha, review_id: .review_id, html_url: .html_url, repository: .repository, body_sha256: $sum}')
  satelle ledger append "$SATELLE_ITEM" --kind github.review --payload "$payload" >/dev/null ||
    { echo "published $url but could not record github.review"; exit 1; }
fi
if [ "$event" = APPROVE ]; then
  echo "published approval $url"
  exit 0
fi
latest=$(satelle story attachments "$SATELLE_ITEM" --json | jq -r '[.[] | select(.name == "pr-review")] | last | .sha256 // empty')
if [ "$latest" != "$sum" ]; then
  printf '%s' "$body" | satelle story attach "$SATELLE_ITEM" --name pr-review --type text/markdown --file - >/dev/null ||
    { echo "requested changes ($url) but could not attach pr-review"; exit 1; }
fi
status=$(satelle story get "$SATELLE_ITEM" --json | jq -r '.status')
if [ "$status" = "$SATELLE_FROM" ]; then
  satelle story set "$SATELLE_ITEM" --status in_progress >/dev/null ||
    { echo "requested changes ($url); pr-review attached; could not reopen at in_progress"; exit 1; }
fi
echo "requested changes ($url); pr-review attached; reopened at in_progress"
```
