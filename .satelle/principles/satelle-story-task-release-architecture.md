---
name: satelle-story-task-release-architecture
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Target architecture for epic:story-task-release — a route ends at a configured state, merge and release are a separate local task, forge actions are repo-configured commands, satelle holds no forge credential, a queue is defined in a file and its entries live on the queue's own chain, and satelle ships no merge or release policy.
---

# Story / task release architecture

Every story tagged `epic:story-task-release` is planned and reviewed against
this. A misfit is rejected, or this is amended first.

## 1. A story ends at a configured state
A route ends at a terminal state or at a configured state where the story
waits, such as `approved` ("PR approved, awaiting release"), which adds it to a
queue (`enqueue = ["release"]`). A story at such a state is not finished: the
session that drove it there keeps its seat until the story is terminal, and is
the one that fires the queue
(`satelle queue fire release --session <session>`). Where a story stops is
configuration, never a Go default.

## 2. Merge and release are a separate local task
The merge, release and verify lane is a task on a local trigger (a schedule,
or a condition over story state): a separate run, not a state of the story's
workflow. The session that enqueued the story fires it and drives that run to
done, for a single story as for an epic's children. Task and story share only
the PR and the story's chain; the task records its outcome
on the story and never takes its seat or worktree. An epic's children release
like any approved story: when every open child is approved the driving agent
fires the release queue (`.satelle/skills/release.md` step 0), and the epic
closes through its own gates once they reach done; a child the run sends back
keeps the epic open.

## 3. Forge actions are commands the repo configures
Opening, reviewing, approving and merging a PR are repo-declared commands
behind a provider-neutral seam; no forge-specific logic in the binary. A review
verdict is published under the reviewing role's own identity by a configured
publisher command.

## 4. Identity belongs to the repo's tooling
A role's forge identity is carried by the repo's command, token or app. satelle
stores, mints and forwards no credential.

## 5. Queue entries are work state on the queue's chain
A queue is defined in a file, `.satelle/queues/<name>.toml` (its filter and
trigger), and is an item with its own `que_` chain; the kernel mirrors the
file's definition onto that chain. Entering a state that names the queue in
its `enqueue` list, or `satelle queue add`, appends `queue.added` on the
queue's chain; `satelle queue remove` appends `queue.removed`, and a fire
appends `queue.fired`. Membership is a fold of those events and the filter; no
queue table or second transport ([[satelle-sync-architecture]] §1).

## 6. Mechanism only
satelle ships waiting steps, triggers, the queue, queues and
publisher seams. Merge and release policy is the repo's configuration
([[satelle-mechanism-vs-substrate]], [[satelle-agent-agnostic]]).

## 7. Rules every slice carries
[[satelle-sync-architecture]] §6, rules 1-4.
