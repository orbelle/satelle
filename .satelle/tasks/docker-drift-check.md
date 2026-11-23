# Drift check on a docker-local node

A task that runs this repo's drift check, `scripts/drift-check.sh`, on a
docker-local node: `satelle remote fire docker-drift-check <task> --session
<session>` gives a container the firing tree's HEAD commit, checked out with
no network and no secret, and every chain the run wrote comes back here, each
verified. It needs no agent. The remote is `.satelle/remotes/docker-drift-check/`:
its `[drive] task` names this task and its `[drive] checkout = true` stages the
HEAD commit; its overlay `satelle.toml` makes the node's execution a
`drift-node` item (`satelle workflow show drift-node --session <session>`),
whose `backlog → done` move runs `satelle-change-judged-check` then
`satelle-drift-node-check` in the node. The satelle that runs is the node
image's, built from the firing tree at every fire. How a docker-local remote
runs is `satelle help remotes --session <session>`.

The drift review's step 1 (`.satelle/skills/drift-review.md`) fires it after
every release; the drift-constitution judge stays local.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Run the drift check on a docker-local node") | .id'
```

When it prints nothing, create it once, from the main checkout, and put the
printed id into `.satelle/remotes/docker-drift-check/remote.toml`'s `[drive]
task`:

```sh
session=<session>
satelle task create --title "Run the drift check on a docker-local node" --input-kind story --input-tag docker-drift-check --body - --session "$session" <<'BODY'
A docker-local node runs scripts/drift-check.sh against the firing tree's HEAD commit and the node image's satelle, and its result comes back here (.satelle/tasks/docker-drift-check.md).

ACTION: run once on a docker-local node with satelle remote fire docker-drift-check <this task> --session <session>; the node is given the firing tree's HEAD commit, creates one execution of this task and drives it on the drift-node workflow, whose backlog -> done move runs the checks satelle-change-judged-check then satelle-drift-node-check in the node. Needs no secret, no network and no agent.
VERIFICATION: the execution is here, every event of it by node:<node>, its drift-check gate.run notes start "satelle: " and end "drift-check exit <N>", satelle ledger verify --session <session> says chains intact, and satelle remote status docker-drift-check <this task> --session <session> shows the run concluded with every drift-check line under "gate drift-check <result>:".
BODY
```

No item carries the tag `docker-drift-check`, so the node is handed the
task's chain only.

## Run it and read it

```sh
session=<session>
task=<the task id found above>
satelle remote fire docker-drift-check "$task" --session "$session"
satelle remote status docker-drift-check "$task" --session "$session"
satelle execution list --parent "$task" --session "$session"
satelle ledger list <the execution the fire names> --kind gate.run --session "$session"
```

The run's notes carry each gate's full notes under `gate <gate> <result>:`.
The `drift-check` gate's notes start `satelle: <version>`, then every line
drift-check.sh printed, and end `drift-check exit <N>`: 0 is clean, 1 lists
drift (the run concludes rejecting), 2 means the node could not check. Nothing
retries. A concluded run's container is removed, accepted or not.
