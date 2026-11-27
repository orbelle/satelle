---
name: satelle-pack
type: skill
description: Followed by the session agent when the user asks to add a pack. It first finds a workflow state whose performer may change the tree, proposing one to the user when the repo has none, installs the pack with satelle pack install, reads the entry skill's guide and needs, proposes each gate for the repo's own workflow from the current workflow, the pack's hints and the user's directions, applies only what the user confirms, and says when the landing route has no security reviewer.
---

# Take a pack

A pack is a set of skill directories placed under `.satelle/skills/`. It ships
skills only, never workflows: wiring its gates into a workflow is this repo's
job, done by you with the user. Satelle never merges a pack into a workflow.
Learn the pack format first: `satelle help configuration --session <session>`
(`## pack.toml`) and `satelle help workflows --session <session>`.

## 1. When

Follow this when the user asks to add, take or install a pack.

## 2. Find the pack

- The pack's ref is `<owner>/<name>` and its version `<v>`. Take both from the
  user, or find them on the satelle.dev marketplace and confirm them with the
  user.
- To look before installing, preview it into a scratch directory with
  `satelle pack fetch <ref> --version <v> --dir <tmp> --session <session>`.

## 3. Install

Install needs a seat at this worktree on a state whose performer may change
the tree. Find or add one before installing.

- **Find a state that may change the tree.** Read each workflow
  `satelle validate --session <session>` loads with
  `satelle workflow show <name> --session <session>`. A state may change the
  tree when it has an agent performer and its `edits` is unset or `"tree"`
  (`satelle help edit-gate --session <session>`). When one exists, move the
  item there and seat it (`satelle story seat <id> --session <session>`), then
  install.
- **When none exists**, as on a repo just made by satelle init, whose starter
  workflow goes backlog -> done with no performer, propose to the user the
  state and agent to add, naming both files and showing the TOML:
  - in `.satelle/workflows/<name>.toml`, added to the workflow:

        [states.in_progress]
        performer = { agent = "executor", skill = "satelle-pack" }

        [[transition]]
        from = ["backlog"]
        to = "in_progress"

        [[transition]]
        from = ["in_progress"]
        to = "done"

  - in `.satelle/agents.toml`:

        [agents.executor]
        command = "session"

  Say that the existing backlog -> done transition stays, and that the user
  may choose other state and agent names.
- **Apply only when the user confirms.** Edit nothing until then. On the
  user's confirmation, add by hand exactly what was confirmed, run
  `satelle validate --session <session>` until it reports the configuration
  valid, move the item to the new state
  (`satelle story set <id> --status in_progress --session <session>`), seat
  it (`satelle story seat <id> --session <session>`), then install. When the
  user declines, tell them install needs such a seat, and stop without
  installing.
- Run `satelle pack install <ref> --version <v> --session <session>`.
  `satelle help configuration --session <session>` (`## pack.toml`) says what
  install refuses. When a check fails it writes nothing, and
  `.satelle/packs.toml` is unchanged: report the refusal to the user.
- Install records the pack in `.satelle/packs.toml`. Installed skills are the
  repo's own to edit; install never merges or overwrites them.

## 4. Read the guide

- The entry skill's `.satelle/skills/<entry>/SKILL.md` is the pack's wiring
  guide. Read it, and each member skill it names.
- Read the `[needs]` of `.satelle/skills/<entry>/pack.toml` and tell the user
  what the pack needs (scripts, agents, network, secrets) before wiring
  anything.

## 5. Wire each gate with the user

- Read the repo's own workflow with
  `satelle workflow show <name> --session <session>` for each workflow the
  user names, or each one `satelle validate --session <session>` loads.
- For each gate the guide describes, propose a `[gates.<gate>]` naming the
  member skill and the transition group it joins. Base each proposal on the
  current workflow, the pack's hints in its guide, and the user's directions.
- A judged gate names a read-only binding, as
  `satelle help workflows --session <session>` (Agents) states.
- Show the proposal to the user. Apply only what the user confirms, by editing
  `.satelle/workflows/<name>.toml` by hand.
- Run `satelle validate --session <session>` until it reports the configuration
  valid, then `satelle workflow show <name> --session <session>` to check each
  gate sits where the user confirmed.

## 6. Security reviewer

- Find the landing route: the transitions the pack's gates join, and the route
  an item takes through them to a terminal state, from
  `satelle workflow show <name> --session <session>`.
- Read each gate's skill on that route. When none of them is a security
  reviewer, tell the user so, and name a security reviewer pack from the
  marketplace, or say that none was found.
- Change no workflow to add a security reviewer unless the user asks.

## 7. Report

Tell the user the skills installed (from the install output or
`.satelle/packs.toml`), each gate wired and where, the pack's needs, and the
security reviewer finding.

## 8. Keep packs current

- Run `satelle pack status --session <session>` to see each installed pack's
  version, its locally edited and missing files, and the newest version the
  marketplace serves. Local edits are expected.
- To have doctor report newer versions, add to `.satelle/doctor.toml`:

      [[check]]
      skill = "satelle-pack-version-check"
      fix = "in a seat that may change the tree, run the update command the issue names, then merge each edited file it lists under .satelle/pack-merge"

- Leave it to the user to decide when to run the `satelle pack update`
  command an issue names.
