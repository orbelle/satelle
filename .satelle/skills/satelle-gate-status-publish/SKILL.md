---
name: satelle-gate-status-publish
type: check
after: always
description: Posts the pull-request gates' results on the head as commit statuses (satelle/<gate>) through satelle.dev, as the satelle GitHub app (satelle github status), so a branch rule can require them pinned to the app. The gates it posts for are the ones whose after list names it, read from the repo's workflows. The check of the pending gate (pending for every such gate's context, first on the move into review) and the last after entry of each such gate (that gate's result, on every result: after always). Reads each post back from GitHub and names the account that posted it. Never refuses: a status that cannot be posted is named in the notes and the gate's result stands. Deterministic, no agent.
---

# Gate status publish (satelle/<gate> on the head)

A branch rule on the base branch can require these contexts, each pinned to
the satelle GitHub app (app 5379260), so only the app may post them. This
check posts them through satelle.dev, which acts as the app on the repository
the project is bound to.

- **Which contexts.** The gates whose `after` list names this skill own them,
  in any workflow under `.satelle/workflows` (read with
  `satelle workflow show <name> --json --session <session>`); each is `satelle/<gate>` on HEAD
  (`git rev-parse HEAD`). The skill writes no gate name but its own.
- **Pending mode** (the pending gate's own check, `SATELLE_GATE` unset):
  posts `pending` for every context, so no success from an earlier
  presentation of the same head stays green while this one runs, and a gate
  this presentation never reaches keeps `pending`. A workflow that cannot be
  read is a line in the notes; with no gate naming this skill it posts nothing.
- **Gate mode** (an `after` entry, `SATELLE_GATE` set): posts
  `satelle/$SATELLE_GATE`; the kernel runs an after entry only for a gate
  whose `after` names it. It reads the gate's result and notes from stdin,
  as the result stands once the entries before it have run, and posts `success` for accept, carried or skipped, `failure` for
  reject and `error` for anything else. The description is
  `<result> (<from>-><to>): <first line of the notes>`, cut to GitHub's 140
  characters. Listed last, so an accept the verdict rules refuse posts as
  `failure`.
- **Who posted it.** After a post, the check reads the head's statuses back
  from GitHub and compares the newest one for that context's `creator.id` with
  `app_account`, the satelle app's account id, which this skill holds on its
  own line below. `skills/satelle-pr-approval-check/SKILL.md` holds the same
  value; the two change together. Its line names the account either way.
- **Never refuses.** It exits 0 on every path. A status the server refuses, a
  missing tool or a dry run is a line in the gate's notes; the gate's result
  is unchanged.

```sh
#!/usr/bin/env bash
set -u
self=satelle-gate-status-publish
app_account=344745317
for tool in jq git satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so no status is posted"; exit 0; }
done
head=$(git rev-parse HEAD 2>/dev/null) || { echo "no HEAD, so no status is posted"; exit 0; }
short=${head:0:12}
post() { # context state description
  satelle github status --sha "$head" --context "$1" --state "$2" --description "$3" --json 2>&1
}
if [ -z "${SATELLE_GATE:-}" ]; then
  posting=
  for f in .satelle/workflows/*.toml; do
    [ -e "$f" ] || continue
    w=$(basename "$f" .toml)
    if ! shown=$(satelle workflow show "$w" --json 2>/dev/null); then
      echo "workflow $w could not be read, so its gates get no pending status"
      continue
    fi
    posting="$posting $(printf '%s' "$shown" | jq -r --arg s "$self" '(.gates // {}) | to_entries[] | select(any((.value.After // [])[]; . == $s)) | .key')"
  done
  posting=$(printf '%s\n' $posting | sort -u)
  if [ -z "$posting" ]; then
    echo "no gate's after names $self, so no pending status is posted"
    exit 0
  fi
  for g in $posting; do
    if out=$(post "satelle/$g" pending "pending ($SATELLE_FROM->$SATELLE_TO): gates running"); then
      echo "satelle/$g pending on $short"
    else
      echo "satelle/$g not published on $short: $out"
    fi
  done
  exit 0
fi
in=$(cat)
result=$(printf '%s' "$in" | jq -r '.result // empty')
case "$result" in
  accept|carried|skipped) state=success ;;
  reject) state=failure ;;
  *) state=error ;;
esac
desc=$(printf '%s' "$in" | jq -r --arg r "${result:-none}" --arg e "$SATELLE_FROM->$SATELLE_TO" \
  '"\($r) (\($e)): " + ((.notes // "") | split("\n") | map(select(test("\\S"))) | (.[0] // "") | sub("^\\s+"; "")) | .[0:140]')
ctx="satelle/$SATELLE_GATE"
if ! out=$(post "$ctx" "$state" "$desc"); then
  echo "$ctx not published on $short: $out"
  exit 0
fi
if ! command -v gh >/dev/null 2>&1; then
  echo "$ctx $state published on $short; could not read it back from GitHub (gh is not installed)"
  exit 0
fi
if ! statuses=$(gh api "repos/{owner}/{repo}/commits/$head/statuses?per_page=100" 2>&1); then
  echo "$ctx $state published on $short; could not read it back from GitHub: $statuses"
  exit 0
fi
creator=$(printf '%s' "$statuses" | jq -r --arg c "$ctx" '[.[] | select(.context == $c)][0].creator.id // empty')
if [ -n "$creator" ] && [ "$creator" = "$app_account" ]; then
  echo "$ctx $state on $short, posted by the satelle app (account $app_account)"
else
  echo "$ctx $state published on $short, but GitHub shows it from account ${creator:-none}, not the satelle app's account $app_account"
fi
exit 0
```
