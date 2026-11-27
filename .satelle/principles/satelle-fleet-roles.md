---
name: satelle-fleet-roles
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: A fleet of sessions has one orchestrator per control repository and any number of workers. The orchestrator assigns stories, sizes the fleet from machine capacity, fires releases, lands substrate stories, runs block breakers and carries operator decisions. A worker drives one assigned story to approved (done for a substrate story), reports, and never fires a release or lands. A session with no orchestrator does both jobs, as satelle-agent-goals says.
---

# Fleet roles: orchestrator and workers

A control repository may be worked by one session alone or by a fleet: one
orchestrator session and the worker sessions it assigns stories to. Which one a
session is comes from how it was started: a session given a story by an
orchestrator, with an address to report to, is a worker; a session the operator
starts to run the fleet is the orchestrator; any other session works alone.

## The orchestrator

There is one orchestrator per control repository. It:

1. **Assigns stories.** It picks stories whose dependencies are met and hands
   each to a worker session through the host app
   ([[satelle-operator-rulings]], 2026-11-22: a host app such as herdr carries
   the sessions), naming the story and the address the worker reports to. It
   keeps stories whose plans touch a common path apart, as
   [[satelle-parallel-driving]] says.
2. **Sizes the fleet.** How many workers run is its choice, bounded by the
   hardware and by the other projects sharing the machines, not by a count
   fixed in this repository. Its source is the capacity command
   sto_deb291e7 delivers (`satelle help --session <session>` lists it once it
   ships); until then it reads the same facts by hand when it decides. It reads
   capacity on demand, when it is about to start or stop a worker, or at a
   considerate cadence of minutes, never as a tight polling loop: other
   projects share the machines ([[satelle-shared-machine]]).
3. **Fires releases.** It decides when to fire the release queue and drives
   the run to done ([[satelle-agent-goals]]; `.satelle/skills/release/SKILL.md`).
4. **Lands substrate stories.** It lands a worker's done substrate story as
   `.satelle/skills/substrate.md` step 6 and the 2026-11-28 landing ruling in
   [[satelle-operator-rulings]] say, or lets the worker land it when it says
   so.
5. **Runs block breakers.** When a worker reports a hold, it runs or directs
   the block breaker (`.satelle/tasks/block-breaker.md`) and restarts the story
   on a plan-only or driver-note resolution.
6. **Carries operator decisions.** A story amendment, a blocked ruling, a
   refused permission or any other decision that is the operator's goes from
   the worker to the orchestrator to the operator, and the answer comes back
   the same way. It never decides one itself.

## A worker

A worker drives the one story it was assigned:

1. It drives the story to `approved`, or to `done` for a substrate story, on
   its own workflow, through every gate, from the story's own worktree.
2. It reports to the orchestrator at `approved` or `done`, at a hold, and when
   it needs an operator decision, then stops.
3. It never fires a release and never lands a substrate story, unless the
   orchestrator tells it to land one.
4. It keeps the machine quiet for another worker's integration check when that
   worker asks, as [[satelle-parallel-driving]] says.

## A session alone

A session with no orchestrator has both roles for its own stories: it drives a
story to `approved`, fires the release and follows the run to `done`, and lands
its own substrate stories, as [[satelle-agent-goals]] says. Nothing here
changes that.
