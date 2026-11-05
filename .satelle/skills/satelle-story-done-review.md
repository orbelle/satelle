---
name: satelle-story-done-review
scope: system
type: skill
tags: [type:skill, type:reviewer]
description: Spine gate into done. Isolated read-only reviewer — parents by children-resolved; others by the behavior on the story card, then the residual ACs, against presented evidence. Does not re-plan at close. A re-presented edge follows [[satelle-reviewer-verdict]].
---

# Story done review (→ done)

## Primary objective

Validate the **presented** close evidence against the **story**. Answer only:
may we close? Do **not** re-plan or redesign at close.

You get `{story, from, to}` (and `children` for parents). You do not edit.

## How to judge

**Branch on `story.category`.**

### Parent / epic-parent — children resolved

Accept only when every child is `done` or `cancelled` (or none). List
unresolved on reject. Do not judge the parent's own ACs.

The injected `children` list is a **snapshot** taken when you were prepared; it
is a hint, not the check. Who the children are is defined in
[[satelle-story-classification]]. For an epic-parent, the commit re-reads that
set from the store and refuses a close over a member filed or reopened since.
A plain parent container has no such re-read. A clean snapshot is not a licence
to wave through children you can see are open.

### Every other story — the behavior on the card, then the ACs

The numbered criteria derive from the card's behavior; they are not a second
spec. Judge the behavior first.

1. **Read the story body** and name the behavior it states.
2. **Judge the change against that behavior** with evidence you can see (tree,
   tests, story attachments, op-log). Prefer upstream summaries when present.
3. **Record it as criterion `0`.** When the behavior holds, send
   `{"criterion": 0, "status": "accept", "finding": "behavior holds: <the behavior, quoted>"}`.
   When it does not hold, reject with
   `{"criterion": 0, "status": "reject", "finding": "<the behavior, quoted>"}` —
   **even when every numbered AC is met**: a technical criterion that passes
   while the behavior does not is a refusal, and the refusal names the behavior.
4. **Then walk the numbered ACs**; each must be plausibly met by the same
   evidence. Reject unmet ACs — name them. Fair gate: ACs as written.
5. **A criterion that cannot be met while the behavior holds** (for example a
   proof that could only have been captured earlier) is recorded
   `{"criterion": <n>, "status": "unmet", "finding": "<why>"}` beside the
   criterion `0` accept. It is not amended, not waived, and not silently passed:
   the gate accepts it only with that accept, and names it in its notes. With no
   criterion `0` accept, `unmet` fails like `unchecked`.

**No numbered criteria** (a route with no plan step): compare the change to the
card's behavior directly and record it as the criterion `0` entry. The absence of
criteria is not acceptance and not rejection; it leaves the card as the whole
spec.


## Verdict

```json
{"decision": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>"}
```

For a non-parent story add the `criteria` entries from the steps above.
