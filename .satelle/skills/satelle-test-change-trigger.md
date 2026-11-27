---
name: satelle-test-change-trigger
type: check
description: The trigger of satelle-test-change-review. Exits 0 when the story's diff touches a path under tests/, exits 1 when it touches none, so the review is dispatched only for a story that changed a test. Deterministic and self-contained; the path pattern is the script's first line.
---

# Test-change trigger

Not a verdict. A gate that declares `trigger = "satelle-test-change-trigger"`
runs this script before it is enqueued: **exit 0 runs the gate, exit 1 skips
it** and its output is noted, and any other exit means the trigger is broken,
so the gate runs anyway.

The story's change set is the `paths` of `satelle story diff <id> --json --session <session>`, the
id being `$SATELLE_ITEM`. When any path in it matches `TEST_PATHS`, the
review runs; otherwise it is skipped, and no judge is dispatched. An item that
is not a story has no story diff: it is skipped, exit 1, with a line saying so.

```check
#!/usr/bin/env bash
set -uo pipefail

TEST_PATHS='^tests/'

command -v jq >/dev/null 2>&1 || { echo "jq is not installed"; exit 2; }
item=${SATELLE_ITEM:-}
if ! printf '%s' "$item" | grep -qE '^[a-z]+_[A-Za-z0-9]+$'; then
  echo "SATELLE_ITEM (${item:-unset}) is not an item id"
  exit 2
fi
case $item in
  sto_*) sid=$item ;;
  *) echo "$item is not a story: it has no story diff to judge; test-change review not needed"; exit 1 ;;
esac
if ! diff=$(satelle story diff "$sid" --json 2>&1); then
  echo "could not read the diff of $sid: $diff"
  exit 2
fi
if ! all=$(printf '%s' "$diff" | jq -r '.paths // [] | .[]'); then
  echo "could not read the paths of $sid's diff"
  exit 2
fi
files=$(printf '%s\n' "$all" | grep -E "$TEST_PATHS" || true)
if [ -z "$files" ]; then
  echo "no path under tests/ changed for $sid; test-change review not needed"
  exit 1
fi
echo "test paths changed for $sid: $(printf '%s' "$files" | tr '\n' ' ')"
exit 0
```
