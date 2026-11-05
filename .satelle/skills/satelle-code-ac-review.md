---
name: satelle-code-ac-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: The one code review on in_progress → integration (unit tests run beside it in satelle-build-unit-check). Isolated read-only reviewer validates the engaged slice against the story's BDD and numbered ACs, the executor's per-AC test mapping, proof honesty (a named test that would pass on the engagement baseline cannot cover a fix AC) and scope (the slice stays inside this story); rejects a slice that adds user configuration to the binary. Verdict only; never runs the suite. A re-presented edge follows [[satelle-reviewer-verdict]].
---

# Code vs acceptance-criteria review (in_progress → integration)

Validate the engaged slice against the numbered ACs and the executor's per-AC
mapping. Read-only; do not run the suite or design an alternative.

## Enumeration — the slice

Prefer `payload.diff` when present. Open `diff.patch_path`; the file is JSON with `files`, `stat`, and `patch`, the same shape as `satelle story diff`. That is the engaged slice. Judge those paths against the ACs, not the whole tree. When `diff.no_baseline` is true or `diff`
is omitted, fall back to Read/Grep of the tree. Do **not** shell
`satelle story diff`; default `[reviewer]` has no shell.

Open each payload `docs` entry's `path` (`ac-evidence`, `plan`) for the evidence.

## Judge (presented slice only)

1. **ACs vs code.** Walk the numbered ACs. Confirm visible code in the slice
   plausibly satisfies each (files exist, behaviour implemented, not
   stubbed/TODO). A prohibition- or invariant-shaped criterion ("never",
   "always", "no X is…") is judged by falsification: enumerate every path its
   words reach that could produce the forbidden case, check each, accept only
   when all hold; reject naming the violating path, or for insufficient evidence
   when the surface cannot be enumerated. A compliant sample is not proof.

2. **Executor per-AC mapping (judgment, not presence).** Presence of the
   `ac-evidence` attachment is `satelle-ac-evidence-check`'s; do not reject for
   it. Confirm each claimed realised test exists in the tree and plausibly
   covers that AC, and that divergences from the plan are stated, not silent.
   **Reject** when the report names a test that does not exist, the mapping
   clearly does not hold, or a `plan` attachment exists and the tree clearly
   ignores its named slice with no plan-defect or divergence note.
   Docs/comment/rename/substrate-only slices may note n/a.

3. **Proof honesty (inverted lock).** For a **fix or behavioural** AC, read
   the named test. **Reject** when that test would pass on the engagement
   baseline — it locks in the pre-fix shape and does not prove the AC.
   Presence of the test symbol is not enough; the assertion must fail if the
   bug were still present. A test whose expected value is computed by the
   code under test, or that asserts only on data it built itself, is no proof
   either. Do not run `go test`. Judge by reading the test.

4. **DB-state ACs.** If an AC asserts store/DB state, use
   `.satelle/logs/operations.log` (or equivalent evidence) — do not reject
   solely because state is invisible in the tree.

5. **Agent-agnostic** ([[satelle-agent-agnostic]], regardless of the ACs). When
   the slice adds or changes parsing of agent output, usage/model accounting,
   tool grants, live sessions or transport behaviour, confirm each adapter
   under `internal/agentcli` either works or records an explicit adapter-named
   "unavailable" (never a silent zero, empty field or Claude default). **Reject**
   a single-provider mechanism with no stated handling for the other adapters
   (name them), and provider model names, flags or tool names hardcoded outside
   an adapter or configuration.

6. **User configuration in the binary** ([[satelle-mechanism-vs-substrate]],
   "User configuration is never compiled in", regardless of the ACs or who
   raised the story). **Reject** a slice whose diff adds anything that section
   lists as user configuration to the binary, a closed provider list included;
   cite the file:line and name the principle's alternative. **Passes:** what the
   principle allows, plus a vendor named in test fixtures and the harness
   adapters under `internal/agentcli`.
7. **Dispatch pointers** ([[satelle-dispatch-pointers]]). When the slice
   assembles what an agent receives (a prompt, a stdin payload, a dispatch
   request), the receiver gets its instruction and pointers (ids, paths,
   retrieval commands). **Reject** when a dispatch embeds readable content:
   item bodies, documents or history the receiving seat can fetch itself,
   copied into its payload, whatever its size; bounding or batching that
   content is the same defect.

8. **Scope.** This is the only code review on the edge, so it also judges
   boundedness. Every changed path must be explained by this story's ACs, its
   body, or mechanical collateral (its own tests and docs, a lockfile the change
   regenerates). Reject work that satisfies another story, naming each
   out-of-scope path and the story that should own it, and say to revert or
   split it into that story. When untracked debris (scratch files, evidence
   dumps) is part of the reason, end the notes with a last line exactly
   `Fix: satelle story tidy <story.id> <path> ...` naming every such path. With
   no engagement baseline, scope is not judged.

Fair gate: judge ACs **as written**; do not add requirements the story never
stated.

## Verdict

```json
{"verdict": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>"}
```
