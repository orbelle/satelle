---
name: satelle-node-sprite
type: skill
description: The scripts a command remote's [connect] names to reach a fly.io sprite through the sprite CLI on this machine - check, start, network, copy_in, exec and stop - so the sprite CLI lives in this repo's substrate and never in the binary.
---

# Node on a sprite

A remote with `provider = "command"` names the commands that reach its node
(`satelle help remotes --session <session>`). These scripts are those commands
for a fly.io sprite, run on this machine with the sprite CLI it is signed in
to. Each takes the node id satelle substitutes for `{node}`; the sprite is
named `satelle-<node id>`, with `_` as `-`.

    [connect]
    check   = ["sh", ".satelle/skills/satelle-node-sprite/scripts/check.sh"]
    start   = ["sh", ".satelle/skills/satelle-node-sprite/scripts/start.sh", "{node}"]
    network = ["sh", ".satelle/skills/satelle-node-sprite/scripts/network.sh", "{node}"]
    copy_in = ["sh", ".satelle/skills/satelle-node-sprite/scripts/copy-in.sh", "{src}", "{node}", "{dst}"]
    exec    = ["sh", ".satelle/skills/satelle-node-sprite/scripts/exec.sh", "{node}", "--"]
    stop    = ["sh", ".satelle/skills/satelle-node-sprite/scripts/stop.sh", "{node}"]

- `scripts/check.sh` refuses the fire before a sprite is created when the
  sprite CLI is missing or `sprite org list` selects no organisation, naming
  `sprite login`.
- `scripts/start.sh` creates the sprite (`sprite create --skip-console`).
- `scripts/network.sh` reads the allow-list on stdin, sets the sprite's
  network policy through the Sprites API (`sprite api
  /v1/sprites/<name>/policy/network`) to allow those domains and deny every
  other, reads the policy back and prints the allowed domains, one per line.
- `scripts/copy-in.sh` copies a file or directory to `{dst}` as a tar stream
  over `sprite exec`, as `cp -R` does.
- `scripts/exec.sh` runs the node command as root (`sudo`) through `sprite
  exec`, which forwards stdin, so secrets reach the node on stdin only, and
  keeps stdout as the command's own.
- `scripts/stop.sh` destroys the sprite, and succeeds for one already gone.
- `scripts/check-test.sh` proves check.sh's refusal with a stub `sprite` on
  PATH: `sh .satelle/skills/satelle-node-sprite/scripts/check-test.sh`.

The sprite's base is Ubuntu, so a remote whose `[setup]` runs the
`satelle-node-setup` pack's script allows the Ubuntu mirrors in `[network]
allow`. `.satelle/remotes/sprite-probe/` is the remote that uses these
scripts; fire it as `.satelle/tasks/docker-task-probe.md` fires the probe task,
with `sprite-probe` as the remote.
