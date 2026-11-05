---
name: satelle-docs-only-check
scope: system
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check for the docs lane's close — rejects any path in the story change set that is not doc-typed, and rejects an empty set unless the story records an external-change document (prose delivered outside the repo). The doc-path pattern and the evidence type are the gate's configuration; a repo changes them by overriding this skill. Deterministic and self-contained.
---

# Docs-only check (docs lane close gate)

**Functional-check** gate on the `docs` lane's close. On `in_progress → done` it
verifies the story's **change set** touched **only doc-typed paths**.

Any other path — a source file, a build config, anything the pattern does not
name — means the slice is not a documentation slice: it took the light lane
while doing code-shaped work, so the close is **rejected** and the slice belongs
on the repo's project lane. An **empty** change set is also **rejected**: empty
is not evidence of a documentation change — unless the deliverable lives outside
the repo and the story records it (see "External document evidence").

## The pattern is configuration

`doc_paths` at the top of the check below is an extended regex over the change
set, and it is the whole file-type scope of this lane. It ships as **markdown
only** — the one doc form every repo has. A repo that keeps prose elsewhere
(`docs/` assets, `*.rst`, `*.adoc`) widens it by **overriding this skill** under
`.satelle/skills/`; a repo that wants a narrower lane narrows it the same way.
Never a branch in the binary: the binary runs the enumeration mechanism
(`satelle story diff`), and the gate decides.

## Evidence channels (union — no commit required)

1. **Recorded** — `satelle story diff <id> --recorded` (change_record rows)
2. **Live + substrate** — `satelle story diff <id> --include-substrate` (git
   worktree since engagement, plus mtime-changed substrate, so prose under a
   git-ignored authored dir is visible too)
3. **Commits** — any commit whose **subject** names the story id (the trailing
   `(sty_…)` convention); a mention only in another commit's body is a citation,
   not ownership
4. **External document (recorded)** — only when the three channels above are
   empty: an attached document of type `external-change` with a non-empty body

## External document evidence

The docs lane also covers prose delivered **outside the repo** — a Confluence
page, a Jira ticket, a hosted document. Such a slice changes no repo path, so
its evidence is a document attached to the story, of type `external-change`
(`external_type` in the check, configuration like `doc_paths`). Its body names
the document's identity (URL or page/ticket id), its new version, and a line on
what changed:

```bash
satelle story attach <sty_id> --name <doc> --type external-change \
  --body '<url or page id> — version <n>: <what changed>'
```

It counts only when the repo change set is empty and the body is non-empty. It
never excuses a non-doc repo path: code-shaped work with a document attached is
still rejected. satelle cannot verify a hosted document's version; the check
confirms the evidence is recorded, and a repo that wants more overrides this skill.

The check is the embedded ```check script below — **self-contained**, no
external file (see [[satelle-reviewer-self-contained]]). Exit 0 accepts;
non-zero rejects with notes. **Mechanism, not judgment.** See
[[satelle-agent-model]].

```check
#!/usr/bin/env bash
set -uo pipefail

# THE CONFIGURATION: which paths count as documentation. Override this skill to
# change it — see "The pattern is configuration" above.
doc_paths='\.md$'
# THE CONFIGURATION: the attached-document type that records a change delivered
# outside the repo (see "External document evidence" above).
external_type='external-change'

