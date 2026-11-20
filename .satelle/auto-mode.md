This repository is governed by satelle, a local workflow CLI whose configuration lives under .satelle/ and whose runtime ledger and cache live under ~/.satelle/.
Running satelle CLI commands with --session for status, validation, doctor, workflow inspection, help, item management, seats, gates, queues, remotes, and GitHub status checks is routine local workspace work.
Reading and writing files inside this repository working tree and temporary files in the system temp directory for command inputs or attachments is routine local work.
Running local Go build, format, vet, and test commands (go test, go vet, gofmt, scripts/comments.sh) is routine in this repository.
Running git commands for status, diffs, logs, branches, worktrees, commits, and pushing story branches, and running gh or satelle github commands for pull requests and commit statuses, is part of this repository's delivery workflow.
