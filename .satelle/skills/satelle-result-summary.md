---
name: satelle-result-summary
type: check
description: The result-summary gate (sto_9f1303b3), last on integration → confirm. Composes the operator's result summary from records that already exist (the change from the item's base to HEAD, every gate verdict on the item's ledger, the pull request and the satelle app's review on HEAD, the item's attachments, and the coder's "How to try it" from ac-evidence) and attaches it as result-summary, typed brief, which satelled's Awaiting-you page shows. Rejects, attaching nothing, when a section cannot be composed. Deterministic, no agent.
---

# Result summary (the operator's brief at confirm)

The item reaches `confirm`, the operator's state, only through this gate. The
operator confirms it (the move to approved) or sends it back with a reason
(the move to in_progress) from this summary, on satelled's Awaiting-you page or
with `satelle story attachment <id> result-summary`.

It writes nothing twice: every section is read from a record another owner
keeps.

- **Changes.** `git log --oneline` and `git diff --stat` from `$SATELLE_BASE`
  (the item's base) to HEAD, the head integration-check tested.
- **Gate verdicts.** Every earlier transition's newest presentation from the
  item's `gate.run` events, one line per gate (result, then the first line of
  its notes), with the number of rounds. The gates before this one in this
  move are listed as accepted: their `gate.run` events are recorded only once
  the whole move has run, but a sequential group reaches its last gate only
  when every earlier gate accepted or carried.
- **Evidence.** The pull request and the satelle app's review on HEAD (the
  newest `github.review` event whose `commit_sha` is HEAD, which pr-approval
  requires), the head sha, and each attachment with the command that reads it.
- **How to try it.** The `## How to try it` section of the coder's
  `ac-evidence` attachment, as the coder wrote it (coder.md).

A missing tool, base, review or how-to rejects with the reason; nothing is
attached and the item stays where it is. No carry: every entry into confirm
regenerates it.

```sh
#!/usr/bin/env bash
set -u
for tool in git jq satelle awk; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the result summary cannot be composed"; exit 1; }
done
id=$SATELLE_ITEM
[ -n "${SATELLE_BASE:-}" ] || { echo "the item has no base (SATELLE_BASE is empty), so its changes cannot be summarised"; exit 1; }

evidence=$(satelle story attachment "$id" ac-evidence 2>/dev/null) ||
  { echo "ac-evidence is not attached; the coder attaches it with a '## How to try it' section (coder.md)"; exit 1; }
howto=$(printf '%s\n' "$evidence" | awk '/^## How to try it[[:space:]]*$/ {on=1; next} on && /^## / {exit} on {print}')
if [ -z "$(printf '%s' "$howto" | tr -d '[:space:]')" ]; then
  echo "ac-evidence carries no '## How to try it' section; the coder adds it (coder.md)"
  exit 1
fi

item=$(satelle story get "$id" --json) || { echo "could not read $id"; exit 1; }
title=$(printf '%s' "$item" | jq -r '.title')
wf=$(printf '%s' "$item" | jq -r '.workflow')
criteria=$(printf '%s' "$item" | jq -r '(.criteria // []) | to_entries[] | "\(.key + 1). \(.value)"')

head=$(git rev-parse HEAD) || { echo "could not read HEAD"; exit 1; }
short=${head:0:12}
log=$(git log --oneline "$SATELLE_BASE..HEAD") || { echo "could not read the log from $SATELLE_BASE"; exit 1; }
stat=$(git diff --stat "$SATELLE_BASE" HEAD) || { echo "could not diff from $SATELLE_BASE"; exit 1; }

runs=$(satelle ledger list "$id" --kind gate.run --json) || { echo "could not read the item's gate.run events"; exit 1; }
earlier=$(printf '%s' "$runs" | jq -r '
  [(. // [])[] | .payload] as $r
  | ($r | reduce (.[] | .transition) as $t ([]; if any(.[]; . == $t) then . else . + [$t] end)) as $ts
  | $ts[] as $t
  | ($r | map(select(.transition == $t))) as $tr
  | ($tr | map(.attempt) | unique | length) as $rounds
  | ($tr | last | .attempt) as $a
  | "", "**\($t)** (\($rounds) round\(if $rounds == 1 then "" else "s" end))",
    ($tr | map(select(.attempt == $a))[]
      | "- \(.gate)\(if (.binding // "") != "" then " (\(.binding))" else "" end): \(.result)\(if (.notes // "") != "" then ": \(.notes | split("\n")[0] | .[0:160])" else "" end)")
') || { echo "could not read the item's gate verdicts"; exit 1; }
route=$(satelle workflow show "$wf" --json) || { echo "could not read workflow $wf"; exit 1; }
this=$(printf '%s' "$route" | jq -r --arg f "$SATELLE_FROM" --arg t "$SATELLE_TO" '
  [.transitions[] | select(.To == $t and ((.From // []) | index($f)))][0]
  | [(.Groups // [])[] | .Gates[]] | (index("result-summary") // length) as $i | .[0:$i][]
  | "- \(.): accepted in this move (a sequential group reaches its last gate only when every earlier gate accepted)"') ||
  { echo "could not read the gates of $SATELLE_FROM -> $SATELLE_TO"; exit 1; }

reviews=$(satelle ledger list "$id" --kind github.review --json) || { echo "could not read the item's github.review events"; exit 1; }
review=$(printf '%s' "$reviews" | jq -c --arg h "$head" '[(. // [])[] | .payload | select(.commit_sha == $h)] | last // empty')
if [ -z "$review" ]; then
  echo "no github.review on head $short in the item's ledger; pr-approval requires one, so the item is in a broken state"
  exit 1
fi
pr_url=$(printf '%s' "$review" | jq -r '"https://github.com/\(.repository)/pull/\(.pull_number)"')
review_url=$(printf '%s' "$review" | jq -r '.html_url // "none"')
atts=$(satelle story attachments "$id" --json | jq -r --arg id "$id" '
  (. // []) | group_by(.name) | map(last) | sort_by(.at)[]
  | "- `\(.name)` (\(.media_type), sha256 \(.sha256[0:12])): `satelle story attachment \($id) \(.name)`"') ||
  { echo "could not list the item's attachments"; exit 1; }

summary=$(cat <<EOF
# Result summary: $title ($id)

Workflow $wf, head $short. Confirm it (move to approved) or send it back with
a reason (move to in_progress).

$criteria

## Changes

\`\`\`
$log

$stat
\`\`\`

## Gate verdicts
$earlier

**$SATELLE_FROM->$SATELLE_TO** (this move)
$this

## Evidence

- Pull request: $pr_url
- The satelle app's review on the head: $review_url
- Head: $head
$atts

## How to try it
$howto
EOF
)
printf '%s\n' "$summary" | satelle story attach "$id" --name result-summary --type "text/markdown; type=brief" --file - >/dev/null ||
  { echo "could not attach result-summary"; exit 1; }
echo "result summary attached ($(printf '%s\n' "$summary" | wc -c | tr -d ' ') bytes)"
exit 0
```
