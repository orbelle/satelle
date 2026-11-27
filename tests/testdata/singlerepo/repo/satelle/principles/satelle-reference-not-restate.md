---
name: satelle-reference-not-restate
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
framing: true
description: A .satelle file states what was true as of when it was written. Verify a volatile fact by running the command that answers it before relying on it; when writing, point to that command rather than writing the fact down.
---

# Reference, not restate

Read a statement under `.satelle/` as true on the day it was written. Before relying on a volatile fact it states, run the command that answers it.

Name the command, file or `[[link]]` that answers a volatile question (versions, model ids, counts, statuses, file:line, current defaults) instead of its current answer.

A volatile fact may appear only as dated history: `on <date> X was Y`.

Rules and process are stated plainly.

`satelle validate --session <session>` lists the lines that the patterns in `.satelle/volatile.toml` match.

For example, point to `satelle workflow show --session <session>` for the gates on a transition rather than copying the gate list into the page.
