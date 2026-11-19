---
name: satelle-pr-approval-check
type: check
description: The approval gate, on the move out of review. Passes only on the satelle GitHub app's approving review on the item's pull request head, read from GitHub and matched by the app account's numeric id, never its login, whose review_id the item's ledger records as a github.review APPROVE on that head. A review of an older head does not count, nor does one GitHub re-pointed from an older head. When the app's latest decision on the head is CHANGES_REQUESTED, attaches its body as the `pr-review` attachment and reopens the item at in_progress. The pull request is selected against the base branch, read as satelle-pr-ready-check reads it. Deterministic, no agent; reads GitHub with gh.
---

# PR approval (the satelle GitHub app's review on the head)

The satelle GitHub app (app id 5379260, bot account `satelle[bot]`, id
344745317) publishes the verdict of the pull-request review task on the pull request;
the waiting state's `waits` text names that task.
orbelle-reviewer (app 5563015, account 350948835) is internal and its reviews
never count. Approval is read back from GitHub, not from satelle.dev.

- **Selection.** The pull request is selected as `satelle-pr-ready-check`
  selects it: open, not a draft, against the base branch, head == HEAD, and a
  title ending `(<item id>)`. The base branch is the trigger base of the queue
  the item's workflow enqueues into, read as `satelle-pr-ready-check` reads it
  (the lines between `# base branch: begin` and `# base branch: end` are the
  same in both).
- **Matching.** A review names its author as an account, not an app. The match
  is on that account's numeric id (`user.id`, `app_account` below), which
  survives the app's rename; the login is never compared.
- **The decision.** Of the app's reviews on HEAD (`commit_id`), the latest
  APPROVED, CHANGES_REQUESTED or DISMISSED one decides. A COMMENTED review
  decides nothing, and a review of an older head does not count.
  - APPROVED passes only when the item's ledger records that review's id as
    a `github.review` APPROVE with `commit_sha` equal to HEAD, the event the
    review task's publish check (`satelle-pr-review-run-publish`) appends. GitHub re-points an
    older approval's `commit_id` to a new head when the branch moves
    (`tests/testdata/github/reviews/README.md`, "GitHub re-points an older
    review's commit_id"), so `commit_id` alone is not trusted. An approval
    the ledger records on another head is refused naming both heads; one the
    ledger does not record is refused too. Neither refusal attaches anything
    or moves the item.
  - CHANGES_REQUESTED attaches the review body as `pr-review` (unless the
    newest `pr-review` already holds it) and moves the item, still at
    `$SATELLE_FROM`, to `in_progress` on the rework edge, then rejects. The
    coder's next run reads it.
  - DISMISSED, or no review, rejects and never reopens; with no review the
    notes quote the waiting state's `waits` text, naming the review task to
    run, and the item waits where it is.

`app_account` is the owner of this value: release.md step 0 reads it from the
line below.

```sh
#!/usr/bin/env bash
set -u
app=5379260
app_account=344745317
for tool in gh jq git satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the review cannot be read"; exit 1; }
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256; }
else
  echo "neither sha256sum nor shasum is installed, so the review cannot be read"; exit 1
fi
story=$SATELLE_ITEM
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
waits=$(printf '%s' "$route" | jq -r --arg s "$SATELLE_FROM" '[.states[]? | select(.name == $s) | .waits // empty][0] // empty')
head=$(git rev-parse HEAD)
short=${head:0:12}
pr=$(gh pr list --state open --base "$branch" --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($SATELLE_ITEM)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against $branch has head $short and a title ending ($SATELLE_ITEM): gh pr create --base $branch --title \"<title> ($SATELLE_ITEM)\""
  exit 1
fi
reviews=$(gh api "repos/{owner}/{repo}/pulls/$pr/reviews" --paginate) || { echo "could not read the reviews of PR #$pr"; exit 1; }
review=$(printf '%s' "$reviews" | jq -s -c --argjson app "$app_account" --arg h "$head" '
  (add // []) | [.[] | select(.user.id == $app and .commit_id == $h and
    (.state == "APPROVED" or .state == "CHANGES_REQUESTED" or .state == "DISMISSED"))]
  | sort_by(.submitted_at) | last // empty')
state=$(printf '%s' "$review" | jq -r '.state // empty')
url=$(printf '%s' "$review" | jq -r '.html_url // empty')
case "$state" in
  APPROVED)
    rid=$(printf '%s' "$review" | jq -r '.id')
    events=$(satelle ledger list "$SATELLE_ITEM" --kind github.review --json) ||
      { echo "could not read the item's github.review events"; exit 1; }
    approvals=$(printf '%s' "$events" | jq -c --argjson rid "$rid" '[(. // [])[] | .payload | select(.event == "APPROVE" and .review_id == $rid)]') ||
      { echo "could not read the item's github.review events"; exit 1; }
    if printf '%s' "$approvals" | jq -e --arg h "$head" 'any(.[]; .commit_sha == $h)' >/dev/null; then
      echo "approved by the satelle app (account $app_account, app $app) on PR #$pr head $short: $url"
      exit 0
    fi
    given=$(printf '%s' "$approvals" | jq -r 'last | .commit_sha // empty')
    if [ -n "$given" ]; then
      echo "refused: the satelle app's approval $url (review $rid) now reports head $short, but the item's ledger records review $rid on head ${given:0:12}; GitHub re-pointed an older approval"
    else
      echo "refused: the satelle app's approval $url (review $rid) on head $short is not on the item's ledger as a github.review APPROVE for that head"
    fi
    exit 1
    ;;
  CHANGES_REQUESTED)
    body=$(printf '%s' "$review" | jq -r '.body // ""')
    sum=$(printf '%s' "$body" | sha256 | cut -d' ' -f1)
    latest=$(satelle story attachments "$SATELLE_ITEM" --json | jq -r '[.[] | select(.name == "pr-review")] | last | .sha256 // empty')
    if [ "$latest" != "$sum" ]; then
      printf '%s' "$body" | satelle story attach "$SATELLE_ITEM" --name pr-review --type text/markdown --file - >/dev/null ||
        { echo "the satelle app requested changes on head $short ($url); could not attach its body"; exit 1; }
    fi
    status=$(satelle story get "$SATELLE_ITEM" --json | jq -r '.status')
    if [ "$status" = "$SATELLE_FROM" ]; then
      satelle story set "$SATELLE_ITEM" --status in_progress >/dev/null ||
        { echo "the satelle app requested changes on head $short ($url); its body attached; could not reopen at in_progress"; exit 1; }
    fi
    echo "the satelle app requested changes on head $short ($url); its body attached; reopened at in_progress"
    exit 1
    ;;
  *)
    line="no approving review by the satelle app (account $app_account, app $app) on PR #$pr head $short: $SATELLE_ITEM waits at $SATELLE_FROM"
    if [ -n "$waits" ]; then
      line="$line for $waits"
    fi
    echo "$line"
    exit 1
    ;;
esac
```
