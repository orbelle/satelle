---
name: satelle-task-validate-before-review
scope: system
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: CODED entry gate for a task EXECUTION (backlog → in_progress): self-contained functional check judging STRUCTURE only — the run names a parent task header that exists and is structurally valid. Completion is the close gate's job (satelle-task-validate-after-review). Pushes back when a run isn't ready to begin.
---

# Task execution — validate-before (coded begin-run gate)

**Coded** entry gate on `backlog → in_progress` for a task **execution** — one
isolated RUN of a task (the header is a stable authored work-definition; the
run is the item transitioning). The check below IS the decision —
deterministic shell, no LLM (see [[satelle-agent-model]]: mechanism runs, the
gate decides; per [[satelle-constitution]]). Receives `{story, from, to}` on
stdin; `story` is the execution item carrying `parent_id` — the `tsk_` header
it runs.

Judges structure only, mirroring `structure.CheckTask` (`satelle task validate`):

- the execution names a parent task (`parent_id`), and that header file exists
 under `.satelle/tasks/` (or, for a default task the binary ships, resolves
 from the binary with no file);
- the header is structurally valid: YAML frontmatter with `id`, `type: task`
 (OKF), `status`, and a `# Title` heading in the body.

NOT judged: whether the task's items were completed (the close gate,
`satelle-task-validate-after-review`), or the richness of the work-definition
prose. A task may update code/files; shipping those changes is the operator's
business, not this workflow's.

```check
# Coded structural entry gate. Reads {story, from, to} on stdin; exit 0
# accepts, non-zero rejects with the reason on stdout.
IN=$(cat)
rest=${IN##*\"to\":\"}; to=${rest%%\"*}
[ "$to" = "in_progress" ] || exit 0
case "$IN" in
 *'"parent_id":"tsk_'*) ;;
 *) echo "execution names no parent task header — create the run with: satelle execution create --parent <tsk_id> ..."; exit 1;;
esac
rest=${IN#*\"parent_id\":\"}; parent=${rest%%\"*}
F=".satelle/tasks/$parent.md"
if [ ! -f "$F" ]; then
 # A default task is virtual: it has no header file, and the binary validates the
 # copy it ships. A header that went missing is still refused.
 satelle task validate "$parent" 2>/dev/null | grep -q "^PASS  tasks/$parent (embedded default)" && exit 0
 echo "parent task header $F does not exist — a run executes an authored task"; exit 1
fi
head -1 "$F" | grep -q '^---$' || { echo "$F: missing YAML frontmatter"; exit 1; }
grep -q '^id:' "$F" || { echo "$F: frontmatter missing id"; exit 1; }
grep -Eq '^type:[[:space:]]*task[[:space:]]*$' "$F" || { echo "$F: frontmatter must have 'type: task' (OKF)"; exit 1; }
grep -q '^status:' "$F" || { echo "$F: frontmatter missing status"; exit 1; }
grep -q '^# ' "$F" || { echo "$F: body missing a '# Title' heading"; exit 1; }
exit 0
```
