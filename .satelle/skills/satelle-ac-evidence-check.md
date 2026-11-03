---
name: satelle-ac-evidence-check
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check on in_progress → integration — rejects when the story has no ac-evidence attachment. Deterministic, zero LLM tokens; runs before satelle-code-ac-review so a missing attachment does not spend a reviewer turn.
---

# AC-evidence presence check (integration-entry functional gate)

Rejects entry to `integration` when the story has no `ac-evidence` attachment; adequacy is satelle-code-ac-review's. A slice with nothing to prove attaches it with an n/a note.

```check
#!/usr/bin/env bash
set -uo pipefail
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload — cannot check ac-evidence"
  exit 1
fi
# --json: this check greps a specific JSON shape below, so it must not
# receive [output] compact mode's table rendering even when this repo
# defaults compact on for an agent caller (satelle help compact-output).
# Fall back to plain `story docs` (no --json) for a released binary that
# predates the flag, so the check still works pre- and post-upgrade.
docs=$(satelle story docs "$sid" --json 2>/dev/null || satelle story docs "$sid" 2>/dev/null || true)
case "$docs" in
  *'"name": "ac-evidence"'*|*"\"name\":\"ac-evidence\""*)
    echo "ac-evidence attachment present for $sid"
    exit 0
    ;;
esac
echo "missing ac-evidence attachment for $sid — attach the per-AC test mapping before retrying in_progress→integration:"
echo "  satelle story attach $sid --name ac-evidence --type note --body '…'   # or --file <path>"
echo "Then re-present: satelle story set $sid --status integration"
exit 1
```
