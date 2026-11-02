---
id: tsk_pr-approve
type: task
status: done
priority: medium
category: substrate
tags: pr-approve, review
inputs_decl: story_id:string:required
created: 2026-11-02T00:00:00Z
updated: 2026-11-02T00:00:00Z
---

# Approve a story's pull request after a cold review

The run of a PR-approval remote (`[drive] kind = "task"`, task `pr-approve`).
Each wake creates a fresh execution of this task with the story it was woken
for as its `story_id` input. The approval is mechanism: `satelle pr approve`
finds the open PR whose head is `epic/<story_id>`, has the `satelle-pr-review` skill
judge it cold, and only on accept approves the exact head it judged and posts
a commit status there.

## Action
Run `satelle pr approve --story <story_id> --execution <this execution's id>`,
with `<story_id>` the execution's `story_id` input. Do not judge the PR
yourself and do not post to the forge by any other means: the command records
the outcome and the findings on this execution.

## Verification
The execution's run-output doc records an `outcome` (`accept`, `reject`, or
`no-op` when no forge is configured) and the findings behind it, naming the PR
and the head commit it was judged on.

## Acceptance Criteria
1. The execution's run-output doc records an outcome and its findings.
2. An accept names the approving review; a reject posts nothing to the forge.
3. Re-runnable from `done` (a new execution per wake).
