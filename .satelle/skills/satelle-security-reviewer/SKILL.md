---
name: satelle-security-reviewer
type: skill
description: The wiring guide of the security reviewer pack. Says which transitions the security review gate suits, the read-only judge of another model family it needs, and how to run it only on changes that touch scripts. Runs nothing; read it when taking the pack.
---

# Security reviewer (wiring guide)

This pack ships one judge skill: a cold, read-only security review scoped to
security alone. The pack ships the skill only. Wiring it into a workflow is
the taking repo's own work; this guide names no state, gate name or agent of
any one repo.

## The member

- `skills/satelle-security-review/SKILL.md`: the security judge. It checks
  secrets and credentials, injection and unsafe input, authentication and
  authorization, process controls, network and supply chain, and writes
  outside the work (checks 1–6), and answers with a JSON verdict that names
  each finding's file and line.

## Where it belongs

Name the gate as the taking workflow likes. It suits these transitions:

- **A plan review**: the move out of the state where a plan is attached and
  approved for building. With no diff in its prompt, the gate judges the
  item's plan attachment (`attachments/plan`).
- **A code review**: the move that lands the change for review. With the
  changed paths and the diff in its prompt, the gate judges the change.
- **Any route that lands scripts or pack changes under `.satelle/`**, such as
  a route for process changes: a skill directory can hold a script that runs
  unattended, so a change there is as much code as any other.

Put the gate in the same transition group as the repo's other judged gates,
before any gate that publishes or merges the work.

## The binding it needs

```toml
[gates.<gate>]
skill = "satelle-security-review"
panel = ["<agent>"]
combine = "all"
```

`<agent>` names an agent in `.satelle/agents.toml` that has read-only tools
only (Read, Grep, Glob) and is of another model family than the performer
that authored the work, so the judge does not share the author's blind spots.
Add the satelle-shipped `satelle-judge-differs-from-performer-check` on the
same transition to enforce that rule. Any `after` verdict checks are the
repo's own.

## Triggering only on script changes

Leave the gate without a trigger to review every change. To review only the
changes that touch scripts, add `trigger = "<trigger skill>"` to the gate and
write that trigger as a `type: check` skill of the repo's own. It reads the
`paths` of `satelle story diff "$SATELLE_ITEM" --json --session <session>`
and:

- exit 0 when any path matches the repo's script pattern (for example a path
  ending `.sh`, a `scripts/` directory, or a skill directory under
  `.satelle/skills/`): the gate runs;
- exit 1 when no path matches: the gate is skipped and the trigger's output
  is noted;
- any other exit means the trigger is broken, and the gate runs anyway.

## Taking the pack

Install it with `satelle pack install <ref> --version <v> --session <session>`,
then wire the gate with the user as the satelle-pack skill,
`.satelle/skills/satelle-pack.md`, says.
