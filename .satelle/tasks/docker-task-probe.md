# Docker task probe

A task that proves a docker-local node runs a task drive: `satelle remote fire
docker-task-probe <task> --session <session>` runs it once in a container, and
every chain the run wrote comes back here, each verified. It needs no secret,
no network and no agent. The remote is `.satelle/remotes/docker-task-probe/`:
its `[drive] task` names this task, and its overlay `satelle.toml` makes the
node's execution a `probe` item (`satelle workflow show probe --session
<session>`), whose `backlog → done` move runs the check
`satelle-probe-version-check` in the node. How a docker-local remote runs is
`satelle help remotes --session <session>`.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Probe a docker-local task drive") | .id'
```

When it prints nothing, create it once, from the main checkout, and put the
printed id into `.satelle/remotes/docker-task-probe/remote.toml`'s `[drive]
task`:

```sh
session=<session>
satelle task create --title "Probe a docker-local task drive" --input-kind story --input-tag remote-probe --body - --session "$session" <<'BODY'
A docker-local node runs this task once and brings back every chain the run wrote (.satelle/tasks/docker-task-probe.md).

ACTION: run once on a docker-local node with satelle remote fire docker-task-probe <this task> --session <session>; the node creates one execution of this task and drives it on the probe workflow, whose backlog -> done move runs the check satelle-probe-version-check in the node. Needs no secret, no network and no agent.
VERIFICATION: the execution is here at done, every event of it by node:<node> including its gate.run, satelle ledger verify --session <session> says chains intact, and satelle remote status docker-task-probe <this task> --session <session> shows the run concluded with verdict accept.
BODY
```

## Run it

```sh
session=<session>
task=<the task id found above>
satelle remote fire docker-task-probe "$task" --session "$session"
satelle remote status docker-task-probe "$task" --session "$session"
satelle execution list --parent "$task" --session "$session"
```

The node is handed the task's chain and the chains of the stories tagged
`remote-probe`, and no other. Its run is verified by the VERIFICATION above.

## On a plain image

`.satelle/remotes/docker-plain-probe/` fires the same task on
`debian:stable-slim`, an image with no satelle: the fire copies its own
satelle into the node before setup, and the remote's `[setup]` runs the
`satelle-node-setup` pack's script, which installs git and jq, as a sprite is
prepared. Run it as above with `docker-plain-probe` in
place of `docker-task-probe`; it needs network for apt.
