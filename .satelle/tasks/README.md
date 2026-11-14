# tasks

Task items are re-runnable work-definitions that declare an ACTION and how
success is VERIFIED; they live on the ledger (`satelle task get <id> --session <session>`). Each
RUN is an execution item whose parent is the task; start one with
`satelle task run <id> --session <session>` and list a task's runs with
`satelle execution list --parent <id> --session <session>`. A superseded task is closed through its
own workflow (`satelle task set <id> --status <terminal state> --session <session>`), never deleted.
