# Remotes and placement

A remote is a definition, `.satelle/remotes/<id>/remote.toml` (schema v1:
provider, machine, repo, drive, secrets, setup, network, lifecycle,
satelle_version), that satelle-server starts nodes from. A node is one image
plus a work item or task, credentials and network rules. It joins with its
node token, reads its definition, run and secrets, fetches the declared repos
read-only, runs setup, then drives the item on its workflow (or runs the task
once and drives the execution), pushes the ledger back and concludes with a
verdict.

    satelle remote fire <remote-id> <item>     # push the item's chain, fire a run
    satelle remote status <remote-id> <item>   # the run; pulls the chain once concluded

Providers: `docker-local` runs the definition as a local container
(inspectable with `docker exec`); `fly-sprite` nodes are started by
satelle-server.

A binding's `placement` in agents.toml says where a state's performer runs:

- `local` (default): dispatched here.
- `remote:<id>`: `satelle story perform` starts a node of that remote, seats it
  at `remote:<node>` and sends the step over the sealed channel.
- `cloud`: a cloud session performs the step, seated at `cloud:<url>`. When its
  work comes back on a branch, `satelle story collect <id> --branch <b>`
  records the branch's commit and its merge base with origin/main (or
  `--base <ref>`), never HEAD; the item's gates then judge that diff, and a
  branch that adds nothing is refused. collect never changes the repository.
