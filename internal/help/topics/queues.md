# Queues

A queue gathers items, and its trigger runs a task or a workflow over them.
Satelle schedules and watches nothing: a queue fires only inside the command
that fills it, or when fired by hand.

## Declaring a queue

A queue is declared in a tracked file, `.satelle/queues/<name>.toml`, like a
workflow. The file is the queue's definition; the ledger keeps its members and
fires (`queue.added`, `queue.removed`, `queue.fired`) on the queue's chain.
This repo's `.satelle/queues/release.toml`:

    title = "Release queue: approved stories"
    id = "que_65bc4edc"   # optional: adopt a queue made before it had a file

    [filter]              # optional; without it, explicit adds only
    kind = "story"        # also status, tag, category
    status = "approved"

    [trigger]             # optional; without it, the queue never fires
    task = "tas_42301d36" # or workflow = "<name>", exactly one
    base = "origin/main"  # the ref a run's base comes from
    min_size = 0          # 0 fires by hand only

- The name is the file's name (lower-case letters, digits, dashes). Every queue
  command takes it: `satelle queue show release`.
- The queue's chain is `id` when set, keeping that queue's members and fire
  history, else `que_<name>`, created on first use.
- An edit to the file applies on the next command. `satelle queue set` refuses
  the title, filter and trigger of a file-defined queue: edit the file.
- The chain mirrors the file, so a reader of the chain alone (a synced replica,
  satelle.dev) sees the definition. The genesis carries the title, filter and
  trigger; when the file's differ from the chain's, the next command appends
  one `item.updated` with all three (actor `satelle`, cause
  `queue file <path>`), and nothing when they agree. An absent `[filter]` or
  `[trigger]` is written as an empty value, meaning cleared: inputs `{}`, a
  trigger with neither `task` nor `workflow`.
- A `que_<name>` chain the file wrote before this runtime's first ledger pull
  gives way to the store's chain for that id: while it holds only the file's
  mirror events, pull and sync replace it with the store's chain and mirror the
  file onto that. Once it holds any other event (a `queue add`, a fire), a fork
  is refused like any other.
- `satelle validate` refuses an unknown key, a missing title, an empty
  `[filter]`, a trigger naming both or neither target, a negative `min_size`, a
  trigger workflow that is not configured, and two files binding one chain.

`satelle queue create` still makes a queue on the ledger alone (no file), for a
one-off or for an external producer:

    satelle queue create --title "Inbox" --input-kind story \
      --trigger-task tas_… --trigger-base origin/main --min-size 0
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
(the file's `[filter]`, or `satelle queue set <que> --input-… | --clear-inputs`
for a queue with no file).

An explicit member lapses when its item moves on, so a fire never takes work
that is no longer ready:

- Added by a state's enqueue (cause `<workflow>:<from>-><to>`): it lapses once
  the item's status is no longer that state, e.g. an approved story sent back
  to in_progress, or moved to done.
- Added by hand or by an external producer (any other cause): it lapses when
  the item reaches a terminal state of its workflow. `queue add` refuses an
  item that is already terminal.

A lapsed member is not in `queue show`'s members and a fire does not take it;
`queue show` lists it under `lapsed (n)` as `left: <status>`. It stays a member
when the filter selects it. Entering the state again enqueues it again. The
ledger keeps its `queue.added`: a lapse is read from the item's status, not
recorded. A cause an external producer writes in the `x:y->z` shape is read
as a state cause. `queue remove` still clears a lapsed member.

An add with a cause that is neither empty nor a state cause is idempotent per
queue, item and cause: if the chain already records that item added with that
cause, before or after any fire, it is reported as already queued and nothing
is written, so a producer's retry never re-queues an item a run took. A
different cause adds again, and `queue add` (no cause) and a state's enqueue
keep re-adding after a fire.

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

A workflow state can add every item entering it to queues, by name:

    [states.approved]
    enqueue = ["release"]

Each name must have a `.satelle/queues/<name>.toml` (`satelle validate`), and
the queue must be open when the move is presented, or the move is refused
before any gate runs. The item is added (cause `<workflow>:<from>-><to>`)
after the move is recorded, and the trigger fires there when it fills. The
member lapses when the item leaves the state (see Members), and every entry
into the state adds it again.

## Where it shows

`satelle queue show` and satelled's Queues tab show each queue's filter,
trigger, members (added or by filter, with their status) and last fire with
its run's status; the tab updates live as items and runs move. A queue
created by an external producer (satelle-server turning an event into an item)
syncs like any item.
