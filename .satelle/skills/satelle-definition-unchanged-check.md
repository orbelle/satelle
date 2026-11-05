---
name: satelle-definition-unchanged-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check on plan → in_progress — rejects when the story's definition was edited after readiness (backlog → plan) was accepted, so no reviewer ever judged that edit. When the accepted plan authored a `## Acceptance criteria` section, the story must carry exactly the lines under it — as the route's apply_criteria applied them. Deterministic, zero LLM; the binary only enumerates the edits.
---

# Definition-unchanged check (implementation-entry functional gate)

The definition stays editable in `plan`; this check refuses an edit made after readiness was accepted.

**Discovery.** The planner authors the criteria in the plan's
`## Acceptance criteria` section and the readiness reviewers judge it. Where the
route declares `apply_criteria`, satelle applies those lines on entry; otherwise
the driver applies them verbatim ([[satelle-discovery-criteria]]).

When the accepted plan has that section, this check **requires** it applied:
it accepts only when the story's criteria equal those lines
(whitespace-normalised) and every edit since readiness is to
`acceptance_criteria`. Criteria not yet applied, criteria that differ, or any
other definition edit (title, body, category) are refused, and a refusal for
unapplied criteria prints the exact command, as a quoted heredoc so the
lines keep their newlines. An unreadable story or plan document is refused,
never treated as "no section". When the plan has no such
section, the original rule holds: any definition edit after readiness is refused.

A non-zero result tells the driver the declared way back: present `plan → backlog`
(the route's recover edge), then re-present readiness, and the edit is judged.

```check
#!/usr/bin/env bash
set -uo pipefail
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload — cannot check the definition"
  exit 1
fi
# --json: this check parses a specific JSON shape, so it must not receive
# [output] compact mode's table rendering (satelle help compact-output).
edits=$(satelle story definition-edits "$sid" --since-edge backlog:plan --json 2>/dev/null || true)
story=$(satelle story get "$sid" --json 2>/dev/null || true)
plan=$(satelle story doc "$sid" plan --json 2>/dev/null || true)
EDITS="$edits" STORY="$story" PLAN="$plan" SID="$sid" python3 - <<'PY'
import json, os, re, sys

sid = os.environ["SID"]

def load(name):
    try:
        return json.loads(os.environ.get(name) or "")
    except ValueError:
        return None

edits = load("EDITS")
if not isinstance(edits, dict) or "count" not in edits:
    print(f"could not enumerate the definition edits for {sid} (satelle story definition-edits returned no count) — refusing rather than assuming none")
    sys.exit(1)
def way_back(reason):
    print(reason)
    print(json.dumps(edits, indent=2))
    print("present the declared way back, then re-present readiness so the edit is judged:")
    print(f"  satelle story set {sid} --status backlog")
    print(f"  satelle story set {sid} --status plan")
    print("then re-present plan → in_progress")
    sys.exit(1)

fields = sorted({e.get("field", "") for e in edits.get("edits", [])})
story = load("STORY")
plan = load("PLAN")
if not isinstance(story, dict):
    print(f"could not read the story {sid} (satelle story get) — refusing rather than assuming its criteria")
    sys.exit(1)
# story get --json omits an empty acceptance_criteria (omitempty), which is the
# intent-only case: absent means "", never "unreadable".
if not isinstance(plan, dict) or not isinstance(plan.get("body"), str):
    print(f"could not read the accepted readiness plan of {sid} (satelle story doc {sid} plan) — refusing rather than assuming it authored no criteria")
    sys.exit(1)
body = plan["body"]
m = re.search(r"^##\s+Acceptance criteria[ \t]*\n(.*?)(?=^##\s|\Z)", body, re.M | re.S)

def norm(s):
    return re.sub(r"\s+", " ", s or "").strip()

if m:
    section = m.group(1).strip()
    if norm(story.get("acceptance_criteria", "")) != norm(section):
        print(f"the accepted readiness plan for {sid} authored acceptance criteria, and the story does not carry them verbatim. Apply the lines under '## Acceptance criteria' (not the heading), then re-present plan → in_progress:")
        print(f"  satelle story set {sid} --acceptance \"$(cat <<'CRITERIA'")
        print(section)
        print("CRITERIA")
        print(")\"")
        sys.exit(1)
    if fields and fields != ["acceptance_criteria"]:
        way_back(f"the definition of {sid} was edited after readiness (backlog → plan) was accepted, so no reviewer judged the edit (fields: {', '.join(fields)}):")
    print(f"the acceptance criteria of {sid} equal the accepted plan's '## Acceptance criteria' lines, which the readiness reviewers judged")
    sys.exit(0)

if edits["count"] == 0:
    print(f"no definition edit since readiness was accepted for {sid}")
    sys.exit(0)
way_back(f"the definition of {sid} was edited after readiness (backlog → plan) was accepted, so no reviewer judged the edit (fields: {', '.join(fields)}):")
PY
```
