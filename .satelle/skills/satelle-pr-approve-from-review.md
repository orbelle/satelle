---
name: satelle-pr-approve-from-review
type: check
description: Publishes the satelle GitHub app's approving review on the story's open pull request head from the accepted code-ac verdict that judged that exact change, and records it as a github.review event on the story's own chain, where satelle-pr-approval-check reads it. Refuses when no accepted code-ac verdict exists, when the change has moved since code-ac judged it, or when the pull request head is not the judged change. The pull request is selected against the base branch, read as satelle-pr-ready-check reads it. Deterministic, no agent; reads GitHub with gh.
---

# PR approve from code review

A pull request head that already passed the `code-ac` gate needs no second
judge. This check verifies that the open pull request head is the exact change
`code-ac` accepted, then publishes the satelle GitHub app's `APPROVE` review
on that head and records it as a `github.review` event on the story's own
chain.

1. The working tree must have no uncommitted changes to tracked files, so
   `HEAD` is the exact tree whose diff is compared.
2. The verdict is the `code-ac` `gate.run` satelle recorded on the story's
   latest move into `integration`: the last one before that
   `item.transitioned` event, which only a move appends. Any `code-ac`
   `gate.run` after it refuses the check, since a `gate.run` can be
   appended by hand. That verdict must be `accept` or `carried` and
   must have recorded a change. When `carried`, the binding and notes are read
   from the matching `code-ac` `accept` verdict.
3. The pull request is selected as `satelle-pr-ready-check` selects it: open,
   not a draft, against the base branch, and a title ending `(<story>)`. The
   base branch is the trigger base of the queue the story's workflow enqueues
   into, or, when it enqueues into no queue with one, the default branch of
   the git remote (`checkout.defaultRemote`, else the only remote), read as
   `satelle-pr-ready-check` reads it (the lines between `# base branch: begin`
   and `# base branch: end` are the same in both).
4. The pull request head must equal both `HEAD` and the `now_head` reported by
   `code-ac`'s judged change, and the change's `now` hash must equal its
   `judged` hash (the same comparison `satelle-change-judged-check` makes).
   Otherwise no approval is published and the move is refused naming both the
   pull request head and the judged change.
5. The body's first line names the project, the gate `code-ac`, its binding,
   the story and the head; the `code-ac` judge's notes follow.
6. The story's own `github.review` events on the head decide whether to
   publish: an approval is never published over requested changes on the same
   head, nor twice.
7. After publishing, `github.review` is appended to the story with `event`,
   `pull_number`, `commit_sha`, `review_id`, `html_url`, `repository` and
   `body_sha256`. It is a ledger event, not an attachment, so the story's
   judged coverage (which hashes attachments) is unchanged.

