---
name: satelle-integration-check
type: check
description: The full local suite, once, on the edge to in_review (integration → in_review, for a story and a chore alike): cross-builds each package release.yml publishes for each of its targets (`scripts/release-targets.sh build`), runs `make test` and asserts the installed binary and live service are byte-identical before and after. GitHub runs no Linux tests (macOS and Windows run there on demand, os-test.yml); each head is tested here, and the release tests the combined candidate of its items once more. Deterministic, self-contained.
---

# Integration check (the full suite, once, before in_review)

Downloads the module graph with `go mod download`, then runs `make test`, the full hermetic suite (unit and integration), on the head
that becomes the pull request's head, before the PR check and failing fast.
Each head is tested here; a release run then builds one candidate from its
items (fast-forwarding or merging their heads in queue order) and runs
`make test` once more on that combined tree, so main only ever fast-forwards
to a tree the release tested (operator, 2026-11-06; sto_4d68812e). It asserts
the installed binary and the live service are unchanged before and after.
Verifies; installs and restarts nothing.

Before the suite it cross-builds every package of the matrix in
`.github/workflows/release.yml` for every GOOS/GOARCH there, with that
file's CGO_ENABLED (`scripts/release-targets.sh build .`), and rejects
naming each target that does not build and its compiler error: the suite
runs on linux only, and a head that never compiled on darwin or windows
would otherwise reach publish (sto_8094569e; release exe_d26ff28f).

The download runs before the hermetic wrapper, with the check's own
environment, so on a node whose only route out is its egress proxy the
modules come through that proxy; the wrapper (`scripts/hermetic.sh`) does not
pass proxy variables through, and the suite reads the module cache the
download filled.

```check
#!/usr/bin/env bash
set -uo pipefail

installed=$(command -v satelle || true)
bin_before=""
if [ -n "$installed" ]; then
  bin_before=$(stat -c '%d:%i:%s:%Y' "$installed" 2>/dev/null || true)
fi
svc_before=""
svc_pid=$(pgrep -f '^[^ ]*(satelle-serve|satelled) --addr' 2>/dev/null | head -1 || true)
if [ -n "$svc_pid" ]; then
  svc_before="$svc_pid:$(readlink -f /proc/$svc_pid/exe 2>/dev/null || true)"
fi

if [ -f /.dockerenv ] && ! bwrap --dev-bind / / true >/dev/null 2>&1; then
  export SATELLE_HERMETIC_ALLOW_SNAPSHOT=1
fi
if ! go mod download; then
  echo "OBJECTIVE 2 FAILED (module download): go mod download exited non-zero before make test"
  exit 1
fi
if ! bash scripts/release-targets.sh build .; then
  echo "OBJECTIVE 2 FAILED (release targets): a binary release.yml publishes does not build for a target above"
  exit 1
fi
make test
suite_ec=$?

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
