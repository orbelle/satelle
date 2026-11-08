---
name: satelle-verdict-reference-check
type: check
description: After check of a gate that judges a plan or a change (sto_b9f9d9c7). Every judge that accepted must say what it judged - quote the work, or name a file, a criterion or a record path; an accept that references nothing gave no reasons, and the gate's result becomes reject with the judge's raw answer kept. Rejects pass. Deterministic, no agent.
---

# Verdict reference rule

An accept that references nothing may mean the judge never read the work.
This check is the reference rule of the gate that lists it in `after`: the
gate owns the rule by declaring it, and a gate that does not declare it
records its judge's verdict as given. The binary holds no reference rule; the
patterns below are this skill's alone.

The rule: each judge that accepted (`members[]` on stdin with result
`accept`) has notes that reference the work by at least one of:

- a quoted passage: 12 or more characters inside double quotes, single
  quotes or backticks;
- a path: a token holding `/` (such as `internal/gate/gate.go:470` or
  `attachments/plan`), or a file name with an extension (`gate.go`);
- a criterion: `AC<n>` or `criterion <n>`;
- a record path: `attachments/` or `.satelle/runs/`.

A reject passes this rule: its reasons are the finding. When an accept
references nothing, the check names the judge and exits 1: the gate's result
becomes reject, the checks after this one do not run, and the judge's raw
answer is kept on the gate.run.

A carried result or a check gate has no `members` and passes.

```sh
set -u
p=$(cat)
n=$(printf '%s' "$p" | jq '.members // [] | length') || { echo "the payload is not JSON"; exit 2; }
quoted='"[^"]{12,}"|'"'"'[^'"'"']{12,}'"'"'|`[^`]{12,}`'
path='[^[:space:]]+/[^[:space:]]+|[A-Za-z0-9_-]{2,}\.[A-Za-z][A-Za-z0-9]{0,5}([^A-Za-z0-9]|$)'
criterion='AC[0-9]+|[Cc]riterion [0-9]+'
record='attachments/|\.satelle/runs/'
failed=0
i=0
while [ "$i" -lt "$n" ]; do
  result=$(printf '%s' "$p" | jq -r ".members[$i].result")
  if [ "$result" = accept ]; then
    binding=$(printf '%s' "$p" | jq -r ".members[$i].binding")
    notes=$(printf '%s' "$p" | jq -r ".members[$i].notes // \"\"")
    if ! printf '%s\n' "$notes" | grep -Eq -e "$quoted" -e "$path" -e "$criterion" -e "$record"; then
      echo "the judge gave no reasons: $binding accepted without quoting the work or naming a file, criterion or record path"
      failed=1
    fi
  fi
  i=$((i + 1))
done
exit "$failed"
```
