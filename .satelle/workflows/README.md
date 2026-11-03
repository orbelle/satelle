# workflows

The lifecycle, as a DERIVED ROUTE in two halves. `done.toml` declares what DONE
means: one table per story category (`*` governs the rest), its obligations in
order, and the park/cancel states. `step.toml` is the step catalogue: each table
is keyed by the OBLIGATION it discharges and declares the `status` an item holds
there, what it `requires`, its `agent`, `skills`, and the `reviewers` gating
ENTRY to it; `[[gate]]` entries are always-on reviewers.

An obligation names a step by that step's TABLE KEY, never by its `status` — a
status is a stage name, and steps deliberately share them. That is the one rule
the format itself does not make obvious.

The binary owns ORDER (a topological sort of the prerequisites) and topology
(cancel, park, backward movement) — they are never authored.

`agents.toml` sits here too: it binds the agent and gate names those files use to
what actually runs them.

Optional on a performing step: `rework = { consult = "<binding>", rounds = N }`
opens a bounded coder↔consultant relay via `satelle story rework`. Absent means
off — a step without the key behaves exactly as today. The gate reviewer still
runs cold and one-shot; the consultant's READY is a signal, never a verdict.

The shipped route is order zero: edit these two files rather than authoring a
lifecycle from scratch. Run `satelle workflow validate` after an edit — it parses
both halves and prints the effective gate/model table, which is the fastest way
to see what your change did. See also `satelle help workflows` and
`satelle story route <id>`.
