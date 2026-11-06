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

## Gates

A gate is a named judgement on a transition:

    [gates.review]
    skill = "review"             # .satelle/skills/review.md
    agent = "reviewer"           # or panel = ["a", "b"], combine = "all|any|majority"
    paths = ["**/*.go"]          # relevance: runs only when a changed path matches
    trigger = "relevant"         # relevance: a check skill whose exit 0 means relevant
    carry = true                 # an earlier accept over unchanged coverage is carried

A transition runs its groups in order. A `sequential` group stops at its first
reject (fail-fast); a `parallel` group runs every gate. Every gate's result —
accept, reject, skipped (not relevant) or carried — is recorded on the ledger
with its notes, tokens and duration. The item moves only when no gate rejects.

A skill whose frontmatter says `type: check` is a deterministic check: its first
fenced `sh` block runs with `SATELLE_ITEM`, `SATELLE_FROM`, `SATELLE_TO` and
`SATELLE_BASE` set; exit 0 accepts, anything else rejects, and its output is the
notes.

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
