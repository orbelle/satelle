---
name: satelle-instruction-change-trigger
scope: system
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: The `when` precondition for satelle-instruction-change-review. Exits 0 when the story's change set adds or rewrites instruction text (a new file, or at least THRESHOLD changed words) in skills, principles or the constitution; exits 1 for a whitespace, punctuation or typo-only change. Deterministic and self-contained; the rule is this script, so a repo overrides the skill to change it.
---

# Instruction-change trigger (`when` precondition)

Not a verdict. A gate that declares `when = "satelle-instruction-change-trigger"`
runs this script before it is enqueued: **exit 0 runs the gate, exit 1 skips
it**, any other outcome runs it (a broken precondition never costs a gate). The
binary runs the script; the rule below is the whole decision.

## What counts

The story's change set, unioned from `satelle story diff <id> --include-substrate`
and `--recorded`, filtered to instruction paths:

- generic: `.satelle/skills/**`, `.satelle/principles/**`,
  `.satelle/constitution.md`
- plus every glob in `satelle.toml` `[instruction_review] extra_paths` — how a
  repo that also keeps embedded defaults in-tree names them; the binary ships no
  repo path

A **new** file, an unmeasurable one (git-ignored substrate), or a change with at
least `THRESHOLD` changed words (`git diff -w --word-diff`, tokens containing a
letter or digit only) triggers. Whitespace and punctuation changes count zero.
A story with no baseline and instruction files changed also triggers. No
instruction path changed means skip.

## Configuration

`THRESHOLD` (default 5) and the generic path pattern are the first lines of the
script. Override this skill by name under `.satelle/skills/` to change either.

```check
#!/usr/bin/env bash
set -uo pipefail

# THE CONFIGURATION — override this skill by name to change it.
THRESHOLD=5
generic='^\.satelle/(skills|principles)/|^\.satelle/constitution\.md$'

payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload"
  exit 2
fi

extract_files() {
  local raw
  raw=$(cat)
  [ -z "$raw" ] && return 0
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$raw" | python3 -c "import json,sys
try:
 print('\n'.join(json.load(sys.stdin).get('files') or []))
except Exception:
 pass" 2>/dev/null || true
  else
    printf '%s' "$raw" | tr -d ' \n' | sed -n 's/.*\"files\":\[\([^]]*\)\].*/\1/p' | tr ',' '\n' | tr -d '"' | grep -v '^$' || true
  fi
}

rec=""
if recorded=$(satelle story diff "$sid" --recorded --json 2>/dev/null); then
  rec=$(printf '%s' "$recorded" | extract_files)
fi
liv=""
base=""
if live=$(satelle story diff "$sid" --include-substrate --json 2>/dev/null); then
  liv=$(printf '%s' "$live" | extract_files)
  base=$(printf '%s' "$live" | grep -oE '"baseline_sha":[[:space:]]*"[a-f0-9]+"' | head -1 | grep -oE '[a-f0-9]{7,}')
fi

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

files=$(printf '%s\n%s\n' "$rec" "$liv" | grep -v '^$' | sort -u | grep -E "$match" || true)
if [ -z "$files" ]; then
  echo "no instruction file changed for $sid (skills, principles, constitution)"
  exit 1
fi
if [ -z "$base" ]; then
  echo "instruction files changed for $sid but there is no engagement baseline to measure against: $(printf '%s' "$files" | tr '\n' ' ')"
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
done <<EOF
$files
EOF

if [ "$total" -ge "$THRESHOLD" ]; then
  echo "instruction text changed: $total words (threshold $THRESHOLD) in: $(printf '%s' "$files" | tr '\n' ' ')"
  exit 0
fi
echo "typo/whitespace-only ($total words, threshold $THRESHOLD) in: $(printf '%s' "$files" | tr '\n' ' ')"
exit 1
```
