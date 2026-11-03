---
name: satelle-agent-agnostic
type: principle
tags: [type:principle]
applies_to: ["*"]
description: satelle drives any agent CLI over any transport. Provider specifics live only in that provider's adapter or in configuration; every mechanism works for each adapter or records an adapter-named "unavailable"; tests and dogfood cover non-Claude adapters.
---

# satelle is agent-agnostic

1. **Provider specifics in one place.** Wire formats, CLI flags, tool names
   and model names live only in that provider's adapter (`internal/agentcli`)
   or in configuration; never in engine, verb, gate or CLI code, never as a
   Claude default.
2. **Every adapter, or explicit "unavailable".** Mechanism that reads agent
   output (usage, model, events, sessions) handles every adapter in
   `internal/agentcli` on each transport it supports, or records an
   adapter-named "unavailable". A silent zero or a Claude default is a defect.
3. **Neutral detection.** Nothing unrecognised is assumed to be Claude.
4. **Tests and dogfood per adapter.** Captured real output per provider; a
   transport, usage, model or session change is dogfooded on a non-Claude
   binding.
