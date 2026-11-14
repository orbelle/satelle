---
name: satelle-task-validate-before-review
type: check
description: Entry gate for a task run (backlog → in_progress). Accepts an execution whose parent is an existing task item; refuses one without, naming why. Structure only; whether the run did its work is the close gate's job. Deterministic, no agent.
---

# Task run: validate before

A run executes a task: `satelle task run <task> --session <session>` creates the execution with the
task as its parent. This check refuses a run that names no task, or a task that
does not exist, before any work starts.

```sh
set -eu
parent=$(satelle execution get "$SATELLE_ITEM" --json | jq -r '.parent // ""')
if [ -z "$parent" ]; then
  echo "execution $SATELLE_ITEM names no parent task: a run is created by 'satelle task run <task> --session <session>'"
  exit 1
fi
kind=$(satelle task get "$parent" --json 2>/dev/null | jq -r '.kind // ""') || kind=""
if [ "$kind" != "task" ]; then
  echo "execution $SATELLE_ITEM's parent $parent is not an existing task"
  exit 1
fi
echo "runs task $parent"
```
