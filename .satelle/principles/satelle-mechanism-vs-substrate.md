---
name: satelle-mechanism-vs-substrate
type: principle
tags: [type:principle]
applies_to: ["*"]
description: What belongs in the binary (mechanism) versus authored substrate (process, gates, opinions). No gate as code, user configuration never compiled in, no route as judgement, flat dispatch, quality over speed, and the test for where a change belongs.
---

# Mechanism vs. substrate

The binary runs process; it never is process. Workflows, gates, skills and
opinions are substrate under `.satelle/`, layered over the embedded defaults (a
same-named file overrides; the embedded source is never edited).

- **No gate as code.** A gate is a reviewer skill plus an optional functional
  check command. The binary runs it and never decides a verdict in a Go
  branch. A check may invoke binary mechanism; the pass/fail rule stays
  configuration even when deterministic.

## User configuration is never compiled in

User configuration is never hardcoded in the binary. It is:
- a named vendor, provider or cloud, and the user's own tools and CLIs;
- a provider's or tool's argv, environment variables and config paths;
- any list of commands, rules or policy entries;
- any behaviour that makes sense only for one user's setup.

The binary offers a generic mechanism that runs what configuration names. A
closed provider list ("unknown provider (want azure, aws)") is the defect.
Naming a vendor in help, comments or examples is fine; requiring it in code is
not. The agent adapters, dispatch (`internal/agent`) and harness hooks
(`internal/hook`, including each harness's hook file location), are mechanism
([[satelle-agent-agnostic]]). This applies whoever raised the story, including
the operator.

**satelle's own system processes are shipped substrate, not compiled-in
configuration.** The init template is substrate the binary ships and writes
into each repo's `.satelle/`, where the repo owns and overrides it: satelle's
maintenance and substrate-alignment processes, how agents use satelle, and its
skill rules (for example reference over content). The binary reads the repo's
copy, never the embedded one, so a default the template names (a pattern, a
principle's text, a route) is the repo's configuration once written (operator,
2026-11-13).

## Route, dispatch, quality

- **No route as judgement.** An agent may add steps or remediation, never drop
  a step, reorder past a pending gate, or satisfy an obligation by assertion.
  Topology is mechanism; route content is configuration.
- **Flat dispatch.** The orchestrator alone schedules (`orch → step → orch`).
  Steps never call steps; a reviewer returns a verdict and never triggers
  remediation; an advisor advises the orchestrator, never the step.
- **Quality over speed.** A route is never shortened to go faster; when speed
  and a gate conflict, the gate wins.

## The test

A change that only makes sense because of how this repo works (its
`.version`, `install.sh`, GitHub Actions, release flow, deploy mechanics)
belongs in `.satelle/`, never in the binary.

## Git and the forge are a step's work

The binary is read-only on the user's repo, its refs and its remotes: it reads
them (status, diffs, merge bases, a bundle written outside the repo) and never
commits, pushes, merges, branches, tags or rewrites them (operator,
2026-11-08). Git it runs in a scratch repo it creates for one run (a remote
node's workspace, the docker check gate's node) is mechanism, not a git action
on the repo. Commits, pushes, merges, pull requests, reviews and commit
statuses are a step skill's actions, which the agent performs under the step's
configuration; a forge verb the binary carries (`satelle github review --session <session>`)
publishes only what the calling step gives it. The binary stays a small
mechanism that runs the user's configuration through its reviewers and gates;
a plan that moves process into it is refused at plan review
(satelle-binary-scope-review). The binary's existing git use is debt,
inventoried with a decision per call in sty_042c7fa3.

The binary also changes no git-tracked content of the user's repo. The repo is
protected by the user's own method, so an issue there is resolved through the
repo's workflow: satelle advises with a warning that names the fix and asks how
to resolve it, and a warning left unresolved turns into an error (operator,
2026-11-13; sto_c26c300c). Untracked, machine-local files satelle put there
(the harness hooks it installed, its runtime records) are satelle's to
maintain.

## Secrets arrive as environment

A secret reaches a script or an agent only through its environment, set when
the process starts: on a remote, satelle.dev resolves the definition's `[env]`
and the launcher writes it into the node's environment; locally, a binding's
secrets are passed into the agent CLI's environment. A script reads the
variable. satelle never substitutes a secret into text it returns to an agent,
so a value never enters an agent's context, transcript, the ledger or a commit.
