# Subtractive audit

A task the operator asks for, on demand, against one target they name: a Go
package, a directory, a set of files or a part of `.satelle/`. The run reads
the target through the lens of essential against accidental complexity and
proposes stories that delete, merge or flatten what the target inflicted on
itself, without losing a behaviour the domain needs. It finds no missing
feature and proposes no defensive edge-case handling. Its rubric is
[[satelle-illegal-states]]. Runs land in the runbook workflow
(`satelle workflow show runbook --session <session>`); the session performs
them.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Audit a target for accidental complexity") | .id'
```

When it prints nothing, create it once, from the main checkout:

```sh
session=<session>
satelle task create --title "Audit a target for accidental complexity" --body - --session "$session" <<'EOF'
The operator names one target (a Go package, a directory, a set of files or a part of .satelle/); the run proposes stories that remove the target's accidental complexity (.satelle/tasks/subtractive-audit.md).

ACTION: read the run's audit-target attachment and the target it names, read-only, against .satelle/principles/satelle-illegal-states.md. Inspect it for (1) dead or speculative abstractions: an interface with one implementation, a pass-through wrapper, a helper that adds indirection without isolating real change; (2) defensive bloat and unreachable states: a guard, fallback or recovery path for a state an upstream type or caller already prevents; (3) configuration and flag explosion: an option, flag or boolean parameter no caller varies, or that only multiplies paths; (4) fragmented state: a variable derivable from existing data, or a multi-step state machine that collapses into one transition or a flat pipeline. Confirm each finding by its callers and tests (rg, go test) before proposing it. Attach audit-report: the proposals in priority order (largest safe reduction first), each with Title (starting Delete, Collapse, Inline or Remove), Target Files/Symbols (paths and functions), Accidental Complexity Identified (why the code exists and why the domain does not need it), Proposed Subtractive Change (the steps, naming what is deleted) and Net Reduction & Risk (estimated lines and branches removed, and the invariant or test that proves no regression). File no story and change no file.
VERIFICATION: attachments task, audit-target and audit-report are on the run. Every audit-report proposal carries the five fields, its title starts with Delete, Collapse, Inline or Remove, and its target names a path that exists. No proposal adds an abstraction, framework, flag or defensive check. A target with no accidental complexity has an audit-report that says so and names what was read.
EOF
```

## Run it

`<target>` is what the operator named. Start the run, record the target and
the task on it, and perform it:

```sh
session=<session>
task=<the task id found above>
exe=$(satelle task run "$task" --json --session "$session" | jq -r '.id // empty')
[ -n "$exe" ] || { echo "could not start a run of $task" >&2; exit 1; }
printf '%s\n' "<target>" | satelle execution attach "$exe" --name audit-target --type text/plain --file - --session "$session"
satelle task get "$task" --session "$session" | satelle execution attach "$exe" --name task --type text/plain --file - --session "$session"
satelle execution set "$exe" --status in_progress --session "$session"
satelle execution seat "$exe" --session "$session"
```

Do the ACTION, attach the report, and present the run done; its close gate
judges the report against the VERIFICATION:

```sh
session=<session>
exe=<the run>
satelle execution attach "$exe" --name audit-report --type text/markdown --file <the report> --session "$session"
satelle execution set "$exe" --status done --session "$session"
```

## After the run

Show the operator the audit-report. The run files nothing: a proposal becomes
a story only when the operator picks it ([[satelle-story-shape]]). File each
picked proposal as a story in that shape, in the category
[[satelle-chore-category]] gives it, with the shared tags
[[satelle-group-related-stories]] asks for, and its `order:<N>` from the
report's priority.
