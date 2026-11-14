---
name: satelle-lessons
scope: project
type: skill
tags: [solo-dev, executor, lessons]
description: Executor skill for capturing typed lessons from a finished story into durable artifacts for later retrospection and process improvement.
---

# Lessons (post-release friction capture)

You are the **lessons** agent, dispatched once when a project story enters
`done`. Under flat dispatch nothing fires on entry: the ORCHESTRATOR runs
`satelle story retrospect <id> --session <session>` after close, and the route names this advisor.
Capture
**satelle's own friction** from this story as a durable, offline corpus —
not generic work notes.

**Attach only.** Do not change story status, do not edit code, do not file
improvement stories (that is `satelle-retrospective` / `satelle story retrospect --session <session>`).

## 1. Reconstruct

Pull by story id from the payload:

```bash
satelle story get <id> --session <session>
satelle story docs <id> --session <session>
satelle story doc <id> plan --session <session>          # if present
satelle story doc <id> release-summary-<id> --session <session>  # if present
satelle ledger list --story <id> --session <session>
```

## 2. Extract satelle-specific friction

Record only friction **about satelle itself** while doing the work:

- Context contradictions (two resident principles disagreeing)
- Gate / hook confusion (edit-gate, commitgate, unclear denies)
- Misaligned or duplicated substrate (stale tags, double paths)
- Unclear messages or missing surfaces that cost a retry loop

**Not** product-domain notes, feature ideas, or generic "what I did".

If nothing notable, attach a short body saying so — a clean run is valid.

## 3. Attach

```bash
satelle story attach <id> --name lessons --type lessons --body "…" --session <session>
```

Body shape (markdown):

```markdown
# Lessons — <story title>

## Friction
- …

## What to do next time
- …
```

The attachment is **not** session-resident (no `principles:session`). Discover
later with `satelle story lessons --session <session>` (cross-story) or `satelle story docs <id> --session <session>`.
The context-audit task may consume this corpus optionally.

See [[satelle-agent-model]], [[satelle-residency]].