payload=$(cat)
# Anchored on the id KEY so a story id quoted inside the body cannot win.
sid=$(printf '%s' "$payload" | grep -oE '"id":[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload"
  exit 1
fi

extract_files() {
  # stdin: story-diff JSON; $1: the list key (default files); stdout: one entry per line
  local raw key="${1:-files}"
  raw=$(cat)
  [ -z "$raw" ] && return 0
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$raw" | python3 -c "import json,sys
try:
 print('\n'.join(json.load(sys.stdin).get(sys.argv[1]) or []))
except Exception:
 pass" "$key" 2>/dev/null || true
  else
    printf '%s' "$raw" | tr -d '\n' | grep -oE "\"$key\":[[:space:]]*\\[[^]]*\\]" | head -1 | sed 's/^"[^"]*":[[:space:]]*//' | tr ',' '\n' | grep -oE '"[^"]+"' | tr -d '"' | grep -v '^$' || true
  fi
}

# stdin: `satelle story docs --json`; stdout: names of documents of $external_type
external_names() {
  local raw
  raw=$(cat)
  [ -z "$raw" ] && return 0
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$raw" | python3 -c "import json,sys
try:
 print('\n'.join(d.get('name','') for d in (json.load(sys.stdin) or []) if d.get('type')==sys.argv[1]))
except Exception:
 pass" "$external_type" 2>/dev/null || true
  else
    printf '%s' "$raw" | tr -d '\n' | sed 's/},[[:space:]]*{/}\n{/g' | grep -E "\"type\":[[:space:]]*\"$external_type\"" | sed -n 's/.*"name":[[:space:]]*"\([^"]*\)".*/\1/p' || true
  fi
}

# stdin: `satelle story doc` JSON; exit 0 only when the body, less its
# frontmatter and whitespace, is non-empty.
body_nonempty() {
  local raw
  raw=$(cat)
  [ -z "$raw" ] && return 1
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$raw" | python3 -c "import json,re,sys
try:
 b=json.load(sys.stdin).get('body') or ''
except Exception:
 sys.exit(1)
b=re.sub(r'\A---\n.*?\n---\n','',b,count=1,flags=re.S)
sys.exit(0 if b.strip() else 1)" 2>/dev/null
  else
    printf '%s' "$raw" | tr -d '\n' | sed -n 's/.*"body":[[:space:]]*"\(.*\)"[[:space:]]*}.*/\1/p' | sed 's/^---\\n.*\\n---\\n//' | sed 's/\\[nt]//g' | tr -d '[:space:]' | grep -q .
  fi
}

# A recorded row whose range git cannot show is used as recorded; unres names it.
rec=""
unres=""
if recorded=$(satelle story diff "$sid" --recorded 2>/dev/null); then
  rec=$(printf '%s' "$recorded" | extract_files)
  unres=$(printf '%s' "$recorded" | extract_files unresolved_records)
fi

liv=""
if live=$(satelle story diff "$sid" --include-substrate 2>/dev/null); then
  liv=$(printf '%s' "$live" | extract_files)
fi

# Commits whose SUBJECT names the story. The trailing "(sty_…)" subject
# convention is what marks ownership; a citation in the BODY of another story's
# commit is not. The --grep narrowing is a cheap candidate filter only — the
# subject test below is the rule, and dropping it brings the body-citation
# misattribution back.
com=""
owned=""
for c in $(git log --grep="$sid" --format=%H 2>/dev/null || true); do
  subj=$(git show -s --format=%s "$c" 2>/dev/null || true)
  case "$subj" in *"$sid"*) owned="$owned $c" ;; esac
done
if [ -n "$owned" ]; then
  com=$(for c in $owned; do git show --name-only --format= "$c" 2>/dev/null; done | grep -v '^$' || true)
fi

changed=$(printf '%s\n%s\n%s\n' "$rec" "$liv" "$com" | grep -v '^$' | sort -u)
if [ -n "$unres" ]; then
  echo "recorded rows used as recorded (range unresolved):"
  printf '%s\n' "$unres"
fi
if [ -n "$changed" ]; then
  # A non-doc repo path is rejected whether or not external evidence is attached.
  offenders=$(printf '%s\n' "$changed" | grep -vE "$doc_paths" || true)
  if [ -n "$offenders" ]; then
    echo "the slice for $sid touches paths that are not doc-typed (pattern $doc_paths) — this is not a documentation-only change; move it to the repo's project lane, or widen the pattern by overriding this skill:"
    printf '%s\n' "$offenders"
    exit 1
  fi
  echo "docs-only slice confirmed for $sid:"
  printf '%s\n' "$changed"
  exit 0
fi

# Empty repo change set: the only remaining evidence is an attached document of
# type $external_type with a non-empty body (prose delivered outside the repo).
# --json: both outputs are parsed as JSON, so they must not receive [output]
# compact rendering for an agent caller (satelle help compact-output).
evidence=""
for n in $(satelle story docs "$sid" --json 2>/dev/null | external_names); do
  if { satelle story doc "$sid" "$n" --json 2>/dev/null || satelle story doc "$sid" "$n" 2>/dev/null; } | body_nonempty; then
    evidence="$evidence $n"
  fi
done
if [ -n "$evidence" ]; then
  echo "docs-only slice confirmed for $sid (external document evidence:$evidence)"
  exit 0
fi
echo "no change set found for $sid — nothing recorded, nothing in the working tree since engagement, no commit subject names it, and no non-empty $external_type document is attached. Empty is not evidence of a documentation change. If the deliverable lives outside the repo, record it: satelle story attach $sid --name <doc> --type $external_type --body '<url or page id> — version <n>: <what changed>'"
exit 1
```
