---
name: satelle-diagnostic-discipline
type: principle
tags: [type:principle]
applies_to: ["*"]
description: When something behaves oddly, inspect running state before measuring change over time; use one wake signal per event; scope verification to the change.
---

# Diagnostic discipline

When something behaves oddly (state vanishing, a transition stuck, costs wrong,
a test flapping) and you can still act:

1. **Inspect running state first.** Look at what holds the database, ports or
   leases, the live service status, and leaked children of earlier sessions,
   before instrumenting how something changes over time.
2. **One wake signal per event.** When the work already reports completion (a
   command exit, a gate verdict, a CI result), arm no second poller or timer for
   it.
3. **Scope verification to the change.** Re-run the smallest check that answers
   the question; run the full suite only when the change is broad or the fault
   is unlocalised.

When the process itself forbids progress, that is a blockage, not a diagnosis
problem: park per [[satelle-recognise-blockage]].
