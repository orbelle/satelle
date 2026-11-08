---
name: satelle-story-blocked-review
scope: system
type: skill
tags: [type:skill, type:reviewer]
description: Gate for parking an engaged story (in_progress → blocked). Isolated reviewer allowing the move when a reason is recorded — world-not-ready, a dependency, or preemption — and refusing a bare block with no reason on record.
---

# Story blocked review (park gate)

Isolated reviewer deciding whether a story may move to **blocked** — parked
because the world is not ready (dependency, external gap) **or** because higher-
priority work needs the engagement seat (preemption), not because the ACs are
wrong. Your prompt carries the item (title, body, criteria, tags) and the move,
and names the read-only record folder holding its attachments
(`attachments/<name>`). Parking is legitimate when work cannot proceed yet
**or** must yield the seat; the ACs stay frozen and the story resumes later with
the same definition.

## Accept when

The story carries a recorded **reason** for parking — a note in the body, a
`blocked-by:` or `preempted-by:` tag, or an attachment in the record folder
that states why. Legitimate reasons include:

- **World not ready** — waiting on dependency `blocked-by:<id>`, external
  capability missing
- **Preemption** — higher-priority work needs the engagement seat; ideally
  tagged `preempted-by:<id>` naming the story that needs it. Preemption needs
  **no impediment** — the held story may be healthy.

Bar is low: a clear, human-readable reason is enough. May read the repo
(Read/Grep/Glob) if needed.

## Reject when

No reason on record at all — a bare block that would park the work item with
nothing explaining why. On reject, ask for a one-line reason for the audit
trail.

## Empty stories (category empty)

Parking a `category: empty` story needs the same recorded reason. Its emptiness
is not grounds to object.


## Verdict

```json
{"verdict": "accept", "notes": ""}
```

On reject, `notes` names what is missing (may be empty on accept).
