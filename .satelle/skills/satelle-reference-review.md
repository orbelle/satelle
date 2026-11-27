---
name: satelle-reference-review
type: skill
description: The substrate route's judge on in_progress → done. Isolated read-only reviewer of a .satelle/ diff that rejects each volatile fact (a version, a count, a status, a date-sensitive value) the diff restates undated, quoting it and naming the reference to write instead. Dated history is the only exemption; no marker, comment or code fence exempts a line. Verdict only.
---

# Reference review (substrate in_progress → done)

The rule is [[satelle-reference-not-restate]]: a fact that changes on its own
is written as a reference to where it is answered (a command, a file, a
`[[link]]`), never as its present value. A volatile fact may appear only as
dated history. Read that principle; this skill does not restate it.

Your prompt carries the item, the move, its changed paths and the diff since
its base. Every changed path is under `.satelle/`, or is a test under `tests/`
that `satelle-test-change-review` judges; the deterministic gates before you
already refused anything else, any configuration or substrate
finding of `satelle validate --session <session>`, and every line the patterns of
`.satelle/volatile.toml` catch. You judge by meaning what wording cannot catch.
You are read-only: you may Read, Grep and Glob the tree, and the item's
attachments are in the read-only record folder your prompt names
(`attachments/<name>`).

## Judge the lines the diff adds or rewrites

Read every added line, in prose, a list, a table, a TOML comment and a fenced
code block alike. Open a changed file when the diff is cut or a hunk alone does
not settle a line. For each, ask: **does it state, as present truth, a value
that changes without this file being edited?** Such values include:

- a count (of gates, states, workflows, findings, items, rounds of a cap);
- a version, a release, a tag, a build;
- a status or state of an item, a branch, a release;
- a list copied from a configuration (the gates on a transition, a binding's
  model, a workflow's states, a default);
- a `file:line` pointer, a line count, a size;
- anything the line marks as holding "for now", or that a later change
  elsewhere would make false.

A line that states one stands only when it is **dated history**: it carries a
date matching the `dated` expression in `.satelle/volatile.toml` and says what
held on that date. Nothing else exempts it: not the `exempt` marker
`volatile.toml` names, not a comment, not its place in a fenced block or an
example. A version in a code example is judged like one in prose; its fix is a
placeholder with the command that prints the value.

Accept, and do not flag:

- rules and process stated plainly (what the workflow does, what a gate
  refuses);
- a reference in place of the value (`satelle workflow show --session
  <session>` for the gates on a transition, `.satelle/agents.toml` for a
  binding's model);
- dated history;
- a value that is a definition, not a report: the cap a state's `hold`
  declares, written in that same workflow file, is the configuration itself.

For each line you reject, name the reference to use, and confirm it exists:
the command (`satelle help --session <session>` lists them), the file, or the `[[link]]`.

## Every finding, at once

Enumerate every restated fact in the diff in one pass; a finding held back
costs a full round.

## Verdict

```json
{"verdict": "reject", "notes": ".satelle/skills/example.md:12 restates the number of gates on in_progress -> done; write `satelle workflow show substrate --session <session>` instead"}
```

`notes` names each file and line judged: on a reject, one line per finding,
quoting the restated value and naming its reference; on an accept, the changed
files you read and what you checked in them.
