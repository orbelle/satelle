# Block breaker

A task a driving session runs against one story held at `blocked` after its
round cap, before it raises any new story (`.satelle/session.md`). A cold
agent of another family than the held gate's dissenting judges reads the held
story's record and returns a `block-resolution`
(`.satelle/skills/block-breaker.md`); its runs follow
`.satelle/workflows/block-breaker.toml`.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Break a held story'"'"'s block") | .id'
```

When it prints nothing, create it once, from the main checkout:

```sh
session=<session>
satelle task create --title "Break a held story's block" --input-kind story --input-status blocked --body - --session "$session" <<'EOF'
A story held at blocked after its round cap is read cold before any new story is raised (.satelle/session.md).

ACTION: follow .satelle/tasks/block-breaker.md, section "Run it", for the one held story the session names: attach its record to the run (held-item, held-plan, held-findings), tag the run block-breaker and reselect it into the block-breaker workflow, move it to the breaking state whose binding is of another family than every dissenting judge, and perform it. Copy the block-resolution the breaker returns onto the held story, and present the run done.
VERIFICATION: the run's block-resolution names the held story and either rules it answerable, with a change (plan, driver-note or card-amendment) and an answer for each dissent the hold names, or rules it blocked with the reason no change within the card can answer the dissent; the held story carries the same block-resolution. The run's done gate (satelle-block-resolution-check) checks both.
EOF
```

Its input selection takes every blocked story; a run works only the one held
story the session names in `held-item`.

## Run it

`<held>` is the held story's id. Start the run and route it to the breaker's
workflow before its first move (a run lands in runbook until it is tagged):

```sh
session=<session>
held=<held>
task=<the task id found above>
exe=$(satelle task run "$task" --json --session "$session" | jq -r '.id // empty')
[ -n "$exe" ] || { echo "could not start a run of $task" >&2; exit 1; }
satelle execution set "$exe" --add-tag block-breaker --session "$session"
satelle execution set "$exe" --reselect block-breaker --session "$session"
satelle story get "$held" --session "$session" | satelle execution attach "$exe" --name held-item --type text/plain --file - --session "$session"
satelle story attachment "$held" plan --session "$session" | satelle execution attach "$exe" --name held-plan --type text/markdown --file - --session "$session"
satelle ledger list "$held" --json --session "$session" | jq '[.[] | select(.kind == "gate.run" and .payload.result == "reject")]' | satelle execution attach "$exe" --name held-findings --type application/json --file - --session "$session"
```

A story with no plan (a chore, or one held from backlog) has no `held-plan`;
skip that line.

### Choose the breaker

The hold (in `satelle story get <held> --session <session>`, under the held
state) names each dissenting verdict as a gate and its binding. A binding's
family is the vendor its `model` names in `.satelle/agents.toml` (claude, gpt,
gemini or grok); a check's dissent has no family. Each breaking state of the
block-breaker workflow is performed by one binding
(`satelle workflow show block-breaker --session <session>` lists them), one
state per family.

1. Strike every family a dissenting binding runs.
2. Of the states left, prefer one whose family is not also the held work's
   author: the planner's for a hold on plan, the session's own harness for a
   hold on code or on the card. If only the author's family is left, take it:
   every other family dissented.
3. If no state is left, every family dissented: do not run the breaker. The
   hold goes to the operator as it is.

Then move the run into that state, seat it, and perform its breaker:

```sh
session=<session>
exe=<the run>
state=<the chosen breaking state>
satelle execution set "$exe" --status "$state" --session "$session"
satelle execution seat "$exe" --session "$session"
satelle execution perform "$exe" --session "$session"
```

### Land the resolution

The breaker's answer is the run's `block-resolution`. Copy it onto the held
story and close the run; the done gate checks the form and the copy:

```sh
session=<session>
exe=<the run>
held=<held>
satelle execution attachment "$exe" block-resolution --session "$session" | satelle story attach "$held" --name block-resolution --type text/markdown --file - --session "$session"
satelle execution set "$exe" --status done --session "$session"
satelle execution release "$exe" --session "$session"
```

What the session does next with the held story is in `.satelle/session.md`.
