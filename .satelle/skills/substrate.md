---
name: substrate
type: skill
description: The in_progress performer of this repo's substrate workflow, bound to the interactive session. Edit .satelle/ only, on the item's own branch; write references, not restated values; validate; commit; present done, whose gates are the validation; once they accept, fast-forward main to the branch. No version, tag, publish or install.
---

# Substrate (in_progress performer)

You are the performer of `in_progress` in the substrate workflow
(`.satelle/workflows/substrate.toml`), bound to the interactive session
(`executor`, `command = "session"`): nothing is dispatched. A substrate item
changes this repo's process under `.satelle/` (workflows, skills, principles,
tasks, configuration) and nothing else. It has no plan state, no integration
and no release: the gates on `in_progress -> done` are the validation, and the
landing is a fast-forward of main to the item's branch.

A change that needs any file outside `.satelle/` (Go, a test under `tests/`, a
script, docs) is not substrate: it belongs on a chore or delivery item. Say so
and cancel or re-raise this one; do not carry it here.

## 1. Pull the item

```bash
satelle story get <id> --session <session>
satelle story attachments <id> --session <session>
satelle story attachment <id> <name> --session <session>
satelle story route <id> --session <session>
```

When the item is back at `in_progress` after a rejected round, each rejecting
gate's notes are the work: answer every one before presenting again.

## 2. Branch and seat

Work on a branch named by the item id, cut from `origin/main`, in its own
worktree, and seat the item there:

```bash
git fetch origin
git worktree add -b <id> ../<id> origin/main
satelle -C ../<id> story seat <id> --session <session>
```

## 3. Edit `.satelle/` only

- Make the smallest edit the story asks for.
- Write a reference, not a restated value ([[satelle-reference-not-restate]]):
  name the command, file or `[[link]]` that answers a version, count, status or
  list, in prose and in code examples alike. A fact that must be recorded is
  dated history, with the date it held. No marker exempts a line.
- Process stays substrate ([[satelle-mechanism-vs-substrate]]): nothing here
  asks the binary to know about it.

## 4. Validate and commit

```bash
satelle validate --session <session>
```

It must report the configuration valid with no substrate finding and no
volatile line. Commit on the item's branch with a conventional subject ending
in the item id. Push the branch (`git push -u origin <id>`) so the work is
not only local.

## 5. Present done

```bash
satelle story set <id> --status done --session <session>
```

The gates on that move (`satelle workflow show substrate --session
<session>`) check that every changed path is under `.satelle/`, that the
configuration validates, that no volatile line is listed, and have the reference
judge read the diff. A reject returns the notes; fix each finding and present
again. After the round cap the item is held at `blocked`: follow
`.satelle/session.md`.

## 6. Land: fast-forward main

Only after the item is `done`, land exactly the commits the gates judged. Run
these from the main checkout, as written, with no `-C`:

```bash
git fetch origin
git push origin <id>:main
```

A done item's seat allows no edits, and the edit gate reads `git -C <worktree>`
with a subcommand that is not read-only as acting on that tree
(`satelle help edit-gate --session <session>`), so the same commands with `-C`
are refused. The push names the branch, not the worktree, so it needs neither.

The push is a plain fast-forward: git refuses it when `origin/main` has moved
past the branch's base. Main's ruleset asks for a pull request, so the remote
prints that the push bypassed that rule; for a done substrate item this is
expected, because its gates on `in_progress -> done` are the review. When the
fast-forward is refused, the branch must be brought onto `origin/main` and
validated again, which changes a tree: do not do it in the done item. Report
it to the operator with the branch and what `origin/main` gained.

Then bring the main checkout to `origin/main` (`git merge --ff-only origin/main`,
run in the main checkout) and remove the item's worktree
(`git worktree remove <path>`).

## Do not

- Edit a path outside `.satelle/`; the paths gate refuses it.
- Bump a version, edit `CHANGELOG.md` or `docs/releases/`, tag, publish, or install: the binary is
  unchanged, so nothing is released.
- Merge to main before the item is `done`, or merge anything but a fast-forward.
- Move past a rejecting gate, or change the item's status any other way.
