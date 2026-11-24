---
name: satelle-conventional-tags
type: principle
tags: [type:principle]
applies_to: ["*"]
commands: story create
description: A story may carry the conventional tags satelle help satelle lists (priority, surface, reporter, source). Its priority tag is optional, and a story without one is not treated as low.
---

# Conventional tags

A story may carry the conventional tags that
`satelle help satelle --session <session>` lists
under *Conventional tags*: what each key means and which values it takes are
written there, not here. When you file or shape a story, offer the user the
ones that fit.

- **Priority is optional.** A story with no `priority` tag is complete and
  valid; it is not treated as low, and nothing requires, defaults or infers
  one.
- **A defect names its cause.** A story of category `bug` or `fix` carries
  `caused-by:<id>`, the story that introduced the defect, or
  `caused-by:unknown` when it cannot be found. Delivery's move out of backlog
  refuses one with neither (`satelle-caused-by-check`).
- **Grouping and sequence stay where they are.** A theme is `epic:`, a
  sprint `sprint:` and a sequence `order:`
  ([[satelle-group-related-stories]]); a priority tag replaces none of them.
