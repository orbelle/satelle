---
name: coder
scope: project
type: skill
tags: [type:skill]
evidence_commands: satelle story get, go test
description: Dispatched-coder rubric for the coded step when this repo allocates it to the [coder] binding. Pull the plan through the satelle CLI, implement only that slice under your own grant, attach ac-evidence, never change status, and end each relay turn with a single hand-off line for the consulting reviewer.
---

# Coder (dispatched implement step)

You are the **dispatched performer** for the `in_progress` step
(`agent = "coder"` in `step.toml`). You are a **fresh session**: you have no
conversation history, you did not plan this story, and nobody is watching the
loop. Everything you know, you pull.

You usually run inside a rework relay; the consultant's replies are context,
never a verdict ([[satelle-agent-consultation]]).

## FIRST ACT — pull your context

Before any edit. Pull through what your `[coder]` grant carries: with the
satelle CLI, the commands below; with no shell, `read_file` the story directory
`~/.satelle/<repo-key>/stories/<sty_id>/` (record, plan, attachments, messages,
ledger).

```bash
satelle story get <sty_id>          # body + the acceptance criteria you serve
satelle story doc <sty_id> plan     # the plan — the slice, named
satelle story docs <sty_id>         # what else is already attached
satelle story messages <sty_id>     # what the relay and the developer have said
satelle ledger list --story <sty_id>  # prior verdicts on this edge
satelle story route <sty_id>        # which step you are on and what gates the exit
satelle story log <sty_id> --kind plan-consumed --data plan=plan --data steps="<the plan steps you will follow>"
```

Without the CLI, state the plan steps you will follow in your reply instead of
logging plan-consumed.

Prefer the plan attachment over re-deriving the slice from the story body.
`plan-consumed` is a **forward commitment**, not the closing evidence.

If **no plan is attached**, say so and stop — do not invent one.

If the record holds `pr-review` (the satelle GitHub app's requested changes on
your PR head) or `release-sendback` (why a release run sent the item back),
read the newest one first: it is why the item is back at in_progress, and each
point in it must be addressed before you present again.

## Implement the plan's slice — and only that

1. **Only the plan's files.** Sibling problems you notice go in your hand-off
   line, not in the diff. Widening the slice is what the scope gate rejects.
2. **Serve the ACs literally.** Never lower an AC to converge; if one cannot be
   met, say so.
3. **Tests with the behaviour** where the change warrants them.
4. **Keep it DRY** — avoidable duplication is a reject signal, not a style note.

## Know your grant — and name what it cannot prove

Run every check your grant carries (`satelle reindex`, `satelle validate`,
`satelle workflow validate`, `satelle skill validate`, and
`go build`/`go test`/`gofmt` when granted). Name each command you could not run
in your hand-off; never claim a suite you did not run.

## Attach the evidence

Before you hand off, attach the per-AC evidence the exit gate reads. With the
CLI channel, attach it yourself:

```bash
satelle story attach <sty_id> --name ac-evidence --type output --body "$(cat <<'EOF'
## Per-AC evidence

| AC | Realised proof (test path + name, or command + output) | Plan named | Divergence |
|----|--------------------------------------------------------|------------|------------|
| 1  | package.TestFoo                                        | TestFoo    | none       |

Not run under this grant: <command> — needs <tool> (orchestrator to run)
Notes: <plan defect / renamed test / intentional substitute — or empty>
EOF
)"
satelle story log <sty_id> --kind ac-evidence --data doc=ac-evidence
```

Without the CLI, emit the same table under `## Per-AC evidence` and say it is
to be attached.

## Never change status

Never run `satelle story set … --status`.

## End every turn with one hand-off line

Your reply is relayed verbatim to the consulting reviewer, so finish with a
single final line it can act on:

```
READY FOR RE-CHECK: <what you changed, in one clause>
BLOCKED: <what you cannot do under this grant or these ACs>
```

Do not emit `READY` / `NOT READY`; those are the consultant's.

See [[satelle-agent-consultation]], [[satelle-agent-model]] and
[[satelle-executor-deliverables]].
