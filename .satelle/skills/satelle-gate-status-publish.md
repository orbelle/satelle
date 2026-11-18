---
name: satelle-gate-status-publish
type: check
after: always
description: Posts the pull-request gates' results on the head as commit statuses (satelle/<gate>) through satelle.dev, as the satelle GitHub app (satelle github status), so a branch rule can require them pinned to the app (sto_4f698e3f). The check of the pr-pending gate (pending for every context, first on integration → review) and the last after entry of pr-ready, pr-approval and the review task's pr-review gate (that gate's result, on every result: after always). Reads each post back from GitHub and names the account that posted it. Never refuses: a status that cannot be posted is named in the notes and the gate's result stands. Deterministic, no agent.
---

# Gate status publish (satelle/<gate> on the head)

The branch rule on main requires `satelle/pr-ready`, `satelle/pr-review` and
`satelle/pr-approval`, each pinned to the satelle GitHub app (app 5379260), so
only the app may post them. This check posts them through satelle.dev, which
acts as the app on the repository the project is bound to.

- **Which contexts.** The `gates=` line below owns them; each is
  `satelle/<gate>` on HEAD (`git rev-parse HEAD`).
- **Pending mode** (the `pr-pending` gate's own check, `SATELLE_GATE` unset):
  posts `pending` for every context, so no success from an earlier
  presentation of the same head stays green while this one runs, and a gate
  this presentation never reaches keeps `pending`.
- **Gate mode** (an `after` entry, `SATELLE_GATE` set): reads the gate's
  result and notes from stdin, as the result stands once the entries before it
  have run, and posts `success` for accept, carried or skipped, `failure` for
  reject and `error` for anything else. The description is
  `<result> (<from>-><to>): <first line of the notes>`, cut to GitHub's 140
  characters. Listed last, so an accept the verdict rules refuse posts as
  `failure`.
- **Who posted it.** After a post, the check reads the head's statuses back
  from GitHub and compares the newest one for that context's `creator.id` with
  `app_account`, which it reads from `satelle-pr-approval-check.md` (that
  file owns the value). Its line names the account either way.
- **Never refuses.** It exits 0 on every path. A status the server refuses, a
  missing tool or a dry run is a line in the gate's notes; the gate's result
  is unchanged.

```sh
#!/usr/bin/env bash
set -u
gates="pr-ready pr-review pr-approval"
for tool in jq git satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so no status is posted"; exit 0; }
done
head=$(git rev-parse HEAD 2>/dev/null) || { echo "no HEAD, so no status is posted"; exit 0; }
short=${head:0:12}
post() { # context state description
  satelle github status --sha "$head" --context "$1" --state "$2" --description "$3" --json 2>&1
}
if [ -z "${SATELLE_GATE:-}" ]; then
  for g in $gates; do
    if out=$(post "satelle/$g" pending "pending ($SATELLE_FROM->$SATELLE_TO): gates running"); then
      echo "satelle/$g pending on $short"
    else
      echo "satelle/$g not published on $short: $out"
    fi
  done
  exit 0
fi
case " $gates " in
  *" $SATELLE_GATE "*) ;;
  *) echo "gate $SATELLE_GATE posts no status (not on the gates= line)"; exit 0 ;;
esac
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
app_account=$(sed -n 's/^app_account=//p' .satelle/skills/satelle-pr-approval-check.md)
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
