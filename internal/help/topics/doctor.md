# satelle doctor — is satelle ready to govern this repository?

`satelle doctor` is the one diagnostic surface. It answers a single question for
the current repository — and, with `--all`, for every registered one.

It **composes** the checks that already own their rules rather than adding rules
of its own. That is deliberate: `satelle init`, `satelle doctor`, and the
engagement precondition must never form three different opinions about the same
repo, and the only way to guarantee that is for them to run the same code. A
rule that lived only in doctor could not be changed without a binary release,
which is exactly what the constitution forbids.

## What it checks

| Area | Owner |
| --- | --- |
| Every binding's command, transport, timeout, and `${VAR}` resolution | the agents layer check |
| Machine-wide profile references — missing profile, reference cycle, role conflict | profile resolution |
| Workflow node/edge allocations, and lifecycle-hook allocations | the agents layer check |
| Reviewer permission ceilings | the agents layer check |
| Workflow / skill / principle / task structure | the authored-substrate contracts |
| Cross-workflow consistency — ambiguous `applies_to`, unresolved skills | the workflow consistency check |
| Required binaries — each isolated binding's executable is on PATH | doctor |
| Harness hook scaffolding vs this binary's canonical wrappers | the scaffold check |
| The context every agent receives — fit, size, duplication, references | the context audit (below) |

## The context every agent receives

Instruction text is paid for on every call, so doctor checks what is actually
delivered, using the same renderers the hook and the dispatch use. All of it is
advisory (WARN or INFO), and none of it makes a model call. satelle sizes no
dispatched seat against a budget: each agent CLI manages its own context. A
resident document that does not fit the SessionStart harness limit is first
condensed (`satelle substrate condense` prints the rules), with a marker that
returns the original; a read pointer is the last resort.

