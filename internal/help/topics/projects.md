# Projects and workspaces

A **project** is a repo or directory. This machine's projects are listed in
`~/.satelle/config.toml` `[projects] paths` and managed with
`satelle project add|remove|list`; satelled shows them on its Projects page. A
project is bound to at most one satelle.dev project
(`satelle project bind|create|show`), recorded on this machine only.

A **workspace** is a collection of projects on satelle.dev, personal or team.
`satelle workspace list` lists yours; `satelle project create --workspace <id>`
creates a project in one.

A satelle.dev project can be bound to a GitHub repository by installing the
satelle GitHub app on it (on satelle.dev). `satelle github review --pr N --sha
S --event E [--body-file F|-]` then publishes a pull-request review on that
repository as the app: the repository comes from the binding, and the event
and body are passed through unchanged. A project with no binding is refused
(FAILED_PRECONDITION).

The app that reviews this repo's pull requests is the satelle GitHub app (app
5379260, satelle[bot]). The approval check matches that account, satelle[bot]
(id 344745317); reviews by orbelle-reviewer, an internal app, never count.

satelled keeps one partition per project it has been sent:
`satelle project partitions` lists them and `satelle project prune <repo_key>`
drops one whose project is gone. The automatic sync pass removes a project
whose path no longer exists from `[projects] paths` and drops its satelled
partition; `satelle project add <path>` registers it again.
