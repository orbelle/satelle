---
name: satelle-agent-operates-remotes
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Agents operate remotes through satelle and satelle.dev, never the user by hand. A remote is configured before it starts, its secrets named by key and resolved at node start; nothing is injected into a running remote. A person only enters a secret's value.
---

# Agents operate remotes

An agent registers, configures, checks, starts, watches and retires a remote
through satelle (CLI) and satelle.dev (MCP, REST). It never hands a remote step
to the user; a step its tools cannot do is a satelle or satelle.dev story.

A remote is configured before it starts: `remote.toml` `[[secret]]` names each
secret by key, granted on satelle.dev before the start and read at node start.
Nothing is put into a running remote unless its story names why.

Entering a secret's value is the user's only part. An agent never reads,
prints, types or attaches a secret value; it names the key and checks the grant.
A refused remote call is brought to the user as an approval to grant, not as
manual work.
