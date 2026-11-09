---
name: satelle-story-cancel-review
scope: system
type: skill
tags: [type:skill, type:reviewer]
description: Exit gate for cancel (any → cancelled). Isolated reviewer — requires a recorded reason; parents by children-resolved; supersede claims must name delivering story/commit and verify AC coverage; sibling bundling is an explicit reject. Review-only.
---

# Story cancel review (abandon gate)

Isolated reviewer deciding whether a story may move to **cancelled**. Your input
is your prompt (the item, its tags and body, the move) and the read-only record
folder it names: the item's attachments (`attachments/<name>`) and, for a
parent, its children (`children.md`, one `- id [status] title` line each).
Cancelling is a normal operator-driven outcome — not a quality failure — but it
must be **deliberate and evidence-grounded**. You judge and do not edit; never
invent a supersede or cancel children on the operator's behalf.

## Floor — reason required

**Reject** when there is **no reason on record**: a body note, or a
`cancel-reason` attachment in the record folder (you cannot read the ledger). A
bare cancel erases the item without audit trail. A `cancel-reason:` tag is
**metadata**, not a substitute — a tag alone with no reason on record still
rejects.

## Branch on `story.category`

### Parent / epic-parent — children resolved

Accept the cancel **only** when every child in the record folder's `children.md`
is `done` or `cancelled` (or there are none). On reject, list each unresolved
child as `id (status)`. Judge from the record's children list.

The gate **refuses and names**; it never cancels children itself. Shelving a
container is a deliberate cascade — resolve children first, then the parent.

### Empty (category empty)

A `category: empty` story proves a workflow or a remote, so it is often
cancelled once it has. Its emptiness is not grounds to object: it needs no
delivered work, no diff and no supersede, only the reason floor above.

### Every other category

The floor, supersede, and sibling-bundling rules below apply unchanged.

## Cancel-reason tag (when the repo declares it)

When the repo declares a `cancel-reason` controlled namespace in `satelle.toml`
`[tags.vocabulary]`, expect the story to carry a `cancel-reason:<value>` tag
consistent with the recorded reason; name a missing or mismatched tag in notes.
**Where no such namespace is declared, do not require the tag** — judge as above.

## Supersede / delivered-elsewhere claims

If the reason claims the work was **delivered under another story or commit**:

1. The claim must **name** the delivering story id and/or commit sha. Vague
   "covered elsewhere" / "already done" without a name → **reject**.
2. **Verify** against the tree (Read/Grep; you have no shell): does that
   delivery actually satisfy **this** story's numbered ACs?
3. **Unverifiable** claim → **reject** (name what is missing).

### Sibling bundling (canonical reject)

Sibling bundling is one story cancelled as delivered by a sibling (same
parent or epic) that does not cover all of its criteria. On cancel, apply it as follows and put all four items in **notes**:

1. The cancelled story id and the named deliverer id
2. That they are siblings (same parent/epic)
3. Which cancelled ACs the deliverer does **not** cover
4. That this is a scope breach, not a legitimate supersede

**Reject** when those hold. Do **not** accept bundling so the cancel note
"passes" after a quick rewrite.

### Seat contention (canonical reject)

**Reject** any cancel whose justification is that the engagement seat was needed
— "freeing the engagement seat", "another story must be engaged", "blocking
higher-priority work", "preempted so we can start X". No value in the repo's
declared `cancel-reason` vocabulary (`satelle.toml` `[tags.vocabulary]`) means
*preempted*; a `cancel-reason:` tag present anyway is metadata, not
justification, and does not rescue it.

Notes must name the correct path: park the story, moving it to `blocked` with
the reason in its body (the blocked review admits preemption). Cancel of a
healthy paused story to free the seat cannot pass this gate.

### Legitimate supersedes (accept with named evidence)

- Duplicate of another story that **did** scope the same ACs
- Requirement **withdrawn** by the operator (reason says so clearly)
- Delivery by a story that **legitimately** scoped this work in its ACs
- External/upstream delivery with named evidence (issue, PR, commit)

## Terminality and revival

**Cancelled is terminal.** Revival is a **new** story tagged `supersedes:<id>`,
never a reopen of the cancelled record — same path [[satelle-agent-goals]]
prescribes for the AC-wrong branch. A cancel-reason tag never makes the record
resumable.

## On reject — stop and surface

Notes must be actionable for the **operator**. Instruct the executor to
**stop** and surface the refusal — **do not** iterate cancel notes until the
gate accepts. The ledgered `review_reject` is the visible warning.


## Verdict

```json
{"verdict": "accept", "notes": "<what you judged: quote the work, or name the file, criterion or record path you checked>"}
```
