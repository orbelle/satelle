# tasks

Authored task HEADERS (`tsk_*.md`, `type: task`): re-runnable work-definitions
that declare an ACTION and how success is VERIFIED. The file is the source of
truth; the DB indexes it. Each RUN is an execution under a per-task folder
`<tsk_id>/exe_*.md`; create one with `satelle execution create --parent <tsk_id>`.
Dispose of a superseded header with `satelle task archive <tsk_id>`: it marks the
record archived (dropped from the default `task list`, still readable via `task
get`) and MOVES the header + its executions to `.satelle/backups/tasks/<ts>/<id>/`
— archive is record disposition, distinct from workflow status.
