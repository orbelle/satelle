# tasks

Task items are re-runnable work-definitions that declare an ACTION and how
success is VERIFIED; they live on the ledger (`satelle task get <id>`). Each
RUN is an execution item whose parent is the task; start one with
`satelle task run <id>` and list a task's runs with
`satelle execution list --parent <id>`. A superseded task is closed through its
own workflow (`satelle task set <id> --status <terminal state>`), never deleted.
