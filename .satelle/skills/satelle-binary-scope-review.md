---
name: satelle-binary-scope-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Readiness (backlog → plan) reviewer. Isolated read-only judge of the plan's footprint in the satelle binary — rejects a plan that adds a git or forge action, or process logic that belongs in a step skill or configuration, to the binary, per [[satelle-mechanism-vs-substrate]]. A re-presented edge follows [[satelle-reviewer-verdict]].
---

# Binary scope review (backlog → plan, readiness)

Read-only. Open the `plan` doc's `path` and judge only where the planned work
lives. The rule is [[satelle-mechanism-vs-substrate]], section "Git and the
forge are a step's work": the binary is a small mechanism that runs the user's
configuration through its reviewers and gates, and it takes no git or forge
action.

## Judge

1. **Git and forge actions.** List every action the plan has the binary take on
   a repo or a forge: commit, push, merge, branch, tag, clone, fetch that
   changes refs, pull request, review, status. **Reject** each one the plan puts
   in Go code under `internal/` or `cmd/`, naming the planned file and the
   action, and say which step skill should perform it instead. A step skill,
   task or check script under `.satelle/` running git is fine.
2. **Process in the binary.** **Reject** a plan that encodes in Go what a route,
   skill, gate or setting should hold: a release or merge procedure, a fixed
   order of forge calls, a policy choice (which review to post, whether a
   self-approval counts), or behaviour that only makes sense for one repo's
   flow. Name the file and where it belongs (`.satelle/workflows`, a skill, or
   a setting).
3. **Mechanism is fine.** Accept binary work that runs configuration: parsing
   and validating config, running reviewers and gates, recording the ledger,
   syncing state, dispatching agents, reading state a gate or the configuration
   needs. A read-only fact may come from git; prefer the single read-only git
   adapter named in the principle's debt note.
4. **Existing debt.** A plan that only touches code already listed as debt is
   not rejected for that debt; reject only what the plan adds.

Every finding names the planned file, the action or logic, and where it
belongs. Judge the plan as written; do not design the change.

## Verdict

```json
{"decision": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>"}
```
