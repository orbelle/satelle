---
name: satelle-integration-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
check_reuse: tree
description: The full local suite, once, on the edge to approved (integration → approved, for a story and a chore alike): runs `make test` and asserts the installed binary and live service are byte-identical before and after. GitHub runs no tests; each head is tested here, and the release tests the combined candidate of its items once more. Deterministic, self-contained.
---

# Integration check (the full suite, once, before approved)

Runs `make test`, the full hermetic suite (unit and integration), on the head
that becomes the pull request's head, before the PR check and failing fast.
Each head is tested here; a release run then builds one candidate from its
items (fast-forwarding or merging their heads in queue order) and runs
`make test` once more on that combined tree, so main only ever fast-forwards
to a tree the release tested (operator, 2026-11-06; sto_4d68812e). It asserts
the installed binary and the live service are unchanged before and after.
Verifies; installs and restarts nothing.

```check
#!/usr/bin/env bash
set -uo pipefail

# --- Before: fingerprint the operator's environment (read-only) -------------
installed=$(command -v satelle || true)
bin_before=""
if [ -n "$installed" ]; then
  # device+inode+size+mtime — changes if the binary is replaced in place OR swapped.
  bin_before=$(stat -c '%d:%i:%s:%Y' "$installed" 2>/dev/null || true)
fi
# The live service process: pid plus the exe it is running. Reading /proc is
# free and needs no systemctl, so an unreachable bus cannot skew the check.
# Anchor to argv0: the check runs as a bash -c script whose own command line
# holds this pattern, so an unanchored pgrep -f can select the check's subshell.
svc_before=""
svc_pid=$(pgrep -f '^[^ ]*(satelle-serve|satelled) --addr' 2>/dev/null | head -1 || true)
if [ -n "$svc_pid" ]; then
  svc_before="$svc_pid:$(readlink -f /proc/$svc_pid/exe 2>/dev/null || true)"
fi

make test
suite_ec=$?

# --- After: the same fingerprints must be unchanged -------------------------
fail=0
if [ -n "$installed" ]; then
  bin_after=$(stat -c '%d:%i:%s:%Y' "$installed" 2>/dev/null || true)
  if [ "$bin_before" != "$bin_after" ]; then
    echo "OBJECTIVE 2 FAILED (disturbed install): the suite changed the installed binary $installed"
    echo "  before: $bin_before"
    echo "  after:  $bin_after"
    echo "  a test must drive the repo-built binary via SATELLE_BIN, never the installed one"
    fail=1
  fi
fi
svc_after=""
if [ -n "$svc_pid" ]; then
  new_pid=$(pgrep -f '^[^ ]*(satelle-serve|satelled) --addr' 2>/dev/null | head -1 || true)
  if [ -n "$new_pid" ]; then
    svc_after="$new_pid:$(readlink -f /proc/$new_pid/exe 2>/dev/null || true)"
  fi
  if [ "$svc_before" != "$svc_after" ]; then
    echo "OBJECTIVE 2 FAILED (disturbed service): the suite changed the operator's running service"
    echo "  before: $svc_before"
    echo "  after:  ${svc_after:-<no service running>}"
    echo "  a hermetic test must set SATELLE_HOME to a temp dir and never seed or signal a real serve"
    fail=1
  fi
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi

if [ "$suite_ec" -ne 0 ]; then
  echo "OBJECTIVE 2 FAILED (full suite): make test exited $suite_ec"
  exit "$suite_ec"
fi
echo "objective 2 PASS: full suite green; installed binary and live service unchanged"
```
