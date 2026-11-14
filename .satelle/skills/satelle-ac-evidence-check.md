---
name: satelle-ac-evidence-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check on in_progress → integration — rejects when the story has no ac-evidence attachment. Deterministic, zero LLM tokens; runs before satelle-code-ac-review so a missing attachment does not spend a reviewer turn.
---

# AC-evidence presence check (integration-entry functional gate)

Rejects entry to `integration` when the story has no `ac-evidence` attachment; adequacy is satelle-code-ac-review's. A slice with nothing to prove attaches it with an n/a note. The story is `$SATELLE_ITEM`, and its attachments are read with `satelle story attachments <id> --json --session <session>`.

```check
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
if [ -z "$sid" ]; then
  echo "SATELLE_ITEM is not set, so the story's ac-evidence cannot be checked"
  exit 1
fi
if ! atts=$(satelle story attachments "$sid" --json 2>&1); then
  echo "could not read the attachments of $sid:"
  echo "$atts"
  exit 1
fi
if printf '%s' "$atts" | jq -e 'any(.[]; .name == "ac-evidence")' >/dev/null 2>&1; then
  echo "ac-evidence attachment present for $sid"
  exit 0
fi
echo "missing ac-evidence attachment for $sid — attach the per-AC test mapping before retrying:"
echo "  satelle story attach $sid --name ac-evidence --file <path> --session <session>"
echo "Then re-present: satelle story set $sid --status ${SATELLE_TO:-integration} --session <session>"
exit 1
```
