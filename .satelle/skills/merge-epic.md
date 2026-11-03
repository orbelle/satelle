---
name: merge-epic
scope: project
type: skill
tags: [type:skill]
description: Executor skill for a container merge step. Brings main level with origin/main, then merges the epic/<id> branch of each child the container's release document names into it. Does not bump .version, does not push, and does not advance status.
---

# Merge epic (in-loop executor step)

You are the **executor** for the container's merge step. Every child this entry releases has discharged `integrated`, or is done or cancelled. **Merge their branches.** You do not bump `.version`. You do not push. You do not advance status. The parent's release step owns the version bump and the push to main.

An epic child's done is not "on main". This step is what puts those branches together. The parent's done, after release, is the push.

## What to do

0. **Start from main as it is on origin**. Run `satelle trunk sync --fast-forward --strict` on the branch you are engaged on. It brings local main level with origin/main and prints what came in. A non-zero exit means main is dirty, diverged, ahead (unpushed commits), cannot be moved, unreachable or unresolved: stop, report its line verbatim, and do not merge onto a stale base.
1. List the children: `satelle story list --tag epic:<theme>` for the container's theme, and drop the epic-parent itself. Also drop every child tagged `lane:trunk`: it already landed on trunk, through its own release or, for substrate, the moment it was made.
2. Keep only the children this entry releases: `satelle story doc <epic> release` lists them, one `- <id>` line each. satelle writes it on every entry into this step — every child, or the subset `satelle story set <epic> --status merging --release all|<id>,<id>` selected. Merge those and no other child; a child left out keeps its status and waits for a later release. When the epic has no release document, the entry releases every child. Skip a child whose branch main already contains: an earlier release merged it. For each remaining child that is not cancelled, the branch is `epic/<child-id>`. Fetch it. If a child has no such branch, stop and say which id. Do not invent a branch from `main`.
3. Merge in dependency order. A child whose `depends-on:` target is done already contains that target's commits if it was cut from that branch. Merge the tip of each chain. Do not merge both a dependency branch and a dependent branch that already contains it onto the same base — that is how the chain conflicts with itself.
4. Resolve conflicts only when they are overlapping edits of the same lines. Do not redesign a child's slice.
5. Leave the result on the branch you were engaged on. Do not push.

## What you must not do

- Do not bump `.version` or edit `CHANGELOG.md`.
- Do not `git push`.
- Do not `satelle story set` the container or a child. Releasing a child never changes its status.
- Do not cancel a child to make the merge smaller.
