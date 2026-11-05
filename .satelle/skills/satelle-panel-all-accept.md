---
name: satelle-panel-all-accept
scope: system
type: skill
tags: [type:skill, type:functional-check]
description: Panel combine check — exit 0 only when every declared seat accepted and none is unavailable. The rule is this script; the binary does not branch on the name.
---

# Panel combine: all accept

Functional-check skill a gate names as `combine`. stdin is the seat record the
engine built, and nothing else:

```json
{"seats":[{"seat":"reviewer-claude","accept":true,"unavailable":false,"notes":""}],"declared":3}
```

`declared` is the panel length, including seats that produced no process. Exit 0
only when every declared seat is present, `accept` is true, and `unavailable`
is false. Otherwise exit 1. Stdout names each dissenting seat and each
unavailable seat.

Unavailable stays in the count. Dropping those rows and judging `len(seats)`
would shrink the panel; this script does not.

The check does not call satelle. A repo that wants a different rule authors
another skill and names it; this file is not a compiled verdict.

```check
#!/usr/bin/env bash
set -uo pipefail
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to fold the panel"
  exit 1
fi
python3 -c "
import json, sys
raw = sys.stdin.read()
try:
    data = json.loads(raw)
except Exception as e:
    print('combine input is not JSON: %s' % e)
    sys.exit(1)
seats = data.get('seats')
declared = data.get('declared')
if not isinstance(seats, list) or not isinstance(declared, int):
    print('combine input needs seats and declared')
    sys.exit(1)
dissent = []
unavailable = []
accepts = 0
for s in seats:
    if not isinstance(s, dict):
        unavailable.append('?')
        continue
    name = s.get('seat') or '?'
    if s.get('unavailable'):
        unavailable.append(str(name))
        continue
    if s.get('accept'):
        accepts += 1
    else:
        dissent.append(str(name))
missing = declared - len(seats)
if missing > 0:
    unavailable.append('%d declared seat(s) absent' % missing)
parts = []
if dissent:
    parts.append('dissent: ' + ', '.join(dissent))
if unavailable:
    parts.append('unavailable: ' + ', '.join(unavailable))
if parts:
    print('; '.join(parts))
sys.exit(0 if accepts == declared and declared > 0 else 1)
"
```
