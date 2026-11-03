---
name: satelle-format-vet-check
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate on in_progress → integration — rejects if gofmt -l reports dirty files under internal/cmd/tests or go vet ./... fails. Deterministic, zero LLM tokens; self-contained per satelle-reviewer-self-contained.
---

# Format/vet check (integration-entry functional gate)

Rejects entry to `integration` when `gofmt -l` reports files or `go vet ./...` fails.

```check
#!/usr/bin/env bash
set -uo pipefail
# Paths mirror integrate.md / build.md: internal, cmd, tests.
dirty=$(gofmt -l internal cmd tests 2>&1)
gofmt_ec=$?
if [ "$gofmt_ec" -ne 0 ]; then
  echo "gofmt failed (exit $gofmt_ec):"
  printf '%s\n' "$dirty"
  exit 1
fi
if [ -n "$dirty" ]; then
  echo "gofmt dirty files (run gofmt -s -w on offenders):"
  printf '%s\n' "$dirty"
  exit 1
fi
vet_out=$(go vet ./... 2>&1)
vet_ec=$?
if [ "$vet_ec" -ne 0 ]; then
  echo "go vet failed (exit $vet_ec):"
  printf '%s\n' "$vet_out"
  exit 1
fi
echo "gofmt and go vet clean"
```
