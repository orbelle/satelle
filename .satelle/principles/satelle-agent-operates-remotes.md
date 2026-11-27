---
name: satelle-agent-operates-remotes
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Agents operate remotes, never the user by hand. The machine that fires a run opens the connection to the node and operates it; no server is required, and satelle.dev is one place a remote may be operated from. A node is isolated to its one task. A remote reads exactly the secrets its definition declares, locally from the control repository's untracked .satelle/.env, when the run starts, and copies them nowhere; an agent never handles a value. Connection, host and secret source are the control repository's configuration.
---

# Agents operate remotes

An agent registers, configures, checks, starts, watches and retires a remote.
It never hands a remote step to the user; a step its tools cannot do is a
satelle or satelle.dev story.

## The firing machine operates the node

A run is machine-initiated: the machine that fires it opens the connection to
the node, gives it its work and reads its result back. No node calls back to a
server, so a remote needs no server. satelle.dev is one place a remote may be
operated from (its MCP tools, for claude.ai or grok.com where there is no
repo), not the only one; in a repo the agent fires the remote with the satelle
CLI (`satelle help remotes --session <session>`).

A node is isolated to its task: it gets one task or item, the secrets its
definition declares and the network its definition allows, and nothing else of
the machine that fired it. For an item, that is one repository's work, the
item's target repository ([[satelle-control-plane-architecture]]), with the
substrate it reads through `SATELLE_SUBSTRATE_DIR`; a node never gets another
repository its control repository registers. The firing machine only reads
the target (a bundle written outside it) and writes none of its refs; git the
node runs is in its own scratch workspace.

satelle is the kernel; everything else is configuration
([[satelle-mechanism-vs-substrate]]). The binary carries one node contract. How
the node is reached (a local docker, a docker host over SSH or Tailscale SSH),
which host and image it runs, and where each secret's value comes from are the
remote's definition, in the control repository (in N=1, the repo itself).

Which parts of this model are built is the state of the remotes epic's stories
(`satelle story list --parent sto_426138e8 --session <session>`), not this page.

## Secrets: declared, never granted

- A secret is a name, a value and a scope (personal or team), and nothing else:
  no readers list, no per-remote or per-workflow grant.
- A remote's definition declares each secret it needs by name. Accepting that
  definition version is the authorisation. Scopes never cross, no remote reads
  another user's personal secrets, and a wake key is never readable as a
  secret.
- A different token is a different key name. There are no aliases and no
  per-remote override of one key.
- No agent proposes, makes or checks a grant (satelle-server sty_f7c38cc5
  removed them).

A value is read from its source when the run starts and is copied nowhere:
not into the definition, the image, the node's configuration or disk, the
ledger or a commit. Locally the source is the control repository's own
secrets file, `.satelle/.env` (in N=1, the repo's): untracked and ignored by
git, written by the user, and the one place a value rests on the firing
machine. A target repository holds no secrets file; a value in the control
repository's may reach a run against any repository it governs, since a
substrate is not a security boundary ([[satelle-control-plane-architecture]]
§1). Later a supported secrets service
(Infisical, satelle.dev) is a source in its place. The value reaches the node
only over the connection the fire opens, and the node holds it in its process
environment for its one run (operator, 2026-11-24, [[satelle-operator-rulings]]).

An agent never reads, prints, types or attaches a secret value; it names the
key in the definition. Putting a value in its source is the user's only part.
A refused remote call is brought to the user as an approval of that call, not
as manual work.
