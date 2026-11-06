# Projects and workspaces

A **project** is a repo or directory. This machine's projects are listed in
`~/.satelle/config.toml` `[projects] paths` and managed with
`satelle project add|remove|list`; satelled shows them on its Projects page. A
project is bound to at most one satelle.dev project
(`satelle project bind|create|show`), recorded on this machine only.

A **workspace** is a collection of projects on satelle.dev, personal or team.
`satelle workspace list` lists yours; `satelle project create --workspace <id>`
creates a project in one.

satelled keeps one partition per project it has been sent:
`satelle project partitions` lists them and `satelle project prune <repo_key>`
drops one whose project is gone.
