---
name: satelle-pr-approval-check
type: check
description: The pr-approval gate (sto_ebcaa5aa; sto_efe81afd puts it on integration → approved after pr-ready). Passes only on the satelle GitHub app's approving review on the item's pull request head, read from GitHub and matched by the app account's numeric id, never its login, whose review_id the item's ledger records as a github.review APPROVE on that head (sto_952d4435). A review of an older head does not count, nor does one GitHub re-pointed from an older head. When the app's latest decision on the head is CHANGES_REQUESTED, attaches its body as pr-review and reopens the item at in_progress. Deterministic, no agent; reads GitHub with gh.
---

# PR approval (the satelle GitHub app's review on the head)

The satelle GitHub app (app id 5379260, bot account `satelle[bot]`, id
344745317) publishes the `pr-review` gate's verdict on the pull request.
orbelle-reviewer (app 5563015, account 350948835) is internal and its reviews
never count. Approval is read back from GitHub, not from satelle.dev.

- **Selection.** The pull request is selected as pr-ready selects it: open, not
  a draft, against main, head == HEAD, and a title ending `(<item id>)`.
- **Matching.** A review names its author as an account, not an app. The match
  is on that account's numeric id (`user.id`, `app_account` below), which
  survives the app's rename; the login is never compared.
- **The decision.** Of the app's reviews on HEAD (`commit_id`), the latest
  APPROVED, CHANGES_REQUESTED or DISMISSED one decides. A COMMENTED review
  decides nothing, and a review of an older head does not count.
  - APPROVED passes only when the item's ledger records that review's id as
    a `github.review` APPROVE with `commit_sha` equal to HEAD, the event the
    publish check (`satelle-pr-review-publish`) appends. GitHub re-points an
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
  - DISMISSED, or no review, rejects and never reopens.

`app_account` is the owner of this value: release.md step 0 reads it from the
line below.

```sh
#!/usr/bin/env bash
set -u
app=5379260
app_account=344745317
for tool in gh jq git satelle sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the review cannot be read"; exit 1; }
done
head=$(git rev-parse HEAD)
short=${head:0:12}
pr=$(gh pr list --state open --base main --json number,headRefOid,title,isDraft --limit 100 |
  jq -r --arg h "$head" --arg id "($SATELLE_ITEM)" '[.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0].number // empty')
if [ -z "$pr" ]; then
  echo "no open pull request against main has head $short and a title ending ($SATELLE_ITEM): gh pr create --base main --title \"<title> ($SATELLE_ITEM)\""
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
    sum=$(printf '%s' "$body" | sha256sum | cut -d' ' -f1)
    latest=$(satelle story attachments "$SATELLE_ITEM" --json | jq -r '[.[] | select(.name == "pr-review")] | last | .sha256 // empty')
    if [ "$latest" != "$sum" ]; then
      printf '%s' "$body" | satelle story attach "$SATELLE_ITEM" --name pr-review --type text/markdown --file - >/dev/null ||
        { echo "the satelle app requested changes on head $short ($url); could not attach pr-review"; exit 1; }
    fi
    status=$(satelle story get "$SATELLE_ITEM" --json | jq -r '.status')
    if [ "$status" = "$SATELLE_FROM" ]; then
      satelle story set "$SATELLE_ITEM" --status in_progress >/dev/null ||
        { echo "the satelle app requested changes on head $short ($url); pr-review attached; could not reopen at in_progress"; exit 1; }
    fi
    echo "the satelle app requested changes on head $short ($url); pr-review attached; reopened at in_progress"
    exit 1
    ;;
  *)
    echo "no approving review by the satelle app (account $app_account, app $app) on PR #$pr head $short"
    exit 1
    ;;
esac
```