| Identifier | What it reports |
| --- | --- |
| `context.session.overflow` | the SessionStart context, rendered once per **bound harness** (the harness of each seat's command, plus the harnesses installed for an in-loop seat), still omits documents to read pointers under that harness's own limit after condensing — naming the harness, the limit, the rendered size and each omitted document |
| `context.session.unavailable` | a harness could not be judged: no `[harness.<name>]` table, or a seat command no adapter recognises (never assumed to be any provider) |
| `context.reminder.over_clip` | the per-prompt reminder is longer than the harness's own `[harness.<name>] tool_context_limit_chars` clip — a channel limit of the harness, like the SessionStart cap, reported only when exceeded |
| `context.duplicate.verbatim` | a run of at least `[doctor] duplicate_min_words` words copied word-for-word between two documents, compared against every principle and skill (authored and embedded, resident or on-demand, linked or not). Point at the source with `[[name]]` instead of restating it |
| `context.baseline.override` | a document in `.satelle/principles` or `.satelle/skills` that shadows a baseline principle (`satelle-story-shape`, `satelle-start-small`) by name with a different body, or restates a passage of one (the same word-for-word test). Point at the principle with `[[name]]`; a semantic contradiction is not judged here |
| `context.ref.unresolved` / `.unwired` / `context.index.stale` | a `[[name]]` or backticked `satelle-…` name that resolves to nothing or is retired; a skill that is named but that no route, gate, hook, summary, advisor or task wires; a `documents/index.md` entry whose document is gone |

`satelle init` and `satelle principle validate` judge the resident-set ceiling
against the same bound harnesses and show it as WARN.

## `--context-review` — opt-in semantic review

Paraphrase is not word-for-word, so it cannot be found by comparison.
`--context-review` runs the repo's reviewer binding — **a model call** — once for
each distinct assembled context, read-only and one-shot, under the
`satelle-context-audit` rubric (an authored `.satelle/skills/` copy overrides the
embedded one, including its output contract). The payload is the context plus an
index of every principle and skill, so a restatement of a document the context
never receives is still found (`context.duplicate.semantic`). A reviewer that
cannot run, or whose reply does not parse, is `context.review.unavailable`,
naming the adapter. Without the flag no agent is spawned.

## Two configuration layers, deliberately kept apart

Doctor names both, because confusing them is the mistake it exists to prevent:

- **Repo workflow POLICY** — `.satelle/`: workflows, gates, lifecycle hooks, and
  which logical agent runs each step. What *this repo* enforces. Never
  machine-wide.
- **Machine-wide EXECUTION** — `~/.satelle/agents.toml`: reusable provider
  profiles (command, transport, model, effort, tools). *How* a command runs on
  this machine, shared by every repo that explicitly references one.

For each binding, doctor prints the **effective value and its source**:

```
GRANT [reviewer] role=reviewer interface=command backend=isolated:claude read-only
  source: command = "claude -p …" (profile:claude-opus)
  source: effort = "low" (repo)
  source: model = "opus" (profile:claude-opus)
  source: tools = "Read,Grep,Glob" (embedded)
```

The four sources are `repo` (inline in `.satelle/workflows/agents.toml`),
`profile:<name>` (an explicitly referenced machine-wide profile),
`global-role:<name>` (the catalog's opt-in `[roles]` default), and `embedded`
(satelle's compiled fallback).

## Secrets

Environment **values are never printed**, in any mode, including `--json`.
Doctor lists each binding's env **key names** with whether the value resolved:

```
Environment keys (names only — values are never printed):
  [planner] ANTHROPIC_AUTH_TOKEN (resolved), ANTHROPIC_BASE_URL (resolved)
```

An unresolved `${VAR}` is a finding that names the **key**, never its contents.

## Severity

| | Meaning |
| --- | --- |
| **FAIL** | an error — the repo is not ready, and engagement will refuse for the same reason, with the same identifier |
| **WARN** | advisory — printed, never blocking. Used where the underlying test is a heuristic and a false positive would strand a working repo |
| **INFO** | context you asked for (a live probe that answered) |

Every finding carries a **stable identifier** (`binary.missing`,
`agents.profile.broken`, `hook.alloc.unresolved`, …). The identifier is the
contract: the same defect reports the same id whether you meet it in `doctor`,
in `satelle init`, or in an engagement refusal — so it can be scripted against.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | healthy — warnings are allowed and still printed |
| `1` | one or more error findings, in any checked repository |
| `2` | doctor itself could not run (bad flag, unreadable global config) |

## `--all` — every registered repository

```bash
satelle doctor --all
```

Each registered repo is checked **independently**. An unreadable, uninitialised,
or pathological repo becomes its own one-finding report rather than aborting the
sweep — a diagnostic that stops at the first bad repo is useless to an operator
with several. Output ends with `N healthy, M unhealthy`, and the exit code is the
worst result across all of them.

`satelle service status` prints the same healthy/unhealthy tally for registered
repos, so an unhealthy repo is visible from the service surface rather than
looking identical to a ready one.

## `--live` — opt-in provider probes

**Ordinary `satelle doctor` performs no paid and no network model call at all.**

`--live` additionally starts each isolated binding's CLI:

- a **command**-transport binding is asked for its `--version`;
- an **ACP** binding is taken through a single `initialize` handshake.

Neither opens a session nor sends a prompt, so **neither is a paid model call** —
but both spawn provider processes and may consume provider authentication and
rate budget. That is the side effect to be aware of before wiring `--live` into
anything automated.

Every probe is bounded by `--timeout` (default 20s) and its process group is
killed and reaped on the deadline or on cancellation, so a probe never leaves a
provider process behind. Authentication is diagnosed only where the provider
**says so** — an unexplained failure is reported as a spawn failure, not guessed
at as an auth problem.

Live findings are advisory: a provider that is down does not make a
well-configured repository unhealthy.

## Machine-readable output

```bash
satelle doctor --all --json
```

```json
{
  "repos": [{"repo": "…", "ok": false, "findings": [
    {"id": "binary.missing", "severity": "error", "title": "Missing executable",
     "detail": "…", "remediation": "…", "artifact": "reviewer"}
  ]}],
  "summary": {"healthy": 2, "unhealthy": 1},
  "exit_code": 1
}
```

Ids and severities are stable; env values never appear.

See also: `satelle help global-agents` (the machine-wide profile catalog),
`satelle help workflows` (what a workflow governs), and
`satelle help agent-dispatch` (how a dispatched step runs).
