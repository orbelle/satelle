# Workflows, gates, performers and agents

A workflow is one file, `.satelle/workflows/<name>.toml`: its states and the
transitions between them, written out explicitly.

    name = "default"
    [states.backlog]
    start = true                 # exactly one start state
    [states.blocked]
    park = true                  # a park state; resumes = true lets "@prior" return
    resumes = true
    [states.done]
    terminal = true

    [[transition]]
    from = ["backlog"]           # "*" = any state that is not terminal or park
    to = "done"
    groups = [{ mode = "sequential", gates = ["review"] }]

An item gets its workflow at create: `--workflow`, else the first
`[[workflows.rule]]` in `satelle.toml` whose `category`, `tag` and `kind` all
match, else `[workflows] default`. It keeps that workflow.

A state may name queues an item entering it joins, `enqueue = ["<name>"]`,
each a `.satelle/queues/<name>.toml`: `satelle validate` refuses a name with no
file, and the move is refused when the queue is closed (`satelle help
queues`).

## Gates

A gate is a named judgement on a transition:

    [gates.review]
    skill = "review"             # .satelle/skills/review.md
    agent = "reviewer"           # or panel = ["a", "b"], combine = "all|any|majority"
    paths = ["**/*.go"]          # relevance: runs only when a changed path matches
    trigger = "relevant"         # relevance: a check skill; exit 0 runs the gate, exit 1 skips it
    carry = true                 # an earlier accept over unchanged coverage is carried
    after = "publish"            # a check skill run once the gate has a verdict
    timeout = "30m"              # bounds this gate's check scripts; default 10m
    placement = "remote:integration"  # a check gate only: run its check on a node

A transition runs its groups in order. A `sequential` group stops at its first
reject (fail-fast); a `parallel` group runs every gate. Every gate's result —
accept, reject, skipped (not relevant) or carried — is recorded on the ledger
with its notes, tokens and duration. The item moves only when no gate rejects.

A skill whose frontmatter says `type: check` is a deterministic check: its first
fenced `sh` block runs with `SATELLE_ITEM`, `SATELLE_FROM`, `SATELLE_TO` and
`SATELLE_BASE` set; exit 0 accepts, anything else rejects, and its output is the
notes.

A gate's `trigger` is a check skill whose exit decides relevance: exit 0 means
relevant and the gate runs; exit 1 means not relevant, and the gate is recorded
`skipped` with the trigger's output as its notes. Any other exit, or a trigger
killed by a signal, means the trigger failed: the gate runs anyway (it fails
closed) and its notes start
`trigger <skill> failed (exit <code>); the gate ran` (or
`failed (killed by a signal)`), then the trigger's output. A trigger
that times out, cannot start or names a missing skill rejects the gate.

A gate's `after` is a check skill run once the gate has a verdict: an accept or
reject from its check or judge (every panel member answered), or a carry. It
does not run on a skip, or on a reject that is a failure rather than a verdict
(a missing skill or agent, a trigger that timed out or could not run, a
coverage error, a failed dispatch, an answer with no verdict, the
reviewer-differs refusal). Besides the check
variables it sees `SATELLE_GATE`, `SATELLE_GATE_RESULT` (accept, reject or
carried) and `SATELLE_GATE_BINDING`, and its stdin JSON adds `gate`, `result`,
`binding` and `notes`. Exit 0 keeps the result; any other exit turns it into a
reject. Its output joins the notes as `after <skill>: …`. Use it to publish a
verdict somewhere (this repo's `pr-review` gate publishes to the PR with
`satelle github review`).

A gate's `timeout` (a Go duration such as `"30m"` or `"1h30m"`, positive)
bounds each check script the gate runs — its check skill, its trigger and its
`after` — each run separately; without it the default of 10m applies.
`satelle validate` refuses a timeout that does not parse, one that is not
positive, and one on a gate that runs no check script (a judge with no trigger
and no `after`). A check cut at its bound rejects the gate, and the notes
name the bound and where it came from: `timed out after 30m0s (gates.full-test
timeout)`, or `timed out after 10m0s (default; set timeout on
[gates.full-test] to raise it)`. A trigger or `after` that times out keeps its
`trigger <skill>: ` or `after <skill>: ` prefix. A timeout is a failure, not a
verdict, so `after` does not run on it.

`satelle story set <id> --status <to>` runs the gates in that command before
it returns. A gate with a long `timeout` (see `satelle workflow show`) can
outlast a harness's bound on one shell command: run such a move as the
harness's background command, with that command's own timeout above the sum
of the sequential gates' bounds.

A check gate's `placement` says where its check skill runs: `local` (the
default) here, or `remote:<id>` on a node of the docker-local remote
`.satelle/remotes/<id>/` (see `satelle help remotes`). Only a check gate may
be placed on a remote, and only on a docker-local one; `satelle validate`
refuses any other. Relevance (`paths`, `trigger`), carry and `after` still run
here, so a skipped or carried gate starts no node and an `after` keeps its
credentials here. The node judges the item's pushed head: its collected
branch's commit, else HEAD on its branch, which must be committed (a clean
tree, not detached) and in the history of `origin/<branch>`; otherwise the gate
rejects saying to push. The gate's `timeout` bounds the whole node run (start,
setup, fetch and the check). The result is the gate's gate.run as a local
check records it, with `location: remote:<node>` and notes that start
`remote <id>: node <node> at <sha>: exit <code>`. A node that fails to start,
a head that does not check out, a timeout, or a run that brings back no exit
code rejects with the cause named; none of them is a verdict, and none passes.

### Hold

