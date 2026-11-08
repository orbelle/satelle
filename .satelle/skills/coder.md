---
name: coder
scope: project
type: skill
tags: [type:skill]
evidence_commands: satelle story get, go test
description: The in_progress performer's rubric (delivery and chore workflows), bound to the interactive session. Pull the plan through the satelle CLI, implement only that slice, attach ac-evidence, never change status, and end each relay turn with a single hand-off line for the consulting reviewer.
---

# Coder (implement step)

You are the **performer** for the `in_progress` state of the delivery and chore
workflows (`performer = { agent = "executor", skill = "coder" }`), which
`.satelle/agents.toml` binds to the interactive session (`executor`,
`command = "session"`): nothing is dispatched. Do not rely on conversation
history for what this story needs; pull it. If a dispatched binding performs
this step instead, its prompt names the record folder: read the item's
attachments there (`attachments/<name>`).

You usually run inside a rework relay; the consultant's replies are context,
never a verdict ([[satelle-agent-consultation]]).

## FIRST ACT — pull your context

Before any edit:

```bash
satelle story get <id>                # body + the acceptance criteria
satelle story attachment <id> plan    # the plan — the slice, named
satelle story attachments <id>        # what else is already attached
satelle story route <id>              # your position and the latest gate rejects
satelle ledger list <id>              # prior gate results on this item
```

The ACs to implement are the plan's `## Acceptance criteria` section when it has
one, else the story's: the same list the judges use.

Prefer the plan attachment over re-deriving the slice from the story body.

If **no plan is attached**, say so and stop — do not invent one.

If the record holds `pr-review` (the satelle GitHub app's requested changes on
your PR head) or `release-sendback` (why a release run sent the item back),
read the newest one first: it is why the item is back at in_progress, and each
point in it must be addressed before you present again. On a send-back, merge
origin/main into your branch (`git fetch origin && git merge origin/main`);
never rebase or force-push the pushed head; resolve any conflict in the real
work; the route re-runs the full suite at integration -> approved.

Record the item's changelog lines as its `changelog` attachment
(`satelle story attach <id> --name changelog --type text/markdown --file -`,
format in `satelle-pr-ready-check.md`). Never edit `CHANGELOG.md` or
`.version`: the release run writes the version section from the attachments.

## Implement the plan's slice — and only that

1. **Only the plan's files.** Sibling problems you notice go in your hand-off
   line, not in the diff. Widening the slice is what the scope gate rejects.
2. **Serve the ACs literally.** Never lower an AC to converge; if one cannot be
   met, say so.
3. **Tests with the behaviour** where the change warrants them.
4. **Keep it DRY** — avoidable duplication is a reject signal, not a style note.

## Know your grant — and name what it cannot prove

Run every check your grant carries: `satelle validate`, and the commands of
`satelle-format-vet-check` (`gofmt -l internal cmd tests scripts`,
`go vet ./...`, `GOOS=windows go build ./...`, `sh scripts/audit-literals.sh`)
and `satelle-build-unit-check` (`go build ./...`,
`go vet -tags integration ./tests/...`, `go test ./...`). Name each command you
could not run in your hand-off; never claim a suite you did not run.

## Attach the evidence

Before you hand off, attach the per-AC evidence the exit gate reads:

```bash
satelle story attach <id> --name ac-evidence --type text/markdown --file - <<'EOF'
## Per-AC evidence

| AC | Realised proof (test path + name, or command + output) | Plan named | Divergence |
|----|--------------------------------------------------------|------------|------------|
| 1  | package.TestFoo                                        | TestFoo    | none       |

Not run under this grant: <command> — needs <tool> (orchestrator to run)
Notes: <plan defect / renamed test / intentional substitute — or empty>
EOF
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
