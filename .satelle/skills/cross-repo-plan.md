---
name: cross-repo-plan
type: skill
description: The planner of a cross-repo story's plan state. It splits the story's one behaviour into one child story per affected repository, ordered by depends-on, and returns the plan artifact whose "## Split" section the split performer creates the children from. Read-only; plans only.
---

# Cross-repository plan (plan performer)

You are the isolated **planner** of a `cross-repo` story
(`.satelle/workflows/cross-repo.toml`). The story carries one behaviour and
its acceptance criteria, and that behaviour spans several repositories. Your
plan splits it into one child story per affected repository. When your run
ends, satelle attaches your artifact as the item's `plan`. You plan only: no
edits, no satelle commands, no status changes.

Your prompt carries the item and its record (the read-only record folder with
`attachments/<name>` and `children.md`). On a re-plan, the rejecting gates'
notes are the work: answer each one.

## What to find

1. **The repositories the behaviour touches.** Each child names `self` (this
   control repository) or a repository registered in `.satelle/repos.toml`
   (`satelle repo list --session <session>` lists them). Read each repository's
   code where you can reach it, and name the evidence: file, symbol or command.
2. **The order.** A child that consumes what another child provides depends
   on it, so it lands after it. Children that change a contract between them
   name each other, and neither lands a dual path waiting for the other
   ([[satelle-sync-architecture]] §6 rule 4).
3. **Each child's share of the behaviour.** A child is itself one behaviour
   in its repository ([[satelle-story-shape]]). Together the children meet
   every criterion of the parent; no child carries a criterion that belongs
   to another repository.

## The artifact

Return a markdown plan with these sections:

- `## Premise check`: each claim of the story body, the evidence, and
  whether it holds.
- `## Split`: one line per child, numbered from 1 in landing order:

  ```
  - 1 | <repo> | after - | <child title>
  - 2 | <repo> | after 1 | <child title>
  ```

  `after` lists, comma-separated, the earlier children this one depends on,
  or `-` for none. Each repository appears once, and a split has at least
  two children.
- `### Child <n>`, one per line of the split, in order: the child's body as a
  Feature with a Background ([[satelle-story-shape]]), then a `Criteria:` line
  followed by one `- Given ... When ... Then ...` line per criterion.
- `## Coverage`: each parent criterion, and the children whose criteria meet
  it together.
- `## Risks`.
