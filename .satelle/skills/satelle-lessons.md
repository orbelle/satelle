---
name: satelle-lessons
scope: project
type: skill
tags: [type:skill]
description: Post-release friction capture — attach a typed lessons document recording satelle's OWN process/tooling friction (context contradictions, gate confusion, substrate misalignment). Reads the ledger as a record of what the GATES DID, never as evidence of what the delivered work IS. Attach-only; never advances status; never injected into session context.
---

# Lessons (post-release friction capture)

Dispatched after close (`satelle story retrospect`). Capture satelle's own friction from this story.

**Attach only.** Do not change story status, do not edit code, do not file
improvement stories (that is the epic retrospective, relayed with `satelle story retrospect`).

## 1. Reconstruct

Pull by story id from the payload:

```bash
satelle story get <id>
satelle story docs <id>
satelle story doc <id> plan          # if present
satelle story doc <id> ac-evidence   # if present — the per-AC realised-test map
satelle story doc <id> release-summary  # if present
satelle ledger list --story <id>
```

## 2. What the ledger is evidence OF

The ledger records **what the gates did**. It is not evidence of **what the
delivered work is**.

A run with 14 `review_accept` and 0 rejects tells you the gates accepted. It
does **not** tell you the code matched its acceptance criteria — a reviewer that
missed a defect and a slice with no defect produce the identical ledger. Write
what you can support:

| You observed | You may write | You may NOT write |
| --- | --- | --- |
| 0 rejects, 0 blocked transitions | "no recorded friction on this path" | "none material", "clean run", "no problems" |
| every gate accepted first-pass | "every gate accepted on the first present" | "the slice was correct", "the gates confirm the work" |
| a reject and a re-present | the friction, its cost, and the fix | — |

**Never** conclude from gate outcomes alone that:

- the delivered work satisfied its acceptance criteria;
- this run is a **baseline** other runs should emulate;
- **no process change is required**.

If you have no evidence beyond the ledger, say the run produced **no recorded
friction** and stop there.

## 3. Verified vs unverified lessons

A lesson that restates an **acceptance criterion** as a forward-looking rule
("keep doing X", "always represent Y as Z") is a claim that the tree honours X
today. Your grant is read-only but it does reach the repository — so **check it**
before you write it as practice:

```bash
grep -rn "<the symbol or rule>" internal/ cmd/ tests/   # or read_file / list_dir
```

- **Verified** — you found the code that implements the rule: write it as a
  practice, naming the file/symbol you checked.
- **Unverified** — you did not check, or you looked and could not confirm: write
  it as `unverified against the tree`, naming what you would need to check.
  Never restate a criterion as a confirmed practice on the strength of the
  criterion existing.

## 4. Extract satelle-specific friction

Record only friction **about satelle itself** while doing the work:

- Context contradictions (two resident principles disagreeing)
- Gate / hook confusion (edit-gate, commitgate, unclear denies)
- Misaligned or duplicated substrate (stale tags, double paths)
- Unclear messages or missing surfaces that cost a retry loop

**Not** product-domain notes, feature ideas, or generic "what I did".

Estimate-vs-actual variance is friction only when it names a satelle surface
that caused it; a bare over- or under-estimate is a number, not a lesson.

## 5. Attach

```bash
satelle story attach <id> --name lessons --type lessons --body "…"
```

Exactly **one** lessons document per story. Body shape (markdown):

```markdown
# Lessons — <story title>

## Friction
- …                       # or: no recorded friction — <what the ledger showed>

## What to do next time
- …                       # verified practice, naming what you checked

## Unverified
- …                       # a rule you could not confirm against the tree
```

Omit `## Unverified` when it would be empty. Keep every claim traceable to
something you actually read.

See [[satelle-agent-model]], [[satelle-residency]].
