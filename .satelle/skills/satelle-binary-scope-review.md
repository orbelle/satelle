---
name: satelle-binary-scope-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Readiness (backlog → plan) reviewer. Isolated read-only judge of the plan's footprint in the satelle binary — rejects a plan that adds a write to the user's repo, its refs or remotes, or a forge action, or process logic that belongs in a step skill or configuration, to the binary, per [[satelle-mechanism-vs-substrate]].
---

# Binary scope review (backlog → plan, readiness)

Read-only. On plan → in_progress, read the `plan` attachment from the record
folder your prompt names (`attachments/plan`) and judge only where the planned
work lives. The rule is [[satelle-mechanism-vs-substrate]], section "Git and the
forge are a step's work": the binary is a small mechanism that runs the user's
configuration through its reviewers and gates. The binary is read-only on the
user's repo, its refs and its remotes: it reads them and never writes them.
Git it runs in a scratch repo it creates for one run (a remote node's
workspace, the docker check gate's node) is mechanism, not a git action on the
repo (operator, 2026-11-08, [[satelle-operator-rulings]]).

## Judge

1. **Git and forge actions.** List every action the plan has the binary take on
   the user's repo, its refs, its remotes or a forge. **Reject** each planned or
   changed Go path under `internal/` or `cmd/` that writes the user's repo, its
   refs or its remotes (commit, push, merge, branch, tag, reset, worktree
   removal, a fetch that changes its refs) or takes a forge action (pull
   request, review, status), naming the file and the action, and say which
   step skill should perform it instead. **Accept** Go that only reads the
   user's repo (status, diffs, merge bases, a bundle written outside the repo),
   and git the binary runs in a scratch repo it creates for one run, a clone or
   fetch into it included. A step skill, task or check script under `.satelle/`
   running git is fine.
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
4. **No exemption.** A plan that adds to or extends process already in Go is
   rejected like a new one: existing debt is never a reason to accept more.
5. **Name what each Go change decides.** For every planned change under
   `internal/` or `cmd/`, the notes say in one line what the change decides
   (e.g. "decides the gate order from configuration"). A change whose decision
   names a state, step, skill, seat, category or tag of this repo's workflow is
   process: reject it.

This reviewer runs first on plan → in_progress and fails fast. It runs again on
in_progress → integration, where it judges the changed paths and the diff your
prompt carries instead of the plan (Read a changed file when the diff is cut).

Every finding names the planned file, the action or logic, and where it
belongs. Judge the plan as written; do not design the change.

## Verdict

```json
{"verdict": "accept", "notes": ""}
```
