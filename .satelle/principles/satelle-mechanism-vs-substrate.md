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
not. The agent-harness adapters in `internal/agentcli` are mechanism
([[satelle-agent-agnostic]]). This applies whoever raised the story, including
the operator.

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
