---
name: satelle-story-task-release-architecture
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Target architecture for epic:story-task-release — a route ends at a configured state, merge and release are a separate local task, forge actions are repo-configured commands, satelle holds no forge credential, queue entries live in the story chain, and satelle ships no merge or release policy.
---

# Story / task release architecture

Every story tagged `epic:story-task-release` is planned and reviewed against
this. A misfit is rejected, or this is amended first.

## 1. A story ends at a configured state
A route ends at a terminal state or a configured waiting step (`wait = true`),
such as "PR raised, awaiting merge". A waiting story holds no agent seat and no
worktree. Where a story stops is configuration, never a Go default.

## 2. Merge and release are a separate local task
The merge, release and verify lane is a task on a local trigger (a schedule,
or a condition over story state), not a continuation of the story's drive. Task
and story share only the PR and the story's chain; the task records its outcome
on the story and never takes its seat or worktree. An epic releases on demand
(`--release all|<id>,…`) through its merge step; a member left out keeps its
status.

## 3. Forge actions are commands the repo configures
Opening, reviewing, approving and merging a PR are repo-declared commands
behind a provider-neutral seam; no forge-specific logic in the binary. A review
verdict is published under the reviewing role's own identity by a configured
publisher command.

## 4. Identity belongs to the repo's tooling
A role's forge identity is carried by the repo's command, token or app. satelle
stores, mints and forwards no credential.

## 5. Queue entries are work state in the story chain
Entering a waiting step appends `queue_enqueued`; withdrawal appends
`queue_withdrawn`. The queue is a fold of those events; no queue table, file or
second transport ([[satelle-sync-architecture]] §1).

## 6. Mechanism only
satelle ships waiting steps, triggers, the queue, on-demand epic release and
publisher seams. Merge and release policy is the repo's configuration
([[satelle-mechanism-vs-substrate]], [[satelle-agent-agnostic]]).

## 7. Rules every slice carries
[[satelle-sync-architecture]] §6, rules 1-4.
