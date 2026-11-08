---
name: satelle-instruction-change-trigger
scope: system
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: The trigger of satelle-instruction-change-review. Exits 0 when the story's diff adds or rewrites instruction text (a new file, or at least THRESHOLD changed words) in skills, principles or the constitution; exits 1 for no instruction change or a whitespace, punctuation or typo-only one. Deterministic and self-contained; the rule is this script, so a repo overrides the skill to change it.
---

# Instruction-change trigger

Not a verdict. A gate that declares `trigger = "satelle-instruction-change-trigger"`
runs this script before it is enqueued: **exit 0 runs the gate, exit 1 skips
it** and its output is noted, and any other exit means the trigger is broken,
so the gate runs anyway. The binary runs the script; the rule below is the
whole decision.

## What counts

The story's change set is the `paths` of `satelle story diff <id> --json`, the
id being `$SATELLE_ITEM`, filtered to instruction paths:

- generic: `.satelle/skills/**`, `.satelle/principles/**`,
  `.satelle/constitution.md`
- plus every glob in `satelle.toml` `[instruction_review] extra_paths` — how a
  repo that also keeps embedded defaults in-tree names them; the binary ships no
  repo path

Each is measured against the diff's `base`. A **new** file, an unmeasurable one
(git-ignored substrate), or a change with at least `THRESHOLD` changed words
(`git diff -w --word-diff`, tokens containing a letter or digit only) triggers.
Whitespace and punctuation changes count zero. A diff with no base and
instruction files changed also triggers. No instruction path changed means skip.

## Configuration

`THRESHOLD` (default 5) and the generic path pattern are the first lines of the
script. Override this skill by name under `.satelle/skills/` to change either.

```check
#!/usr/bin/env bash
set -uo pipefail

# THE CONFIGURATION — override this skill by name to change it.
THRESHOLD=5
generic='^\.satelle/(skills|principles)/|^\.satelle/constitution\.md$'

command -v jq >/dev/null 2>&1 || { echo "jq is not installed"; exit 2; }
sid=$(printf '%s' "${SATELLE_ITEM:-}" | grep -oE '^sto_[a-f0-9]+$')
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM (${SATELLE_ITEM:-unset}) is not a story id"
  exit 2
fi
if ! diff=$(satelle story diff "$sid" --json 2>&1); then
  echo "could not read the diff of $sid: $diff"
  exit 2
fi
if ! all=$(printf '%s' "$diff" | jq -r '.paths // [] | .[]'); then
  echo "could not read the paths of $sid's diff"
  exit 2
fi
base=$(printf '%s' "$diff" | jq -r '.base // empty')

# Instruction paths: the generic set plus [instruction_review] extra_paths globs.
match="$generic"
extra=$(sed 's/#.*//' .satelle/satelle.toml 2>/dev/null \
  | awk '/^\[instruction_review\]/{s=1;next} /^\[/{s=0} s' | tr '\n' ' ' \
  | sed -n 's/.*extra_paths[[:space:]]*=[[:space:]]*\[\([^]]*\)\].*/\1/p' \
  | grep -oE '"[^"]+"' | tr -d '"')
for g in $extra; do
  re=$(printf '%s' "$g" | sed -e 's#[.[\^$+(){}|]#\\&#g' -e 's#\*\*#@@DS@@#g' -e 's#\*#[^/]*#g' -e 's#@@DS@@#.*#g')
  match="$match|^$re\$"
done

files=$(printf '%s\n' "$all" | grep -v '^$' | grep -E "$match" || true)
if [ -z "$files" ]; then
  echo "no instruction file changed for $sid (skills, principles, constitution)"
  exit 1
fi
if [ -z "$base" ]; then
  echo "instruction files changed for $sid but its diff has no base to measure against: $(printf '%s' "$files" | tr '\n' ' ')"
  exit 0
fi

total=0
while IFS= read -r f; do
  [ -z "$f" ] && continue
  if git check-ignore -q -- "$f" 2>/dev/null || ! git cat-file -e "$base:$f" 2>/dev/null; then
    echo "new or untracked instruction file: $f"
    exit 0
  fi
  n=$(git diff -w --word-diff=porcelain -U0 --no-color "$base" -- "$f" 2>/dev/null | awk '
    /^@@/ {h=1; next}
    !h {next}
    # porcelain prints a run of changed words as ONE line: count its words.
    /^\+/ { k = split(substr($0,2), w, /[[:space:]]+/); for (i=1;i<=k;i++) if (w[i] ~ /[[:alnum:]]/) a++ }
    /^-/  { k = split(substr($0,2), w, /[[:space:]]+/); for (i=1;i<=k;i++) if (w[i] ~ /[[:alnum:]]/) d++ }
    END { print (a>d ? a : d) + 0 }')
  total=$((total + ${n:-0}))
done <<EOF2
$files
EOF2

if [ "$total" -ge "$THRESHOLD" ]; then
  echo "instruction text changed: $total words (threshold $THRESHOLD) in: $(printf '%s' "$files" | tr '\n' ' ')"
  exit 0
fi
echo "typo/whitespace-only ($total words, threshold $THRESHOLD) in: $(printf '%s' "$files" | tr '\n' ' ')"
exit 1
```
