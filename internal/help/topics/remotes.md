# Remotes and placement

A remote is a definition, `.satelle/remotes/<id>/remote.toml` (schema v1:
provider, machine, repo, drive, secrets, setup, network, lifecycle, connect,
satelle_version), that nodes are started from. A node is one image plus a
work item or task, credentials and network rules. It reads its definition,
run and secrets, fetches the declared repos read-only, runs setup, then drives
the item on its workflow (or runs the task once and drives the execution),
brings the ledger back and concludes with a verdict.

    satelle remote fire <remote-id> <item> --session <session>     # fire a run of the remote for the item
    satelle remote status <remote-id> <item> --session <session>   # the run, and its verdict and notes

Providers: `docker-local` runs the definition as a local container, here;
`command` reaches its node, from here, through the commands its definition
names; `fly-sprite` nodes are started by satelle-server. `satelle validate
--session <session>` checks every remote definition against the v1 schema.

## docker-local: runs locally

A docker-local remote runs locally, not through satelle-server (which serves
no remotes yet). It needs docker, and no sign-in or bound project. The
smallest one is `docker-probe`:

    satelle remote fire docker-probe <item> --steps 1 --session <session>
    satelle remote status docker-probe <item> [--wait 5m] --session <session>

`remote fire` builds the node image from the repo's Dockerfile as
`satelle-node:local`, unless `[machine] image` names another, starts the
container `satelle-node-<node>` with no network, seats the item at
`remote:<node>`, and hands the node its definition, the repo's tracked
`.satelle` files, its run and the item's chain. The node is given its secrets,
then runs setup, then drives the
item (`--steps N` bounds it to N steps; 0, the default, goes as far as the
item goes) and concludes; the fire verifies the chain the node returns,
imports it, releases the seat (also on failure) and records the run, which
`remote status` shows. The item's chain here carries the node's events, by
`node:<node>`.

A docker-local node may start from a plain image that [setup] prepares, as a
sprite does. When `[machine] image` names one (debian, ubuntu), the image
needs no satelle: satelle is copied in at the fire. The fire copies its own
satelle (linux, the node's architecture) to `/usr/local/bin/satelle` before
setup and checks it runs there as its own version; `[setup]` runs the repo's
scripts that install the rest. `docker-plain-probe` does this on
`debian:stable-slim`, its `[setup]` running a script that installs git and jq. An image the fire
cannot give satelle to (another architecture, a firing machine that is not
linux, no writable `/usr/local/bin`) is refused before setup, naming the image
and the reason: the container is removed and nothing is imported.

The fire prints `docker exec -it satelle-node-<node> sh` before the node
runs: inspect the container while it runs. `[lifecycle] scope = "job"` keeps
the container after the run, as does a failed run; `docker rm -f
satelle-node-<node>` removes it. With `scope = "run"` a concluded run's
container is removed, accepted or rejected. A run that stopped (the node
failed and wrote no outcome) is left to inspect, as is every run with
`scope = "job"`.

A docker-local task remote may set `[drive] checkout = true`: the fire bundles
the firing tree's HEAD commit and checks it out, detached, at the node's work
tree before setup, with no network, in place of the tracked `.satelle` files.
`docker-drift-check` uses it to run `scripts/drift-check.sh` on the commit.
The default node image carries git, jq and bash. A concluded run's notes carry
each gate the node ran, as `gate <gate> <result>:` followed by the gate's full
notes.

A docker-local node runs a task drive: `satelle remote fire <remote-id> <task>
--session <session>`, for the task its `[drive] task` names, hands the node
the task's chain and the chains of the items its input selection names, and no
other. The node creates one execution of the task and drives it on its
workflow, which the remote's overlay `satelle.toml` chooses
(`docker-task-probe` uses `probe`). Every chain the run wrote comes back, the
execution's and those of the items it changed, and each is verified before any
is imported: a chain that does not verify imports nothing and concludes the
run `reject`, naming it. A chain that also gained an event here while the
node ran is refused at its fork, never merged: the run's record names it with
the head here and the node's head, every other chain is imported, and the run
concludes `reject`. The node's events past the shared history are scanned for
declared secrets like any other. No seat is taken for a task. `remote status
<remote-id> <task>` shows the run.

A docker-local node is given the secrets its `[secrets] names` declare, each
read from the environment of the shell that fires and handed to the node
process over its stdin, never in argv, the container's configuration, the
ledger or on disk; a name not set there refuses the fire before any container
starts, the node replaces each value in what it reports, and a node record
that carries a declared value is not imported. `[network] allow` is enforced on docker through satelle's egress proxy: the node's only route out is the proxy, which reaches only the hosts the list names, and a fire or gate check that cannot enforce the list refuses before the node starts.
`[secrets.override]` and `[[repo]]` are refused locally. Where a value comes
from is yours: wrap the fire in whatever sets the name (a keyring, a secrets
manager); `docker-secret-probe` proves the path. `max_nodes`, `idle_destroy`
and `keep_warm` are not applied: each fire runs one node.

## command: reached by the commands its definition names

A remote with `provider = "command"` runs from here like docker-local, but
satelle compiles in no way of reaching its node: the definition's `[connect]`
table names it, as argv lists.

    [connect]
    check   = ["my-node-cli", "ready"]                         # optional
    start   = ["my-node-cli", "create", "{node}"]
    copy_in = ["my-node-cli", "copy", "{src}", "{node}:{dst}"]
    exec    = ["my-node-cli", "exec", "{node}", "--"]
    stop    = ["my-node-cli", "destroy", "{node}"]

satelle substitutes `{node}` (the node id, `nod_<hex>`) in every list, and
`{src}` (a path on this machine) and `{dst}` (a path on the node) in copy_in;
any other `{word}` is refused. Each command is run as its argv, never through
a shell, with the firing shell's environment. start, copy_in, exec and stop
are required; a definition that lacks one is reported by `satelle validate
--session <session>` and its fire is refused before any command runs.

`remote fire` runs check first, when the definition names one: a non-zero
exit refuses the fire before start, naming the check command and the tail of
its output. Then start, then copy_in of the firing satelle (linux, the node's
architecture) to `/usr/local/bin/satelle`, then the node contract over exec:
`uname -m`, `satelle --version`, `mkdir -p /node`, copy_in of the run to
`/node/in`, then the node's own run over `/node`, given its secrets on stdin
(which runs `[setup]` and drives the item or task), and the record copied back as `tar -C <dir> -cf -
<name>` through exec, read from exec's stdout. So the node needs `tar`,
`uname` and `mkdir`, exec must keep the node command's stdout clean, and
copy_in copies as `cp -R` does to a `{dst}` that does not exist yet. Every
chain that comes back is verified before it is imported, as on docker-local.

The declared `[secrets] names` are taken from the firing shell and removed
from the environment every command runs with: each value reaches the node
only on exec's stdin. With `[lifecycle] scope = "run"` stop runs when the run
concludes, accepted or rejected, and when it fails; with `scope = "job"` the
node is left and the fire prints the stop command. A run left `running` here
by an interrupted fire is recovered at the next fire by running stop for its
node, so stop should succeed for a node that is already gone.
`[network] allow` is refused on a command remote until it is enforced there.

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

The image must have git (or setup installs it). Unlike a fire, `[secrets]`
and `[[repo]]` are refused for a gate: the check rejects naming them.

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
