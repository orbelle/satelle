---
name: satelle-pr-gates
type: skill
description: The wiring guide of the pull-request gates pack. Says which transition each member gate belongs on, what configuration each reads, and how the base branch's ruleset requires their commit statuses from the satelle GitHub app. Runs nothing; read it when taking the pack.
---

# Pull-request gates (wiring guide)

This pack ships four check skills that carry an item through a pull request:
a head made ready, an approving review published as the satelle GitHub app,
that review read back as the approval, and every gate's result posted on the
head as a commit status a branch ruleset can require. The pack ships skills
only. Wiring them into a workflow is the taking repo's own work; this guide
names no state, gate name, queue or branch of any one repo except where a
member itself depends on it.

## The members

- `skills/satelle-gate-status-publish/SKILL.md`: posts each gate's result on
  the head as the commit status `satelle/<gate>`.
- `skills/satelle-pr-ready-check/SKILL.md`: the head is committed, pushed,
  contains the base branch, and has an open pull request and a changelog
  attachment.
- `skills/satelle-pr-approve-from-review/SKILL.md`: publishes the app's
  approving review on the head from the accepted verdict of the gate its
  `verdict_gate=` line names.
- `skills/satelle-pr-approval-check/SKILL.md`: passes only on the app's
  approving review on the head.

## Where each gate goes

Name each gate as the taking workflow likes; the roles are fixed.

- **The pending gate** names `satelle-gate-status-publish` as its skill and
  goes first on the move into the review state. It posts `pending` for every
  status context before the other gates run.
- **The status entry.** `satelle-gate-status-publish` is also the last entry of
  the `after` list of every other gate in this pack. That `after` entry is how
  it learns which gates to post for: it reads the workflows
  (`satelle workflow show --session <session>`) and posts for each gate whose
  `after` names it.
- **The ready gate** names `satelle-pr-ready-check` and goes on the move into
  the review state, after the pending gate.
- **The review gate** names `satelle-pr-approve-from-review` and goes on the
  same move, after the ready gate. It reads from the item's ledger the
  accepted verdict of the gate its member's `verdict_gate=` line names,
  recorded on the move its `verdict_from=` and `verdict_to=` lines name; they
  default to `code-ac` on `in_progress` -> `integration`. A route whose change
  is judged by another gate on another move, such as a substrate route's
  `reference-review` on `in_progress` -> `approval`, edits those three lines in
  its installed copy of the member. A route that records no accepted verdict of
  the named gate on the named move cannot use the review gate.
- **The approval gate** names `satelle-pr-approval-check` and goes on the move
  out of the review state into the approved state. When the app requests
  changes it reopens the item at `in_progress`, so the workflow needs that
  rework edge. The review state may declare `waits`; the check quotes it while
  no approval is present.

Each member is wired only where the repo wants it. Any member may stay
unwired, or give way to the repo's own skill for the same role: a repo that
runs its own ready check keeps it and leaves `satelle-pr-ready-check` unwired.
The approval gate reads the review the review gate publishes, so wire it with
the review gate or with whatever publishes that approving review in its place.

## What each reads

- **The base branch** (ready, review and approval gates): the `trigger.base`
  of the queue the item's workflow enqueues into (`satelle story route`,
  `satelle queue get`). When the workflow enqueues into no queue with one, the
  git remote's default branch (`checkout.defaultRemote`, else the only remote).
- **Ready gate:** the item's `changelog` attachment, the open non-draft pull
  request whose title ends with the item id in parentheses, and HEAD pushed
  and containing the base branch.
- **Review gate:** the accepted verdict of the gate its `verdict_gate=` line
  names on the move its `verdict_from=` and `verdict_to=` lines name, and the
  change it judged. It
  publishes the review through the project's GitHub binding on its satelle
  server, so the project must be bound to a GitHub repository there.
- **Approval gate:** the app's reviews on the head, read with `gh` and matched
  by its `app=` and `app_account=` lines, and the item's `github.review`
  events.
- **Status entry and pending gate:** the repo's workflows. It posts commit
  statuses through the same GitHub binding and reads them back with `gh`,
  comparing the poster with its own `app_account=` line.
- All four use `gh` (logged in), `jq`, `git` and `satelle`. `pack.toml`
  declares the network host they reach.

## Requiring the statuses

In the GitHub ruleset of the base branch, add a required status check for
each `satelle/<gate>` context, where `<gate>` is the name the taking workflow
gives each gate whose `after` names `satelle-gate-status-publish`. Pin each
check's source to the satelle GitHub app, so only the app's posts satisfy it;
the app's id and account are the `app=` and `app_account=` lines of
`skills/satelle-pr-approval-check/SKILL.md`. Because the pending gate posts
`pending` first on every presentation, a success from an earlier presentation
of the same head never stays green.

## Taking the pack

Install it with `satelle pack install <ref> --version <v> --session <session>`,
then wire each gate with the user as the satelle-pack skill,
`.satelle/skills/satelle-pack.md`, says.

A repo that lacks that file gets it from the substrate story
`satelle init --session <session>` prints for it:
`satelle init --session <session>` and `satelle doctor --session <session>`
name it as a shipped file the repo lacks, with the story to raise and the
`satelle init --print <path> --session <session>` command that writes it.
Raising that story is the user's call.
