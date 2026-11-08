---
name: satelle-verdict-notes-check
type: check
description: After check of a judged gate (sto_b9f9d9c7). Every judge's own notes must hold at least 40 non-whitespace characters, whatever its verdict; a judge that answered with fewer gave no reasons, and the gate's result becomes reject with the judge's raw answer kept. A carry or a check gate has no judges and passes. Deterministic, no agent.
---

# Verdict notes rule

A verdict is only worth its reasons. This check is the notes rule of the gate
that lists it in `after`: the gate owns the rule by declaring it, and a gate
that does not declare it records its judge's verdict as given. The binary
holds no notes length; the number below is this skill's alone.

The rule: each judge's own notes (`members[].notes` on stdin, one entry per
judge, one per panel member) hold at least 40 characters that are not
whitespace. It applies to an accept and a reject alike. When any judge falls
short, the check names it and exits 1: the gate's result becomes reject,
the notes carry the reason, the checks after this one do not run (a refused
accept is never published), and the judge's raw answer is kept on the
gate.run (`answer`, or `members[].answer` for a panel).

A carried result or a check gate has no `members`; nothing is judged here
and the check passes.

```sh
set -u
min=40
p=$(cat)
n=$(printf '%s' "$p" | jq '.members // [] | length') || { echo "the payload is not JSON"; exit 2; }
failed=0
i=0
while [ "$i" -lt "$n" ]; do
  binding=$(printf '%s' "$p" | jq -r ".members[$i].binding")
  result=$(printf '%s' "$p" | jq -r ".members[$i].result")
  count=$(printf '%s' "$p" | jq ".members[$i].notes // \"\" | gsub(\"\\\\s\"; \"\") | length")
  if [ "$count" -lt "$min" ]; then
    echo "the judge gave no reasons: $binding answered $result with $count characters of notes (this gate's notes rule wants at least $min)"
    failed=1
  fi
  i=$((i + 1))
done
exit "$failed"
```
