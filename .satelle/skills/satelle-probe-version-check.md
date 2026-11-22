---
name: satelle-probe-version-check
type: check
description: The one gate of the probe workflow. Prints the version of the satelle that runs it and exits 0, so a drive of a probe item shows where its check ran. Deterministic, no agent.
---

# Probe version

The probe workflow (`.satelle/workflows/probe.toml`) exists to give a remote
node something it can drive end to end: one move, `backlog -> done`, with no
performer and this one check. The check proves only that a check ran on the
node and which satelle ran it; its output is the gate's notes.

```sh
set -u
command -v satelle >/dev/null 2>&1 || { echo "satelle is not on PATH"; exit 1; }
satelle version </dev/null 2>&1
```
