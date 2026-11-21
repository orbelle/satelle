---
name: satelle-pack
type: skill
description: Followed by the session agent when the user asks to add a pack. It installs the pack with satelle pack install, reads the entry skill's guide and needs, proposes each gate for the repo's own workflow from the current workflow, the pack's hints and the user's directions, applies only what the user confirms, and says when the landing route has no security reviewer.
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

- Run `satelle pack install <ref> --version <v> --session <session>`.
- It needs a seat at this worktree on a state whose performer may change the
  tree. `satelle help configuration --session <session>` (`## pack.toml`) says
  what install refuses. When a check fails it writes nothing, and
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
