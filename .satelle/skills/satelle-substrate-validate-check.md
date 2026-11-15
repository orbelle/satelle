---
name: satelle-substrate-validate-check
type: check
description: Runs `satelle validate --session <session>` on the presented tree and refuses the move unless it reports the configuration valid with no substrate finding (a command, flag, help topic or link the binary lacks). Deterministic, no agent.
---

# Substrate validate

A substrate change edits the configuration satelle runs and the skills and
principles agents read. `satelle validate --session <session>` refuses a
configuration that would stop the kernel opening and a substrate finding: a
satelle command, flag or help topic a skill names in code, or a `[[link]]`,
that does not exist. Its exit status is this gate's verdict and its output the
notes. Volatile lines it lists do not fail it; `satelle-volatile-literal-check`
judges those.

```sh
set -u
command -v satelle >/dev/null 2>&1 || { echo "satelle is not on PATH, so the configuration cannot be validated"; exit 1; }
satelle validate </dev/null 2>&1
```