```sh
#!/usr/bin/env bash
set -u
for tool in gh jq git satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the review cannot be published"; exit 1; }
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256; }
else
  echo "neither sha256sum nor shasum is installed, so the review cannot be published"; exit 1
fi
story=$SATELLE_ITEM
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "the tree has uncommitted changes to tracked files; commit them, push, then present the move again"
  exit 1
fi
ledger=$(satelle ledger list "$story" --json 2>&1) || { echo "could not read the ledger of $story: $ledger"; exit 1; }
kind=$(printf '%s' "$ledger" | jq -r 'map(select(.kind == "item.created"))[0].payload.kind // empty')
[ -n "$kind" ] || { echo "$story: its ledger names no kind"; exit 1; }
into=$(printf '%s' "$ledger" | jq '[to_entries[] | select(.value.kind == "item.transitioned" and .value.payload.to == "integration") | .key] | last // empty')
[ -n "$into" ] || { echo "$story: it never moved into integration, so no code-ac verdict was recorded on that move; no approval published"; exit 1; }
later=$(printf '%s' "$ledger" | jq --argjson i "$into" '[to_entries[] | select(.key > $i and .value.kind == "gate.run" and .value.payload.gate == "code-ac")] | length')
if [ "$later" != 0 ]; then
  echo "$story: a code-ac gate.run was recorded after its latest move into integration, so the verdict cannot be tied to that move; no approval published"
  exit 1
fi
last_run=$(printf '%s' "$ledger" | jq -c --argjson i "$into" '[to_entries[] | select(.key < $i and .value.kind == "gate.run" and .value.payload.gate == "code-ac") | .value.payload] | last // empty')
last_res=$(printf '%s' "$last_run" | jq -r '.result // empty')
if [ "$last_res" != accept ] && [ "$last_res" != carried ]; then
  echo "$story: no accepted code-ac verdict (latest: ${last_res:-none}); no approval published"
  exit 1
fi
if ! printf '%s' "$last_run" | jq -e '.change != null' >/dev/null; then
  echo "$story: the latest code-ac verdict recorded no change; re-run code-ac"
  exit 1
fi
if [ "$last_res" = carried ]; then
  cov=$(printf '%s' "$last_run" | jq -r '.coverage // empty')
  src=$(printf '%s' "$ledger" | jq -c --arg cov "$cov" '
    ([.[] | select(.kind == "gate.run") | .payload | select(.gate == "code-ac" and .result == "accept" and ($cov == "" or .coverage == $cov))] | last)
    // ([.[] | select(.kind == "gate.run") | .payload | select(.gate == "code-ac" and .result == "accept")] | last)
    // empty')
  binding=$(printf '%s' "$src" | jq -r '.binding // empty')
  notes=$(printf '%s' "$src" | jq -r '.notes // empty')
else
  binding=$(printf '%s' "$last_run" | jq -r '.binding // empty')
  notes=$(printf '%s' "$last_run" | jq -r '.notes // empty')
fi
[ -n "$binding" ] || binding=code-ac
report=$(satelle "$kind" diff "$story" --json 2>&1) || { echo "could not read the diff of $story: $report"; exit 1; }
j=$(printf '%s' "$report" | jq -c '[(.judged // [])[] | select(.gate == "code-ac" and (.result == "accept" or .result == "carried"))] | last // empty')
judged=$(printf '%s' "$j" | jq -r '.judged // empty')
now=$(printf '%s' "$j" | jq -r '.now // empty')
now_head=$(printf '%s' "$j" | jq -r '.now_head // empty')
tr=$(printf '%s' "$j" | jq -r '.transition // empty')
if [ -z "$judged" ] || [ -z "$now" ]; then
  echo "$story: no judged change recorded for code-ac; re-run code-ac"
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
if [ "$n" -gt 1 ]; then
  echo "the queues the workflow $wf of $story enqueues into name $n trigger bases ($(printf '%s' "$bases" | awk 'NF { printf "%s%s base %s", s, $1, $2; s = "; " }')), so the pull request's base branch is ambiguous: give them one base"
  exit 1
fi
if [ "$n" -eq 0 ]; then
  why="the workflow $wf of $story enqueues into no queue with a trigger base"
  alt="or give a queue its states enqueue into base = \"<remote>/<branch>\" under [trigger]"
  remotes=$(git remote)
  remote=$(git config --get checkout.defaultRemote)
  if [ -n "$remote" ]; then
    printf '%s\n' "$remotes" | grep -qxF -e "$remote" || { echo "$why, and checkout.defaultRemote names $remote, which git remote does not list, so the pull request's base branch cannot be read: git config checkout.defaultRemote <remote> naming one git remote lists, $alt"; exit 1; }
  else
    case $(printf '%s' "$remotes" | grep -c .) in
      0) echo "$why, and this repo has no git remote, so the pull request's base branch cannot be read: git remote add <remote> <url>, $alt"; exit 1 ;;
      1) remote=$remotes ;;
      *) echo "$why, and this repo has several git remotes ($(printf '%s' "$remotes" | tr '\n' ' ')) and no checkout.defaultRemote, so the pull request's base branch cannot be read: git config checkout.defaultRemote <remote>, $alt"; exit 1 ;;
    esac
  fi
  heads=$(GIT_TERMINAL_PROMPT=0 git ls-remote --symref -- "$remote" HEAD 2>&1) || { echo "$why, and could not read the default branch of the remote $remote: $heads; $alt"; exit 1; }
  branch=$(printf '%s\n' "$heads" | awk -F '\t' '$2 == "HEAD" && index($1, "ref: refs/heads/") == 1 { print substr($1, 17); exit }')
  if [ -z "$branch" ] || ! printf '%s\n' "$heads" | awk -F '\t' '$2 == "HEAD" && $1 !~ /^ref: / { f = 1 } END { exit !f }'; then
    echo "$why, and the remote $remote names no default branch, so the pull request's base branch cannot be read: set the default branch of the repository $remote names, $alt"
    exit 1
  fi
  base_queue=
  base_ref="$remote/$branch"
else
  base_queue=$(printf '%s' "$bases" | awk 'NF { print $1; exit }')
  base_ref=$(printf '%s' "$bases" | awk 'NF { print $2; exit }')
  case "$base_ref" in
    ?*/?*) remote=${base_ref%%/*} branch=${base_ref#*/} ;;
    *) echo "the queue $base_queue has trigger base $base_ref, which is not <remote>/<branch>, so the pull request's base branch cannot be read: set base = \"<remote>/<branch>\" under its [trigger]"; exit 1 ;;
  esac
fi
# base branch: end
head=$(git rev-parse HEAD)
prs=$(gh pr list --state open --base "$branch" --json number,headRefOid,title,isDraft --limit 100 2>&1) ||
  { echo "could not list pull requests against $branch: $prs"; exit 1; }
pr_json=$(printf '%s' "$prs" | jq -c --arg h "$head" --arg id "($story)" '
  ([.[] | select(.headRefOid == $h and .isDraft == false and (.title | endswith($id)))][0])
  // ([.[] | select(.isDraft == false and (.title | endswith($id)))][0])
  // empty')
pr=$(printf '%s' "$pr_json" | jq -r '.number // empty')
pr_head=$(printf '%s' "$pr_json" | jq -r '.headRefOid // empty')
if [ -z "$pr" ] || [ -z "$pr_head" ]; then
  echo "no open pull request against $branch has head ${head:0:12} and a title ending ($story): gh pr create --base $branch --title \"<title> ($story)\""
  exit 1
fi
short=${pr_head:0:12}
if [ "$pr_head" != "$head" ] || [ "$pr_head" != "$now_head" ]; then
  echo "$story: pull request #$pr head $short is not the change code-ac accepted (judged ${judged:0:12}, head ${now_head:0:12}); push the judged head or re-run code-ac"
  exit 1
fi
if [ "$now" != "$judged" ]; then
  from=${tr%%->*}; into=${tr#*->}
  echo "$story: pull request #$pr head $short (change ${now:0:12}) is not the change code-ac accepted on $from -> $into (judged ${judged:0:12}); re-run code-ac: return $story to $from, present $into again, then present ${SATELLE_TO:-the target state} again"
  exit 1
fi
slug=$(satelle project show 2>&1 | sed -n 's/^slug: *//p')
[ -n "$slug" ] || { echo "no project bound: bind this repo (satelle project bind <project> --session <session>)"; exit 1; }
body=$(printf 'satelle · project %s · gate code-ac (%s) on %s head %s: approve\n\n%s' "$slug" "$binding" "$story" "$short" "$notes")
sum=$(printf '%s' "$body" | sha256 | cut -d' ' -f1)
recorded=$(satelle ledger list "$story" --kind github.review --json | jq -c --arg h "$pr_head" '[.[] | .payload | select(.commit_sha == $h)]') ||
  { echo "could not read the github.review events of $story"; exit 1; }
if printf '%s' "$recorded" | jq -e 'any(.[]; .event == "REQUEST_CHANGES")' >/dev/null; then
  echo "not publishing an approval: changes were requested on head $short of $story"
  exit 1
fi
url=$(printf '%s' "$recorded" | jq -r '[.[] | select(.event == "APPROVE")] | last | .html_url // empty')
if [ -n "$url" ]; then
  echo "already published on head $short of $story ($url)"
  exit 0
fi
out=$(printf '%s' "$body" | satelle github review --pr "$pr" --sha "$pr_head" --event APPROVE --body-file - --json 2>&1) ||
  { echo "publishing the review failed: $out"; exit 1; }
url=$(printf '%s' "$out" | jq -r '.html_url')
payload=$(printf '%s' "$out" | jq -c --argjson pr "$pr" --arg sha "$pr_head" --arg sum "$sum" \
  '{event: "APPROVE", pull_number: $pr, commit_sha: $sha, review_id: .review_id, html_url: .html_url, repository: .repository, body_sha256: $sum}')
satelle ledger append "$story" --kind github.review --payload "$payload" >/dev/null ||
  { echo "published $url but could not record github.review on $story"; exit 1; }
echo "published APPROVE on $story head $short: $url"
```
