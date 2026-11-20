---
name: satelle-illegal-states
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Make illegal states unrepresentable. Tighten the type or the boundary instead of guarding a state that should not exist; parse input once where it enters and trust it inside; add no interface, key, flag or wrapper that two current uses do not need; before adding a branch to fix a bug, look for one to delete.
---

# Illegal states are unrepresentable

A state that should not exist is made impossible to write down, not checked
for. Plans, code and substrate are held to four rules, and a reviewer names a
change that breaks one.

1. **Remove the state; don't guard it.** No defensive `if`/`else`, defaulted
   zero value, null-coalescing fallback or silent recovery for a state that
   should not exist. Tighten the type, the struct, the enum or the boundary's
   validation so the state cannot be built.
2. **Parse at the boundary, trust the core.** Input is validated once, where it
   enters: a command's flags and arguments, a `.satelle/` file as it loads, a
   ledger event as it is read or pulled, a gRPC or HTTP message, an agent's
   verdict, a child process's output. Inside, code assumes the invariant holds
   and fails fast when a contract is broken: it returns an error that names the
   broken contract, and panics only on a programming error.
3. **No speculative flexibility.** No interface, configuration key, flag,
   generic wrapper or extension point unless at least two concrete uses need it
   today. A test double is not a second use when the real implementation can
   run in the test. satelle's configuration surface (workflows, gates, skills,
   agents) is the product a repo configures
   ([[satelle-mechanism-vs-substrate]]); a new key in it still needs a repo that
   sets it differently today.
4. **Look for a deletion first.** Before a bug fix adds a branch, a state
   variable or an abstraction, check whether deleting a flag, collapsing two
   states into one or removing a layer of indirection fixes it. The fix that
   removes code wins, and the plan says which deletion was considered.

**What is not bloat:** a failure the binary does not control is a real state:
a session that died holding a seat, a lost network, a child that crashed, a
store that moved on. It is handled at the boundary where it arrives, as doctor
and the sync pass do; that is the boundary rule, not a guard.

Operator directive, 2026-11-21. It sharpens *A clean, efficient codebase* in
[[satelle-operator-rulings]]; see also [[satelle-start-small]] and
[[satelle-broken-windows]].
