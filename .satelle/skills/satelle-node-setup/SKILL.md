---
name: satelle-node-setup
type: skill
description: The scripts that prepare a remote node's tools on a plain Linux base, the same way for a container and a sprite. A remote's [setup] runs them; the fire has already copied satelle into the node.
---

# Node setup

A node that starts from a plain Linux image, or a sprite, has no tools of this
repo's. The fire copies satelle in before `[setup]` runs
(`satelle help remotes --session <session>`); the scripts here install the
rest. A remote's definition runs them from `[setup]`, by their path in the
node's tree:

    [setup]
    on_create = ["sh .satelle/skills/satelle-node-setup/scripts/base-tools.sh"]

- `scripts/base-tools.sh` installs git, jq and ca-certificates on a Debian
  base with apt-get. It installs only what is missing, so a second run changes
  nothing, and prints each package it installs or finds present.

The remote's `[network] allow` must reach the package mirror the script uses
(for Debian, `deb.debian.org`). A remote that needs more tools adds its own
script beside this one and runs both from `[setup]`.
