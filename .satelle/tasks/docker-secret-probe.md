# Docker secret probe

A task that proves a docker-local node is given its declared secrets:
`SATELLE_PROBE_SECRET=<value> satelle remote fire docker-secret-probe <task>
--session <session>` reads the value from the firing shell's environment and
hands it to the node process over its stdin, never in argv, the container's
configuration, the ledger or on disk. The node's `[setup]` and its step both
read it, and the step records only sha256s. It needs no network and no agent.
The remote is `.satelle/remotes/docker-secret-probe/`: its `[secrets] names`
declares `SATELLE_PROBE_SECRET`, its `[drive] task` names this task, and its
overlay `satelle.toml` makes the node's execution a `secret-probe` item
(`satelle workflow show secret-probe --session <session>`), whose
`backlog → done` move runs the check `satelle-probe-secret-check` in the node.
How a docker-local remote is given secrets is `satelle help remotes --session
<session>`.

Where the value comes from is yours: the fire reads the name from its own
environment, so any wrapper that sets it (a keyring, a secrets manager, a
vault CLI) works. A name not set there refuses the fire before any container
starts.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Probe a docker-local secret") | .id'
```

When it prints nothing, create it once, from the main checkout, and put the
printed id into `.satelle/remotes/docker-secret-probe/remote.toml`'s `[drive]
task`:

```sh
session=<session>
satelle task create --title "Probe a docker-local secret" --input-kind story --input-tag secret-probe --body - --session "$session" <<'BODY'
A docker-local node is given its declared secret from the firing shell and proves it read it without recording it (.satelle/tasks/docker-secret-probe.md).

ACTION: run once on a docker-local node with SATELLE_PROBE_SECRET=<value> satelle remote fire docker-secret-probe <this task> --session <session>; the node creates one execution of this task and drives it on the secret-probe workflow, whose backlog -> done move runs the check satelle-probe-secret-check in the node. Needs no network and no agent.
VERIFICATION: the execution is here at done, its gate.run notes of secret-probe hold "step sha256:" and "setup sha256:" both equal to the sha256 of the value fired with, "argv clean:", and never the value; satelle remote status docker-secret-probe <this task> --session <session> shows the run concluded with verdict accept.
BODY
```

## Run it

```sh
session=<session>
task=<the task id found above>
SATELLE_PROBE_SECRET=<value> satelle remote fire docker-secret-probe "$task" --session "$session"
satelle remote status docker-secret-probe "$task" --session "$session"
satelle execution list --parent "$task" --session "$session"
printf %s '<value>' | sha256sum
```

Compare the last line with `step sha256:` in the execution's gate.run notes
(`satelle ledger list <execution> --session "$session"`).
