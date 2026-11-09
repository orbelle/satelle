---
name: satelle-drift-substrate-check
type: check
description: satelle doctor's drift check (.satelle/doctor.toml, sto_3cf973ae). Runs scripts/drift-check.sh read-only against the installed satelle: exit 0 when the substrate names nothing the binary lacks, 1 with one line per command, flag, help topic, skill or link it names that the binary does not have, 2 when it cannot check. Changes nothing.
---

# Drift substrate check (satelle doctor)

The deterministic half of the drift review (`.satelle/skills/drift-review.md`
step 1), run by `satelle doctor` from `.satelle/doctor.toml`. It checks the
tracked substrate against the installed satelle and changes nothing; each line
it prints is one doctor issue. A finding's fix is the drift review's: correct
the line it names, or raise its drift story.

```sh
[ -f scripts/drift-check.sh ] || { echo "scripts/drift-check.sh is not in $(pwd)"; exit 2; }
bash scripts/drift-check.sh
```
