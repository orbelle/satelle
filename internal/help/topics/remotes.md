# Remotes and placement

A remote is a definition, `.satelle/remotes/<id>/remote.toml` (schema v1:
provider, machine, repo, drive, secrets, setup, network, lifecycle,
satelle_version), that nodes are started from. A node is one image plus a
work item or task, credentials and network rules. It reads its definition,
run and secrets, fetches the declared repos read-only, runs setup, then drives
the item on its workflow (or runs the task once and drives the execution),
brings the ledger back and concludes with a verdict.

    satelle remote fire <remote-id> <item> --session <session>     # fire a run of the remote for the item
    satelle remote status <remote-id> <item> --session <session>   # the run, and its verdict and notes

Providers: `docker-local` runs the definition as a local container, here;
`fly-sprite` nodes are started by satelle-server.

## docker-local: runs locally

A docker-local remote runs locally, not through satelle-server (which serves
no remotes yet). It needs docker, and no sign-in or bound project. The
smallest one is `docker-probe`:

    satelle remote fire docker-probe <item> --steps 1 --session <session>
    satelle remote status docker-probe <item> [--wait 5m] --session <session>

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
  work comes back on a branch, `satelle story collect <id> --branch <b> --session <session>`
  records the branch's commit and its merge base with origin/main (or
  `--base <ref>`), never HEAD; the item's gates then judge that diff, and a
  branch that adds nothing is refused. collect never changes the repository.

A check gate's `placement` in a workflow (see `satelle help workflows --session <session>`) places
the gate's check skill the same way: `remote:<id>` runs it on a node of that
docker-local remote, as below.

## A gate placed on a remote

A check gate with `placement = "remote:<id>"` runs its check skill on a new
node of the docker-local remote `<id>`, started here for that one check. The
transition (`satelle story set <id> --status <to> --session <session>`) starts the container
`satelle-node-<node>`, runs `[setup]` in it, and hands it a git bundle of the
item's pushed head (no secrets, and no network for the code), the check
script, its stdin and a small wrapper. The node fetches the head from the
bundle, checks it out, runs the check in it and returns its exit code and
output, which are recorded here as that gate's gate.run, with `location:
remote:<node>`. No satelle runs in the node and nothing is seated.

Services the check needs come up in the definition's `[setup]`, before the
fetch and the check. A Postgres beside the suite, on an alpine image:

    [setup]
    on_create = [
      "apk add --no-cache git make bash postgresql",
      "mkdir -p /run/postgresql && chown postgres /run/postgresql",
      "su postgres -c 'initdb -D /var/lib/postgresql/data'",
    ]
    post_start = ["su postgres -c 'pg_ctl -D /var/lib/postgresql/data -w start'"]

The image must have git (or setup installs it). As for a fire, `[secrets]`
and `[[repo]]` are refused: the check rejects naming them.

Lifecycle: with `scope = "run"` an accepted check's container is removed; a
reject or a failure leaves it to inspect, and the gate's notes say `docker rm
-f satelle-node-<node>`; a check cut at the gate's `timeout` is removed. With
`scope = "job"` the container is always left.

The same definition runs as a fly-sprite remote with only its `provider`
line changed: a gate may be placed on a docker-local or a fly-sprite remote.
A fly-sprite check runs on a node satelle-server starts
(RemoteService.StartNode), reached over the sealed channel: the starter
hands it the same bundle, script, stdin and wrapper and runs the same check.
It needs a bound project (`satelle project bind`), and the node must run a
satelle carrying the gate jobs (its `satelle_version`). The definition
satelle-server serves the node must be this repo's file: the node reports
the sha256 of its `remote.toml`, and a different one is refused, naming
both, before the check runs. The node's lifecycle is the server's: the
starter ends it after every check.

Every check is its own node and container, with nothing shared, so several
items' gates run at once on separate nodes. A long suite still holds the
`story set` that runs it: start that command in the background (the harness's
background command, its timeout above the gate's) and the session's other
commands run meanwhile.
