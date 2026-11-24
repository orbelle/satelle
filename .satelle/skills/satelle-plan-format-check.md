---
name: satelle-plan-format-check
type: check
description: Functional check on plan → in_progress, ahead of every judged gate. Rejects when the newest plan's "## Acceptance criteria" section holds any line that is not a numbered one-line criterion, naming each such line; a plan with no such section is valid, since the story's own criteria stand. Deterministic, zero LLM tokens.
---

# Plan format check (plan-exit functional gate)

The plan skill (`.satelle/skills/plan.md`, *Author the acceptance criteria*)
says what an `## Acceptance criteria` section may hold; this check refuses a
plan whose section breaks it, before a judge is paid to read it. It reads the
newest `plan` attachment of `$SATELLE_ITEM`
(`satelle story attachment <id> plan --session <session>`).

- No `## Acceptance criteria` heading outside a code fence: accept.
- In the section, up to the next `#` or `##` heading, each line must be
  blank or start `<n>. ` followed by text. Any other line (prose, a bullet, a
  wrapped continuation, a `###` heading, a code fence) is refused, by line
  number and text, and so is a section with no criterion at all.

When `SATELLE_PLAN_FILE` is set, the check reads that file as the plan
instead; a replay over plans already on the ledger uses it.

Exit 0 accepts; exit 1 refuses or could not read the plan.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the story's plan cannot be checked"
  exit 1
fi
if [ -n "${SATELLE_PLAN_FILE:-}" ]; then
  plan=$(cat "$SATELLE_PLAN_FILE") || { echo "could not read $SATELLE_PLAN_FILE"; exit 1; }
elif ! plan=$(satelle story attachment "$sid" plan 2>&1); then
  echo "could not read the plan of $sid:"
  echo "$plan"
  exit 1
fi
out=$(printf '%s\n' "$plan" | awk '
  BEGIN { fence = 0; in_s = 0; seen = 0; n = 0; bad = 0 }
  {
    line = $0
    sub(/\r$/, "", line)
    if (in_s) {
      if (line ~ /^##?[ \t]/) { in_s = 0 }
      else {
        if (line ~ /^[ \t]*$/) next
        if (line ~ /^[0-9]+\. [^ \t]/) { n++; next }
        printf "line %d: %s\n", NR, line
        bad++
        next
      }
    }
    if (line ~ /^[ \t]*(```|~~~)/) { fence = !fence; next }
    if (fence) next
    l = tolower(line)
    if (l ~ /^##[ \t]+acceptance criteria[ \t]*$/) { in_s = 1; seen = 1 }
  }
  END {
    if (!seen) { print "NONE"; exit }
    if (n == 0 && bad == 0) print "EMPTY"
    printf "COUNT %d %d\n", n, bad
  }
')
case "$out" in
  NONE)
    echo "the plan of $sid has no ## Acceptance criteria section: the story's own criteria stand"
    exit 0
    ;;
esac
bad=$(printf '%s\n' "$out" | sed -n 's/^COUNT [0-9]* \([0-9]*\)$/\1/p')
n=$(printf '%s\n' "$out" | sed -n 's/^COUNT \([0-9]*\) [0-9]*$/\1/p')
if [ "${bad:-0}" -gt 0 ]; then
  echo "the ## Acceptance criteria section of the plan of $sid holds $bad line(s) that are not a numbered one-line criterion (\"<n>. <criterion>\", one line each, nothing else before the next ## heading; .satelle/skills/plan.md):"
  printf '%s\n' "$out" | grep '^line '
  exit 1
fi
if printf '%s\n' "$out" | grep -q '^EMPTY$'; then
  echo "the ## Acceptance criteria section of the plan of $sid holds no criterion: write the numbered criteria, or leave the section out so the story's own criteria stand"
  exit 1
fi
echo "the ## Acceptance criteria section of the plan of $sid holds $n numbered one-line criteria and nothing else"
exit 0
```
