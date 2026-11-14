---
name: satelle-story-architecture-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Readiness (backlog → plan) multi-reviewer axis. Isolated read-only judge — does the story fit the target architecture ([[satelle-sync-architecture]]), and does the plan respect mechanism-vs-substrate and avoid putting decisions, or the user's own vendors, tools and policy, in Go when they belong in config? It may block a misfit, or push back an architecture amendment when the architecture is what is wrong. Parallel companion to satelle-story-plan-review.
---

# Story architecture review (backlog → plan, readiness)

Read-only. Judge the plan's architecture (plan-review judges coverage); read the
`plan` attachment from the record folder your prompt names (`attachments/plan`).

## How to judge

- **Architecture fit.** Read [[satelle-sync-architecture]] in full (and
  [[satelle-story-task-release-architecture]] for `epic:story-task-release`);
  trace what the change stores, moves, derives and exposes; reject, citing the
  section and plan element, where the plan breaks a section or a §6 rule. A
  pre-existing finding owned by a named story does not block. A story that
  touches no sync, storage, scope, layering or verb surface passes in one line.
- **Rule 3 proof is a release-step AC.** The §6 rule-3 machine-wide proof
  (a session start in each registered repo, before and after `satelle update --session <session>`)
  is evidence that
  exists only after release. Accept a plan that puts it on the release step as a
  release-step AC; reject a plan that puts it on a code or integration
  criterion ([[satelle-story-shape]]).
- **Mechanism vs substrate** ([[satelle-mechanism-vs-substrate]]) — behaviour
  that should be configuration (the route, skills, agents.toml, tags) must not
  be hardcoded as a special case in Go.
- **Single ownership** — new logic lives in the owning package; no duplicated
  domain calculations in consumers. The plan's `## Existing owner` section must
  record, before its design, where each rule or seam it touches already lives
  (package, symbol, route or config key) or that a search found none. Search the
  repo yourself: when the design adds a second owner beside an existing one, or
  a new owner without the recorded search, reject and cite the existing owner
  you found (or the missing search). For each of these kinds the design touches,
  the section must name the owner found or the search that found none; a kind
  touched but skipped is a reject for that omission:
  - where state or a cache is stored;
  - how a process is spawned or detached;
  - how a root, pin or path is detected;
  - which constant or predicate decides freshness or liveness;
  - which existing record already holds the same fact.
  A plan that adds a path replacing an existing one deletes the old path
  (migrating its callers) in this story; keeping both is a second owner —
  reject. This check is owned here; sibling rubrics do not repeat it.
- **No silent policy** — opt-in attrs and reviewer skills make decisions visible
  in the authored workflow, not buried in engine defaults unless documented.
- **Agent-agnostic** ([[satelle-agent-agnostic]] points 1 and 2, a standing
  check even when the ACs are silent on other agents) — a plan that touches what
  that principle governs states, for each supported adapter, whether it works or
  records an adapter-named "unavailable". Reject a single-provider mechanism
  that does not state the other adapters' handling (name them), and provider
  model names, flags or tool names outside an adapter or configuration.
- **User configuration** — **reject** a plan whose design adds to the binary
  anything [[satelle-mechanism-vs-substrate]] lists as user configuration,
  including a vendor or provider adapter for the user's own tools or clouds and
  a closed provider list, or that adopts a review note recommending such a seam.
  Cite the plan element and name the principle's alternative. This applies
  whoever raised the story, including the operator.
- **Architecture amendment.** When the story is right and the principle wrong,
  reject with a finding headed `architecture amendment` naming the section and
  replacement text; never accept a departure on argument alone.

Accept when the plan's shape is sound on those axes (or it is clearly
non-architectural). Fair gate: ACs and plan as written, not maximality.

Raise every blocking finding at once, numbered; your prompt carries no earlier
verdict.

## Verdict

```json
{"verdict": "accept", "notes": "<what you judged: quote the work, or name the file, criterion or record path you checked>"}
```
