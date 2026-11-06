# Queues

A queue is an item of kind `queue` (id `que_…`). It gathers items, and its
trigger runs a task or a workflow over them. Satelle schedules and watches
nothing: a queue fires only inside the command that fills it, or when fired by
hand.

    satelle queue create --title "Release queue" \
      --input-kind story --input-status approved \   # the filter
      --trigger-task tas_42301d36 --trigger-base origin/main --min-size 0
    satelle queue add <que> <id>...      # explicit members
    satelle queue remove <que> <id>...   # explicit members only
    satelle queue list [--json]
    satelle queue show <que> [--json]    # filter, trigger, members, last fire
    satelle queue fire <que> [--base ref] [--json]

## Members

A queue's members are its explicit items (added with `queue add` or by a
workflow state, in add order), then every item its filter selects
(`--input-kind`, `--input-status`, `--input-tag`, `--input-category`: the same
selection a task's inputs use). The queue itself, the trigger's task and the
runs under either are never members. `queue remove` takes only explicit items;
a filter member leaves when the item or the filter changes
(`satelle queue set <que> --input-… | --clear-inputs`).

## The trigger

`--trigger-task <tas>` starts an execution under that task, as `task run`
does; `--trigger-workflow <name>` starts an execution under the queue in that
workflow's start state. Either way the run's `task.run` record lists the
members it took, with the queue that fired it, and the queue's ledger records
`queue.fired` naming the run. A fire takes every member, so the explicit list
empties.

- `--min-size 0` fires only by hand (`queue fire`).
- `--min-size N` fires when an enqueue (`queue add`, or a transition into a
  state that enqueues) leaves the queue holding N or more members. The fire
  runs in that same command and process.
- `--trigger-base` is the ref a run's base comes from when `queue fire` gives
  no `--base` (default HEAD).

A queue fires again only after the last run reached a terminal state: while it
is open `queue fire` exits 2 naming it, and an enqueue that reaches the size
reports the fire as held and keeps the items queued. Two processes firing at
once are serialised by the queue's chain: the one that loses exits 2 and
creates nothing.

## Enqueue from a state

A workflow state can add every item entering it to queues:

    [states.done]
    terminal = true
    enqueue = ["que_1a2b3c4d"]

The queue must exist and be open when the move is presented, or the move is
refused before any gate runs. The item is added (cause `<workflow>:<from>-><to>`)
after the move is recorded, and the trigger fires there when it fills.

## Where it shows

`satelle queue show` and satelled's Queues tab show each queue's filter,
trigger, members (added or by filter, with their status) and last fire with
its run's status; the tab updates live as items and runs move. A queue
created by an external producer (satelle-server turning an event into an item)
syncs like any item.