A state may cap the rejected rounds of leaving it, and name the park state
that holds the item for the operator once the cap is reached:

    [states.plan]
    hold = { rounds = 3, to = "held" }
    [states.held]
    park = true
    resumes = true

    [[transition]]
    from = ["plan"]
    to = "held"                  # declared, and gateless
    [[transition]]
    from = ["held"]
    to = "@prior"

A round is one presentation of a transition leaving the state, to a target
that is not a park state, that at least one gate rejected. The count starts
again whenever the item moves, so the rounds counted are rejections in a row.
When a rejection brings the count to `rounds`, the item is moved to `to` in
the same command; the move records a summary: the state and rounds, each
dissenting verdict (gate/binding, or the gate for a check) with how many
rounds it rejected and the class it gave most often (a judge's optional
`"class"` in its verdict object, `unclassified` when it gave none), and the
verdicts that accepted in the last round. `story set` exits 2 and prints it;
`story get` shows it while the item stays held. A park state has no performer,
so the item is not performed again.

`satelle validate` refuses a hold whose `rounds` is below 1, whose `to` is not
a park state, that sits on a terminal or park state, or that has no declared
transition from the state to `to`, or one that carries gates.

The move out of the hold is the operator's, never the driving session's: the
session reports the hold and stops. Once the operator has decided, they run
`satelle <kind> set <id> --status <state>` (for a resuming park, the state it
was held from), which starts the count again.

## The base

`SATELLE_BASE` is the item's diff base: the commit its gates judge the change
from. A seat records it: HEAD when the seat is taken, or the commit
`satelle story seat <id> --base <ref>` names (a stacked branch is seated on the
branch it is stacked on). When HEAD's history no longer contains the base (the
branch was rebased or reset), every transition with gates refuses before any
gate runs, naming the base, HEAD and the re-seat that fixes it; gateless moves
still go through. A gate skipped for relevance (its `paths` or trigger matched
nothing) on an empty diff, or on one missing paths the item's last judged diff
had, says so in a warning on the output and its `gate.run` record.

The base is judged against the item's base branch: the ref the seat's `--base`
named, else `origin/main`. When the recorded base is an ancestor of HEAD's
merge base with that branch, the gates judge from the merge base instead, so
merging `origin/main` into the item's branch never widens the judged diff. The
recorded base stands when the branch is not there, when HEAD is wholly inside
it, or when the base is not behind it (a stacked or story-branch seat).

## Performers

A state may carry a performer, an agent plus a skill:

    [states.build]
    performer = { agent = "builder", skill = "build" }

`satelle story perform <id>` dispatches it. A performer whose agent's command is
`session` is the interactive session itself: nothing is dispatched, and the
session does the work under the edit gate.

### The operator's states

A state can be the operator's alone: no agent performs it, and leaving it needs
the operator's recorded word. Its performer names the attachment type the
operator records, and takes no agent or skill:

    [states.deploy]
    performer = { operator = "deploy-record" }

While an item is in such a state, satelle dispatches nothing (`perform`
refuses, a drive stops there) and a seat on it allows no agent edits. Every
transition out of it, `*` moves such as cancel included, is refused until the
operator has recorded an attachment of the declared type since the item last
entered the state; a record from an earlier visit does not count. The
transition then runs its configured gates as usual.

The operator records it from their own terminal, with `SATELLE_SESSION`
unset:

    satelle story record <id> --file F [--name N] [--type-media M]

The type comes from the state; the name defaults to it. Before writing
anything the verb asks for the item id to be retyped on the controlling
terminal (`/dev/tty`); it never reads stdin, and without a terminal it refuses.

Three checks refuse an agent: the kernel refuses any call made with an agent
session (`SATELLE_SESSION`), the edit-gate hook refuses the verb in any agent
shell call (behind wrappers such as `env`, `timeout` or `xargs`, and named in
quoted, heredoc or `-c` program text), and the terminal retype needs the
person at the keyboard. The operator mark is set only by this verb: `attach`,
performer artifacts and the server's attach tool never set it, and
`satelle ledger append` refuses every kind satelle reads (item, seat and
attachment events), so a raw append can neither forge the record nor move the
item.

### What a performer and a judge receive

Each dispatched performer gets its skill and the item (title, body, criteria).
When the latest attempt to leave the item's state was rejected, the prompt also
lists each rejecting gate with its notes (8 KiB each), so a re-run can address
them. Each judge gets the item, the change it judges, and the same record.
A judge answers with a JSON object `{"verdict": "accept"|"reject", "notes":
"..."}`; the last such object in its answer counts, and an answer without one
is a reject.

The record is the newest version of each attachment and the item's children.
Before a run on this machine (and on a remote node, from its own ledger)
satelle writes it to a read-only folder under `.satelle/runs/` (gitignored),
names every file in the prompt, and removes it after the run; small bodies are
also inlined, up to 16 KiB each and 64 KiB in all, newest first, each cut
marked with the bytes shown and the file to read. A detached run gets the
record inline only. The session reads any version itself with
`satelle story attachment <id> <name> [--sha S]`.

## Agents

`.satelle/agents.toml` is a free catalog of bindings:

    [agents.reviewer]
    command = "claude"           # claude | pi | grok | session, or a command line
    model = "..."
    tools = ["Read", "Grep", "Glob"]

Permissions are tool grants, declared up front and enforced by the adapter. A
binding that judges a gate must have a read-only grant on an adapter that
enforces grants, or `satelle validate` refuses the configuration. With
`[rules] reviewer_differs_from_performer = true`, a judge may not run the model
of the performer whose work it judges.
