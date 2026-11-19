---
name: satelle-build-unit-check
type: check
description: Functional-check gate on entry to integration: `go build ./...` then `go test ./...`. Rejects naming which failed, with the compiler or test output. Formatting is satelle-format-vet-check's job. Deterministic, zero LLM tokens, self-contained.
---

# Build + unit check (objective 1)

**Functional-check** gate on entry to `integration` (`on="integration"`), so a
slice cannot reach integration with a broken build or a failing unit suite.

Formatting is satelle-format-vet-check's; the integration suite is satelle-integration-check's. Installs and restarts nothing.

```check
#!/usr/bin/env bash
set -uo pipefail

build_out=$(go build ./... 2>&1)
if [ $? -ne 0 ]; then
  echo "OBJECTIVE 1 FAILED (build): go build ./... does not compile"
  printf '%s\n' "$build_out"
  exit 1
fi
vet_build=$(go vet -tags integration ./tests/... 2>&1)
if [ $? -ne 0 ]; then
  echo "OBJECTIVE 1 FAILED (build): the integration-tagged tests do not compile"
  echo "(they are not RUN here — that is objective 2 — but they must build)"
  printf '%s\n' "$vet_build"
  exit 1
fi

test_out=$(go test ./... 2>&1)
if [ $? -ne 0 ]; then
  echo "OBJECTIVE 1 FAILED (unit tests): go test ./... is not green"
  printf '%s\n' "$test_out" | grep -vE '^ok |no test files'
  exit 1
fi

echo "objective 1 PASS: go build ./... compiles (incl. integration-tagged tests) and go test ./... is green"
```
