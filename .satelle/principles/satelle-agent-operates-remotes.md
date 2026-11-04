---
name: satelle-agent-operates-remotes
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Agents operate remotes through satelle (the CLI, in a repo) and satelle.dev (the MCP, for claude.ai or grok.com), never the user by hand. A remote reads exactly the secrets its accepted definition declares; there are no grants. A person only enters a secret's value, in the satelle.dev UI.
---

# Agents operate remotes

An agent registers, configures, checks, starts, watches and retires a remote.
In a repo the agent uses the satelle CLI; satelle.dev's MCP tools are the same
surface for orchestrating from claude.ai or grok.com, where there is no repo. It
never hands a remote step to the user; a step its tools cannot do is a satelle
or satelle.dev story.

## Secrets: declared, never granted

- A secret is a name, a value and a scope (personal or team), and nothing else:
  no readers list, no per-remote or per-workflow grant.
- A remote's definition declares each secret it needs by name: `satelle:<KEY>`
  in `[secrets]`, or `${satelle.<KEY>}` / `${satelle.<userid>.<KEY>}` in `[env]`.
  Accepting that definition version is the authorisation. A personal or
  owner_only remote reads its registering user's personal secrets; a team remote
  reads its workspace's team secrets. Scopes never cross, no remote reads
  another user's personal secrets, and a wake key is never readable as a secret.
- A different token is a different key name. There are no aliases and no
  per-remote override of one key.
- No agent proposes, makes or checks a grant (satelle-server sty_f7c38cc5
  removed them).

A remote is configured before it starts, and its declared secrets are resolved
at node start. Nothing is put into a running remote unless its story names why.

Entering a secret's value, in the satelle.dev UI, is the user's only part. An
agent never reads, prints, types or attaches a secret value; it names the key in
the definition. A refused remote call is brought to the user as an approval of
that call, not as manual work.
