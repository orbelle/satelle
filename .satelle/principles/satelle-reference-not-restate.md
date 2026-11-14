---
name: satelle-reference-not-restate
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: Point to where a changing fact is answered rather than writing the fact down. A changing fact is written only with the date it held, and rules are written plainly.
---

# Reference, not restate

Name the command, file or `[[link]]` that answers a volatile question (versions, model ids, counts, statuses, file:line, current defaults) instead of its current answer.

A volatile fact may appear only as dated history: `on <date> X was Y`.

Rules and process are stated plainly.

`satelle validate --session <session>` lists the lines that the patterns in `.satelle/volatile.toml` match.

For example, point to `satelle workflow show --session <session>` for the gates on a transition rather than copying the gate list into the page.
