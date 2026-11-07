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
    trigger = "relevant"         # relevance: a check skill whose exit 0 means relevant
    carry = true                 # an earlier accept over unchanged coverage is carried
    after = "publish"            # a check skill run once the gate has a verdict
    timeout = "30m"              # bounds this gate's check scripts; default 10m

A transition runs its groups in order. A `sequential` group stops at its first
reject (fail-fast); a `parallel` group runs every gate. Every gate's result —
accept, reject, skipped (not relevant) or carried — is recorded on the ledger
with its notes, tokens and duration. The item moves only when no gate rejects.

A skill whose frontmatter says `type: check` is a deterministic check: its first
fenced `sh` block runs with `SATELLE_ITEM`, `SATELLE_FROM`, `SATELLE_TO` and
`SATELLE_BASE` set; exit 0 accepts, anything else rejects, and its output is the
notes.

A gate's `after` is a check skill run once the gate has a verdict: an accept or
reject from its check or judge (every panel member answered), or a carry. It
does not run on a skip, or on a reject that is a failure rather than a verdict
(a missing skill or agent, a trigger or coverage error, a failed dispatch, an
answer with no verdict, the reviewer-differs refusal). Besides the check
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
