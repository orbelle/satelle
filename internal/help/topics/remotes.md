# Remotes and placement

A remote is a definition, `.satelle/remotes/<id>/remote.toml` (schema v1:
provider, machine, repo, drive, secrets, setup, network, lifecycle,
satelle_version), that nodes are started from. A node is one image plus a
work item or task, credentials and network rules. It reads its definition,
run and secrets, fetches the declared repos read-only, runs setup, then drives
the item on its workflow (or runs the task once and drives the execution),
brings the ledger back and concludes with a verdict.

    satelle remote fire <remote-id> <item>     # fire a run of the remote for the item
    satelle remote status <remote-id> <item>   # the run, and its verdict and notes

Providers: `docker-local` runs the definition as a local container, here;
`fly-sprite` nodes are started by satelle-server.

## docker-local: runs locally

A docker-local remote runs locally, not through satelle-server (which serves
no remotes yet). It needs docker, and no sign-in or bound project. The
smallest one is `docker-probe`:

    satelle remote fire docker-probe <item> --steps 1
    satelle remote status docker-probe <item> [--wait 5m]

`remote fire` builds the node image from the repo's Dockerfile as
`satelle-node:local` (unless `[machine] image` names another), starts the
container `satelle-node-<node>` with no network, runs setup in it, seats the
item at `remote:<node>`, and hands the node its definition, the repo's
tracked `.satelle` files, its run and the item's chain. The node drives the
item (`--steps N` bounds it to N steps; 0, the default, goes as far as the
item goes) and concludes; the fire verifies the chain the node returns,
imports it, releases the seat (also on failure) and records the run, which
`remote status` shows. The item's chain here carries the node's events, by
`node:<node>`.

The fire prints `docker exec -it satelle-node-<node> sh` before the node
runs: inspect the container while it runs. `[lifecycle] scope = "job"` keeps
the container after the run, as does a failed run; `docker rm -f
satelle-node-<node>` removes it. With `scope = "run"` an accepted run's
container is removed.

A local node is given no secrets and fetches nothing: `[secrets]`, `[[repo]]`
and a task `[drive]` are refused locally. `max_nodes`, `idle_destroy` and
`keep_warm` are not applied: each fire runs one node.

## Remotes through satelle-server

For any other remote, `remote fire` pushes the item's chain to the bound
project first and asks satelle-server to fire the run; `remote status` pulls
the item's chain back once the run concluded.

## Placement

A binding's `placement` in agents.toml says where a state's performer runs:

- `local` (default): dispatched here.
- `remote:<id>`: `satelle story perform` starts a node of that remote, seats it
  at `remote:<node>` and sends the step over the sealed channel.
- `cloud`: a cloud session performs the step, seated at `cloud:<url>`. When its
  work comes back on a branch, `satelle story collect <id> --branch <b>`
  records the branch's commit and its merge base with origin/main (or
  `--base <ref>`), never HEAD; the item's gates then judge that diff, and a
  branch that adds nothing is refused. collect never changes the repository.
