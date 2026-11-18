---
name: satelle-format-vet-check
type: check
description: Rejects when gofmt reports dirty files under internal, cmd, tests or scripts, go vet fails, the windows cross-build fails, the literal audit finds a Go string literal naming this repo's workflow, or the comment check finds a comment in code. Deterministic, no agent. GitHub runs no tests, so these run here.
---

# Format, vet, cross-build, literal audit and comment check

GitHub runs no tests (operator, 2026-11-06): the checks CI used to run before a
release run locally in this gate instead. It rejects when:

- `gofmt -l` lists a file under internal, cmd, tests or scripts;
- `go vet ./...` fails;
- `GOOS=windows go build ./...` fails (Windows is a supported target);
- `scripts/audit-literals.sh` finds a Go string literal naming a state, gate,
  skill, agent, workflow, category or tag of this repo (no process in Go);
- `scripts/comments.sh check` finds a comment in the repo's Go, shell,
  Makefile, JavaScript or CSS (code carries no comments), naming each file and
  line.

```sh
set -u
dirty=$(gofmt -l internal cmd tests scripts 2>&1) || { echo "gofmt failed:"; printf '%s\n' "$dirty"; exit 1; }
if [ -n "$dirty" ]; then
  echo "gofmt dirty files (run gofmt -s -w on them):"
  printf '%s\n' "$dirty"
  exit 1
fi
out=$(go vet ./... 2>&1) || { echo "go vet failed:"; printf '%s\n' "$out"; exit 1; }
out=$(GOOS=windows go build ./... 2>&1) || { echo "windows cross-build failed:"; printf '%s\n' "$out"; exit 1; }
out=$(sh scripts/audit-literals.sh 2>&1) || { echo "literal audit failed:"; printf '%s\n' "$out"; exit 1; }
out=$(sh scripts/comments.sh check 2>&1) || { echo "comment check failed (remove each comment it names):"; printf '%s\n' "$out"; exit 1; }
echo "gofmt, go vet, windows cross-build, the literal audit and the comment check clean"
```
