---
name: satelle-substrate-only-check
scope: system
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check for substrate close — rejects non-lane paths in the story change set (recorded + live with substrate leg + commits naming it), including uncommitted worktree paths; rejects empty sets. Deterministic, self-contained; managed footprint and edit_exempt_paths allowed.
---

# Substrate-only check (substrate workflow close gate)

**Functional-check** gate on the substrate workflow's close. The workflow
DECLARES it as a scoped reviewer node (`on="done"`); on `in_progress → done` it
verifies the story's **change set** touched **only** substrate-lane paths:

markdown and config under `.satelle/` (workflows, skills, principles,
documents, tasks, agents.toml, hooks, …), `docs/`, the binary's own **managed
footprint** outside `.satelle/` (the root `.gitignore` managed block and harness
hook scaffolds under `.claude/` and `.grok/` that `satelle init`
deploys), or any
prefix in `[gate] edit_exempt_paths` in `satelle.toml`.

Any other path (a `.go` file, `cmd/`, build/CI config) — whether committed or
sitting uncommitted in the working tree — means the change is **not**
substrate-only and belongs on the project workflow — the close is **rejected**.
An **empty** change set (nothing recorded, nothing since engagement, no commit
naming the story — including an empty commit alone) is also **rejected**: empty
is not evidence of a substrate-only change.

Why the managed footprint rides this lane: those files are binary-emitted,
binary-managed **process configuration**, not product code.

## Evidence channels (union — no commit required)

The slice is the **union** of:

1. **Recorded** — `satelle story diff <id> --recorded` (change_record rows)
2. **Live + substrate** — `satelle story diff <id> --include-substrate` (git
   worktree since engagement plus mtime-changed substrate under authored dirs
   and the data dir — so git-ignored `.satelle/` is visible)
3. **Commits** — any commit whose **subject** names the story id (the trailing
   `(sty_…)` convention); a mention only in another commit's body is a citation,
   not ownership

A repo whose `.satelle/` is git-ignored (hosted `[sync] personal` continuity)
closes with **no commit at all** when the live/substrate channel shows only
lane paths. Where the repo tracks substrate in git, commit+push remains the
usual practice but is not a gate precondition.

The check is the embedded ```check script below — **self-contained**, no
external file (see [[satelle-reviewer-self-contained]]). Exit 0 accepts;
non-zero rejects with notes. **Mechanism, not judgment.** See
[[satelle-agent-model]].

```check
#!/usr/bin/env bash
set -uo pipefail
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
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
 d=json.load(sys.stdin)
 print('\n'.join(d.get(sys.argv[1]) or []))
except Exception:
 pass" "$key" 2>/dev/null || true
  else
    printf '%s' "$raw" | grep -oE "\"$key\":\\[[^]]*\\]" | head -1 | sed 's/^"[^"]*"://' | tr ',' '\n' | grep -oE '"[^"]+"' | tr -d '"' | grep -v '^$' || true
  fi
}

# Channel 1: recorded change_record union. A row whose range git cannot show is
# used as recorded; unres names it.
rec=""
unres=""
if recorded=$(satelle story diff "$sid" --recorded 2>/dev/null); then
  rec=$(printf '%s' "$recorded" | extract_files)
  unres=$(printf '%s' "$recorded" | extract_files unresolved_records)
fi

# Channel 2: live git + opt-in substrate mtime (empty-tolerant: no baseline → empty)
liv=""
if live=$(satelle story diff "$sid" --include-substrate 2>/dev/null); then
  liv=$(printf '%s' "$live" | extract_files)
fi

# Channel 3: commits whose SUBJECT names the story (always; not only when
# others empty). The trailing "(sty_…)" subject convention is what marks
# ownership; a citation in the BODY of another story's commit is not. The
# --grep narrowing is a cheap candidate filter only (subject matches are a
# subset of whole-message matches) — the subject test below is the rule, and
# dropping it brings the body-citation misattribution back.
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

# Allowed prefixes: authored substrate, managed footprint, edit_exempt_paths.
allow='\.satelle/|docs/|\.gitignore$|\.claude/|\.grok/'
extra=$(grep -E '^[[:space:]]*edit_exempt_paths' .satelle/satelle.toml 2>/dev/null | grep -oE '"[^"]+"' | tr -d '"')
for p in $extra; do
  esc=$(printf '%s' "$p" | sed 's#[^A-Za-z0-9/]#\\&#g')
  allow="$allow|$esc"
done

if [ -z "$changed" ]; then
  echo "no change set found for $sid — nothing recorded, nothing in the working tree since engagement (incl. git-ignored substrate), and no commit subject names it. An empty commit is not evidence of a substrate change."
  exit 1
fi

offenders=$(printf '%s\n' "$changed" | grep -vE "^($allow)" || true)
if [ -n "$offenders" ]; then
  echo "the slice for $sid touches non-substrate paths (committed or uncommitted in the working tree) — not a substrate-only change; use the project workflow (category fix/feature/chore), or commit them under a project story / stash-revert them:"
  printf '%s\n' "$offenders"
  exit 1
fi
echo "substrate-only slice confirmed for $sid:"
printf '%s\n' "$changed"
```
