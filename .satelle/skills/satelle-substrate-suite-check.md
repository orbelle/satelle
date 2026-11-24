---
name: satelle-substrate-suite-check
type: check
description: Refuses a substrate item's move to done while a test that reads this repo's .satelle fails. Runs the unit suite and the integration suite against the item's tree and names each failing test. Deterministic, no agent.
---

# Substrate suite

A substrate item changes `.satelle/` only, and the tests in this repo read it.
The integration suite under `tests/` drives the repo's own workflows, panels,
skills and principles. Unit packages such as `internal/cli`,
`internal/kernel`, `internal/remotedef` and `internal/remotenode` read
`../../.satelle`. A change that leaves any of them failing must not reach
main. `grep -rln '\.satelle' --include=*_test.go internal tests` lists the
test files that name the substrate. The set grows with the code, so this check
runs both whole suites rather than a list that would go stale.

It sits last in the first, deterministic group of the substrate workflow's
`in_progress -> done` move (`satelle workflow show substrate --session
<session>`). A reject there stops the move before any judged gate runs.

The notes name each failing test (`--- FAIL` lines) and each failing
package. A build failure is reported with its compiler output.

```check
#!/usr/bin/env bash
set -uo pipefail
command -v go >/dev/null 2>&1 || { echo "go is not installed, so the tests that read .satelle cannot run"; exit 1; }
fail=0
failing() {
  printf '%s\n' "$1" | grep -E '^\s*--- FAIL|^FAIL|^panic:|cannot|undefined|\.go:[0-9]+:[0-9]+:' | head -60
}
unit=$(go test -count=1 ./... 2>&1)
if [ $? -ne 0 ]; then
  echo "the unit suite fails (go test -count=1 ./...):"
  failing "$unit"
  fail=1
fi
integ=$(go test -tags integration -count=1 -timeout 30m ./tests/... 2>&1)
if [ $? -ne 0 ]; then
  echo "the integration suite fails (go test -tags integration -count=1 ./tests/...):"
  failing "$integ"
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  echo "a test that reads this repo's .satelle fails on this tree: fix the substrate change, or raise a delivery item for a test that must change with it"
  exit 1
fi
echo "the unit suite and the integration suite pass on this tree"
```
