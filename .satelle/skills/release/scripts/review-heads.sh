session=<session>
id=<id>
sub=${SATELLE_SUBSTRATE_DIR:-$(git rev-parse --show-toplevel)/.satelle}
app_account=$(sed -n 's/^app_account=\([0-9][0-9]*\).*/\1/p' "$sub/skills/satelle-pr-approval-check/SKILL.md")
[ -n "$app_account" ] || { echo "no app_account= line in $sub/skills/satelle-pr-approval-check/SKILL.md" >&2; exit 1; }
pr=$(gh pr list --state open --base main --json number,headRefOid,title --limit 100 |
  jq -c --arg id "($id)" '[.[] | select(.title | endswith($id))][0] // empty')
if [ -z "$pr" ]; then
  mpr=$(gh pr list --state merged --base main --json number,headRefOid,title --limit 200 |
    jq -c --arg id "($id)" '[.[] | select(.title | endswith($id))][0] // empty')
  mhead=$(printf '%s' "$mpr" | jq -r '.headRefOid // empty')
  if [ -n "$mhead" ] && git merge-base --is-ancestor "$mhead" origin/main 2>/dev/null; then
    echo "$id: PR #$(printf '%s' "$mpr" | jq -r .number) merged, head $mhead on origin/main: contained, no review to read"
  else
    echo "$id: no open pull request (send it back)"
  fi
  exit 0
fi
n=$(printf '%s' "$pr" | jq -r .number)
head=$(printf '%s' "$pr" | jq -r .headRefOid)
review=$(gh api "repos/{owner}/{repo}/pulls/$n/reviews" --paginate | jq -s -c --argjson app "$app_account" --arg h "$head" '
  (add // []) | [.[] | select(.user.id == $app and .commit_id == $h and
    (.state == "APPROVED" or .state == "CHANGES_REQUESTED" or .state == "DISMISSED"))]
  | sort_by(.submitted_at) | last // empty')
if [ "$(printf '%s' "$review" | jq -r '.state // empty')" = CHANGES_REQUESTED ]; then
  printf '%s' "$review" | jq -j '.body // ""' | satelle story attach "$id" --name pr-review --type text/markdown --file - --session "$session"
  satelle story set "$id" --status in_progress --session "$session"
  echo "$id: sent back: the satelle app requested changes on head $head ($(printf '%s' "$review" | jq -r .html_url))"
else
  echo "$id: PR #$n head $head: no changes requested by the satelle app"
fi
