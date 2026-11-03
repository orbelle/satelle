---
name: satelle-enable-then-operate
type: principle
tags: [type:principle]
applies_to: ["*"]
description: satelle has two phases — enable (install + `satelle init`, idempotent) and operate. A repo's process lives in its `.satelle/` root; its runtime state (database, logs, backups, story cache) lives on the home-keyed runtime plane. Enabled is a precondition operating commands assume, never a side effect they cause.
---

# Enable, then operate

**Enable** once: install the binary and run `satelle init`, which scaffolds the
repo's `.satelle/` root (config and authored substrate) and is idempotent. Init
is the only operation that creates a repo's state.

**Operate** from what enable laid down. The process (config, workflows, skills,
principles, tasks, constitution) is read from `.satelle/`; runtime state (the
database, logs, backups and story cache) lives on the runtime plane
`~/.satelle/<repo-key>/`, never in the tree. A command walks up to the
`.satelle/` it operates from, surfaces a repo that is not enabled, and never
scaffolds one as a side effect. The binary is mechanism; it is never a second
store of process.

See [[satelle-mechanism-vs-substrate]].
