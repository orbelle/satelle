---
name: satelle-shared-machine
type: principle
tags: [type:principle]
applies_to: ["*"]
description: A performer shares its machine with the operator and other agents; it never loads it deliberately, starts no long-lived background work, and keeps every command under four minutes.
---

# The machine is shared

A performer runs on a machine the operator and other agents are using.

- Never load it on purpose: no CPU or memory hogs (`yes`, stress tools, busy
  loops), no "run it under load" reproductions.
- Start no background or detached long-lived process; every process you start
  ends with your command.
- Keep each command under four minutes: bound `-count` and `-timeout`, run only
  the tests you touched, and leave the full suite to the gates.
- Prove a timing or ordering bug by forcing the order in the test (a hook, a
  barrier, a held channel), so it fails every run on the unfixed code. Load and
  volume make a failure likelier; they prove nothing. CI supplies load variety.
