---
name: satelle-agent-agnostic
type: principle
tags: [type:principle]
applies_to: ["*"]
description: satelle drives any agent CLI over any transport. Provider specifics live only in that provider's adapter (dispatch adapters in internal/agent, harness hook adapters in internal/hook) or in configuration; every mechanism works for each adapter or records an adapter-named "unavailable"; tests and dogfood cover non-Claude adapters.
---

# satelle is agent-agnostic

1. **Provider specifics in one place.** Wire formats, CLI flags, tool names
   and model names live only in that provider's adapter or in configuration;
   never in engine, verb, gate, kernel or CLI code, never as a Claude default.
   satelle has two kinds of adapter, both provided and maintained in satelle:
   - **dispatch adapters** (`internal/agent`): how satelle runs an agent CLI
     as a performer or judge (argv, flags, grants, usage and model parsing);
   - **harness hook adapters** (`internal/hook`): how satelle's hooks sit
     inside an interactive harness session (claude, pi, grok, cursor-agent):
     each harness's hook file location and schema, its payload parsing, its
     answer envelopes, and install and remove. The edit gate itself
     (`hook.Gate`) stays harness-neutral.
2. **Every adapter, or explicit "unavailable".** Mechanism that reads agent
   output (usage, model, events, sessions) handles every adapter in
   `internal/agent` on each transport it supports, or records an
   adapter-named "unavailable". A silent zero or a Claude default is a defect.
3. **Neutral detection.** Nothing unrecognised is assumed to be Claude.
4. **Tests and dogfood per adapter.** Captured real output per provider; a
   transport, usage, model or session change is dogfooded on a non-Claude
   binding.
