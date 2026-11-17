# Projects and workspaces

A **project** is a repo or directory. This machine's projects are listed in
`~/.satelle/config.toml` `[projects] paths` and managed with
`satelle project add|remove|list --session <session>`; satelled shows them on its Projects page. A
project is bound to at most one satelle.dev project
(`satelle project bind|create|show --session <session>`), recorded on this machine only.

A **workspace** is a collection of projects on satelle.dev, personal or team.
`satelle workspace list --session <session>` lists yours; `satelle project create --workspace <id> --session <session>`
creates a project in one.

A satelle.dev project can be bound to a GitHub repository by installing the
satelle GitHub app on it (on satelle.dev). `satelle github review --pr N --sha
S --event E [--body-file F|-] --session <session>` then publishes a pull-request review on that
repository as the app: the repository comes from the binding, and the event
and body are passed through unchanged. A project with no binding is refused
(FAILED_PRECONDITION).

The app that reviews this repo's pull requests is the satelle GitHub app (app
5379260, satelle[bot]). The approval check matches that account, satelle[bot]
(id 344745317); reviews by orbelle-reviewer, an internal app, never count.

satelled's Projects page lists this machine's projects: one row per
`[projects] paths` entry, with its counts once the project has pushed.

A repo pushes its partition to satelled when a satelle command there writes
its ledger, when `satelle project add --session <session>` seeds it, and when the automatic
sync pass confirms an added project; a command that writes nothing pushes
nothing. A repo under the temp dir (the system temp dir and `/tmp`) pushes
nothing, added or not.

satelled keeps a partition of a repo that is not one of this machine's
projects, and does not list it. `satelle project partitions --session <session>` lists every
partition and marks those `not-added`; `satelle project prune <repo_key> --session <session>` drops
one. The automatic sync pass removes a project whose path no longer exists
from `[projects] paths` and drops its satelled partition; `satelle project
add <path> --session <session>` registers it again.
