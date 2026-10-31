## [0.0.826] - 2026-11-01

### Changed
- Every move of a story into a performing state appends a `working_on` presence event. `satelle story get` and session start show another location's latest presence as advisory, and it never refuses an action. When the events this checkout has not seen include a status transition by another location, a push no longer rebases past it: the chain's base check refuses the stale append, and the refusal names that location's latest `working_on`. Events that change no status are still rebased. A task appends a result to a story's chain from any session with no seat check, refused only by a stale base. The engagement seat is unchanged; this is slice A of its removal (sty_9c782f0e; slices B–D: sty_c945a68a, sty_194d5a59, sty_2d8ecad5). (sty_9c782f0e)

## [0.0.825] - 2026-11-01

### Added
- `~/.satelle/agents.toml` can map a named seat to a named profile machine-wide, in a `[seats]` table. A repo then needs no `agents.local.toml` to pick its seats' models. The map layers above the repo's `agents.toml` and below `agents.local.toml`, never creates a seat the repo does not have, and leaves the repo file free of `profile=`, so sync push is unaffected. `satelle agent validate` shows each mapped seat's command, model, tools and timeouts on its GRANT line, labelled as coming from the catalog, and the catalog listing shows `SEAT [x] -> profile`. (sty_1f496a91)

## [serve-v0.0.263] - 2026-11-01

### Changed
- The service binary resolves the catalog seat map. (sty_1f496a91)

## [0.0.824] - 2026-11-01

### Changed
- `satelle substrate review` judges each override against its default, the code and the repo's own baseline, and dispatches by manifest: each area's batch carries the instruction and file paths, never item text, and is sized to the judging seat's budget (prompt plus payload). The judge reads each item, its embedded default and its history for itself. With no `--binding`, the driving agent's own adapter judges in the loop; `--binding <seat>` picks a seat. Each item gets remove, reduce (with the replacement text), keep (only for what is repo-specific) or conflict (naming the code it contradicts). Tasks are reviewed too. An item the judge leaves out reads "not judged", and a batch whose answer does not parse keeps its raw answer beside the report while the other batches still report. `--apply` carries the replacement and evidence into the story it raises. (sty_b8ae7a1e)
- `satelle review score` scores a case's expected dispositions as well as its verdict. Seven substrate-review replay cases (one per rule, plus a mixed case) fail a judge that keeps everything; Gemini and Opus each score 14/14. (sty_b8ae7a1e)

## [serve-v0.0.262] - 2026-11-01

### Changed
- The service binary carries the manifest-dispatched substrate review and disposition scoring. (sty_b8ae7a1e)

## [0.0.823] - 2026-11-01

### Breaking
- **`[sync] recent_window` is removed.** A team-scope local store now holds a story's full history while the story is in a performing state of its governing route, whichever location works it, and otherwise keeps the story's row alone; opening the story (`satelle story get`, `satelle story route`, the web story page) fetches its history, and the next trim drops it again unless the story is then performing. Personal and local scope keep every story's history and trim nothing. A `satelle.toml` or `satelle.local.toml` that carries `recent_window` fails to load, naming the key and `satelle init`, and a settings push withholds it.
- init-heals: `satelle init` (or `satelle init --all --yes` for every registered repo) removes `[sync] recent_window` from `satelle.toml` and `satelle.local.toml` and leaves every other line as it was. (sty_7c01ca14)

## [serve-v0.0.261] - 2026-11-01

### Changed
- The service binary reads no [sync] recent_window and holds team-scope history by the governing route's performing states. (sty_7c01ca14)

## [0.0.822] - 2026-11-01

### Changed
- The session-resident `satelle-agent-goals` principle states that a block is never worked around. When a gate, a hook deny, the edit gate, the cross-repo fence or a harness refusal blocks an action, the agent does not do the blocked work another way, and does not hand the user a command that does it. It raises the work as a story in the repo the work belongs to, in the category whose route fits. When no category or workflow fits, it proposes one and creates it only with the user's approval. `satelle init` heals the seeded copy. A create-rubric review-corpus pair covers the rule. To keep every session principle inline under the harness limit, rules that restated another session principle or help topic were cut: the edits-require-a-story "Follow the workflow" paragraph (agent-goals already says it), the agent-goals examples of what is not proof of done, its second statement that a gate needs a reviewer accept, and the clauses on a workflow with no blocked state and on why a supersedes tag is used. (sty_902140d6)
- The substrate-lock refusal with no engaged substrate story no longer tells the agent to ask the developer to repair the substrate outside the agent session. It now says to raise a story whose category route can take the path, or to propose such a category to the developer and create it only with their approval. (sty_902140d6)

## [serve-v0.0.260] - 2026-11-01

### Changed
- The service binary carries the revised session principles and substrate-lock refusal text. (sty_902140d6)

## [0.0.821] - 2026-11-01

### Fixed
- The embedded `satelle-story-intent-review` no longer rejects a story for missing acceptance criteria on the edge that authors them. It judges the behaviour card (role, context, outcome). Where the step's planner authors criteria, it judges the plan's `## Acceptance criteria` section instead of the story's own field. It rejects a card with no behaviour, and a plan whose criteria section is missing or untestable. On a route with no plan step the card is the whole spec. `satelle-story-shape` now states this, and five intent-rubric review-corpus cases cover it (both reviewer families score 6/6 with no false blockers). Existing repos pick it up through `satelle init`; a repo that overrides the intent review keeps its own text. (sty_a3a5397a)

## [serve-v0.0.259] - 2026-11-01

### Changed
- The service binary carries the revised embedded intent review and story-shape principle. (sty_a3a5397a)

## [0.0.820] - 2026-11-01

### Added
- An embedded seat-resident principle, `satelle-dispatch-pointers` (`principles:seat`), for the planner, coder and reviewer seats: before designing how work reaches an agent, establish what that agent can already read and run, and send it the instruction and pointers, never content it can fetch. The embedded `plan` skill names what each designed dispatch's seat reads for itself. `satelle-story-plan-review` and `satelle-code-ac-review` reject a plan or slice that embeds readable content in a dispatch, and review-corpus cases cover both rubrics. (sty_73a2dc68)

### Changed
- An isolated dispatch over its seat's budget is refused. Every dispatch (performer, live session, step summariser, review text) measures its system prompt plus payload against the seat's `[validate.injected.budget]` row. Over budget, it is refused with an error naming the seat, the step and both sizes, and nothing is truncated. A seat with no row is not measured. No migration is needed where the dispatches fit; this repo's budget rows were measured to fit. If one is refused, send the content as a pointer, or raise or remove that seat's row in `satelle.toml` `[validate.injected.budget]`. (sty_73a2dc68)
- Local performers and live sessions receive the story's documents as scratch-file paths instead of inline bodies. A coder or planner payload on a large story drops from about 77 KB to about 10 KB. The principle condensing leaves room for the payload instead of filling the whole budget. (sty_73a2dc68)

## [serve-v0.0.258] - 2026-11-01

### Changed
- The service binary carries the dispatch budget backstop and the documents-by-path payload. (sty_73a2dc68)

## [0.0.819] - 2026-11-01

### Fixed
- `satelle init`, and `satelle init --all --yes` for every registered repo, repairs a seat whose `agents.local.toml` or `agents.workspace.toml` restates its tools in another adapter's names than the one its profile resolves to (for example pi's `read,grep,find,ls` under a Claude profile, which left the seat with no context channel and failed `node.alloc`). It drops the line when the profile already grants the same tools, and otherwise restates each tool in the resolved adapter's names. Every other line, comment included, stays as written. A tool with no counterpart there is left unchanged and reported with the repo, file, seat and name. The `init --all` dry run lists each rewrite before `--yes` applies it. (sty_f7f80c48)

## [serve-v0.0.257] - 2026-11-01

### Changed
- The service binary carries the shared adapter tool vocabulary that init's foreign-tool repair and doctor's agents.binding warning read. (sty_f7f80c48)

## [0.0.818] - 2026-10-31

### Fixed
- A repo whose `satelle init` fails deployed-system validation on a problem the release's migrations do not touch (for example a seat with no context channel, `node.alloc`) is no longer locked out by an init-heals release. Init applies every migration, stamps `deployed.version` at the binary, and then reports the validation failure as its own FAIL and exits non-zero, naming the stamp so a story can run to fix it. Store-backed commands stop refusing with the stamp marker, a fix story engages, and `satelle doctor` keeps reporting the finding. A migration that itself fails still leaves the stamp as it was. (sty_7ef14c27)

## [0.0.817] - 2026-10-31

### Added
- A shipped `agents-repair` story category gives every repo a gated way to repair a broken agents layer, including a repo whose route declares no `[substrate]` lane. Its route (raised → authored → agents-verified) allocates no planner, coder or LLM reviewer, so a broken binding cannot refuse it; the edit gate's substrate lock admits it for `.satelle/workflows/agents*.toml` alone, and its close (`satelle-agents-layer-only-check`) admits no other path. A story governed by `*` that uses the broken seats is still refused with `node.alloc`. There is still no shipped `[substrate]` lane. (sty_32431a16)

## [serve-v0.0.256] - 2026-10-31

### Changed
- The service binary carries the agents-repair category and route. (sty_32431a16)

## [0.0.816] - 2026-10-31

### Breaking
- **`[sync]` scope keys removed — `all`, `workflows`, `principles`, `skills`, `constitution`, `agents`, `tasks`, `settings`, `documents`, `stories`, `ledger`, `executions`.** Sync scope now comes from the signed-in account and the repo's explicit `[sync] project` binding: an unbound repo or a signed-out machine is local and makes no hosted call, a project of the account's personal team is personal, and a project of a team the account belongs to is team. A project whose team the account does not belong to is local. A repo that synced through the directory-name default is local until `satelle project bind`. `satelle project bind`, a rebind, or joining or leaving the project's team changes the scope on the next sync with no file edit. A `satelle.toml` or `satelle.local.toml` that carries one of the keys fails to load, naming the key. The settings area no longer carries scope: a push withholds the keys, and a rehydrate or deploy that restores `satelle.toml` strips them, so another machine's file never sets this machine's scope. `satelle sync status` shows the resolved scope and where it came from (account, team, project). `satelle doctor` and the session substrate advisory judge from the scope the last sync resolved for the current binding. `*.local.*` files never leave the machine under any scope.
- init-heals: `satelle init` (or `satelle init --all --yes` for every registered repo) removes the keys from `satelle.toml` and `satelle.local.toml` and leaves every other line as it was. (sty_b336c2b0)


## [serve-v0.0.255] - 2026-10-31

### Changed
- The service binary resolves sync scope from the account and the repo binding and reads no per-area [sync] scope key. (sty_b336c2b0)

## [0.0.815] - 2026-10-31

### Added
- `satelle substrate review` gives any repo a review of its substrate, changing no file: every principle, skill, workflow step and gate, and agent seat is listed with its origin and the evidence the repo's story history holds for it — rejections and their reasons, amendments, parks, rework, flaky (no-verdict) reruns, panel dissent, per model — plus every constitution rule. One reviewer dispatch (`--binding`, default `reviewer`) gives each item a disposition (remove, reduce, keep or conflict) and each rule a finding; anything it omits reads "not judged". The report prints as markdown (or `--json`) and is saved outside the tree (`--out`). (sty_08f747ad)
- `satelle substrate review --apply <item>` raises one substrate story per chosen item in the reviewed repo, so every change goes through its substrate lane; it edits nothing and refuses embedded and team items. `--from <report.json>` applies a saved report without judging again. (sty_08f747ad)

## [serve-v0.0.254] - 2026-10-31

### Changed
- The service binary carries the substrate review inventory and help. (sty_08f747ad)

## [0.0.814] - 2026-10-31

### Added
- Two embedded principles make behaviour-driven stories satelle's baseline. `satelle-story-shape`: a story card states one behaviour (role, context, outcome), its acceptance criteria are the technical requirements that meet it, each provable at the gate that judges it, and evidence that exists only after release is an AC of the release step, never of the code step. `satelle-start-small`: a change ships as the smallest slice that proves its behaviour, and scope, concurrency or rules grow only once the ledger shows the smaller form held. (sty_bcd344fd)
- A third principle residency, `principles:seat`: such a principle is injected into every dispatched seat whose principles selector includes the session set (planner, coder, reviewers and every other cold-start agent) and never into the SessionStart context, so it costs nothing against the harness's SessionStart limit. Both new principles are seat-resident. (sty_bcd344fd)
- The embedded `satelle-story-plan-review` and `ready` skills reject a story whose card is not a behaviour, whose ACs are not each traceable to it and provable at their gate, or whose slice is not the smallest that proves it. (sty_bcd344fd)
- `satelle doctor` reports a repo principle or skill that shadows `satelle-story-shape` or `satelle-start-small` by name with a different body, or restates a passage of either. (sty_bcd344fd)

## [serve-v0.0.253] - 2026-10-31

### Changed
- The service binary carries the seat-residency tier and the updated embedded principles, skills and help. (sty_bcd344fd)

## [0.0.813] - 2026-10-31

### Fixed
- Engaging a story is no longer refused by a performer-seat allocation problem on a workflow lane the story never walks. The engage check now validates only the route that governs the story (the category's own `done.toml` table, else `*`), so a substrate story still engages and closes while the `*` lane's planner or coder has no context channel; a story governed by `*` is still refused, binding-level checks still run for every story, and `satelle doctor` and `satelle agent validate` still check every route. (sty_259572f6)

### Added
- `satelle doctor` and `satelle agent validate` warn when a repo's `agents.local.toml` (or workspace layer) restates a seat's `tools` in another adapter's tool names than the adapter its profile resolves to, e.g. pi's `read,grep` on a seat now running the Claude CLI. The warning names the repo, the layer file, the seat, both adapters and the mismatched names; the heal is to restate the tools in the resolved adapter's names or drop the override. (sty_259572f6)

## [serve-v0.0.252] - 2026-10-31

### Changed
- The service binary carries the per-adapter tool vocabulary and the layered agents loader's repo root used by the foreign-tool-name warning. (sty_259572f6)

## [0.0.812] - 2026-10-31

### Fixed
- `satelle init --all` counts a registered repo as stale whenever its deployed stamp is below the running binary, not only when its scaffolding changed, and `--yes` heals it exactly as a per-repo `satelle init` would, including the release's init-heals migration, so no repo is left refusing store-backed commands for a breaking-release stamp. Within `satelle init` the stamp is written before validation and sync push run, so one run no longer warns about the stamp it is replacing. Re-stamping clears a recorded push failure that was the breaking-stamp refusal, and `satelle doctor`'s fix line for such a `sync.failing` now names `satelle init`. When init changes synced substrate in a repo whose sync is not local, it publishes those areas with `satelle sync push` after re-stamping; if that push fails, its output ends with one line naming the changed areas and the push command. (sty_2e629e93)

## [serve-v0.0.251] - 2026-10-31

### Changed
- The service binary carries the sync-state record and health finding changes that let a stamp-refusal push failure be cleared on re-stamp and point at `satelle init`. (sty_2e629e93)

## [0.0.811] - 2026-10-30

### Added
- A machine can be marked pull-only for its bound project: `satelle project pull-only on|off`, or `satelle project bind --pull-only` / `--no-pull-only`, stored as `[sync] pull_only = true` in the repo's machine-local `.satelle/satelle.local.toml` (never synced). On a pull-only machine `satelle sync push`, `satelle sync workstate push`, `satelle sync documents push` refuse with a message naming the setting and how to clear it, and so do the pushes that run through them (the web service's background push, node runs), while pulls still run; `satelle sync status` and `satelle project show` show the machine as pull-only. This is per repo binding; `[hosted] push = false` (0.0.803) still turns push off for every repo on the machine. (sty_07eefabe)

## [serve-v0.0.250] - 2026-10-30

### Changed
- The service binary reads `[sync] pull_only` as a reserved switch, never as a sync area. (sty_07eefabe)

## [0.0.810] - 2026-10-30

### Fixed
- In hosted mode the local web UI lists every story this machine is working, including stories engaged from a story worktree (`satelle story worktree`). Those engagements had recorded no location, so 0.0.808 dropped every in-flight worktree story from "This machine's stories" and from the held set. A worktree engagement now records this machine's location, and existing rows written without one count as this machine's. The page's server links now go to the project page that exists on satelle.dev (`/app/p/<project>`), and the progress, epics and members links are not shown until the server serves those pages. (sty_9ece9394)

## [serve-v0.0.249] - 2026-10-30

### Changed
- The service binary lists worktree-engaged stories in hosted mode and links only to server pages that exist. (sty_9ece9394)

## [0.0.809] - 2026-10-30

### Fixed
- When a repo's deployed stamp is behind a breaking release, the session-start notice you see names that release and `satelle init`, ahead of any sync notice. It no longer reports only "settings unpushed" while the next store-backed command is about to refuse the repo. (sty_34808cbe)

## [0.0.808] - 2026-10-30

### Changed
- Once you are signed in and the repo is bound (an explicit `[sync] project`), the server is the store of record. The local store keeps full history only for the stories this machine has engaged and those changed within `[sync] recent_window` (default `168h`); other stories stay as rows, and their history is fetched from the server when you open one (`satelle story get`, `satelle story route`, the web UI story page). Changes from other machines arrive through the story chain subscription instead of a periodic full pull, and a snapshot pass reads the hosted chain only for held stories. In hosted mode the local web UI lists this machine's stories and links the full story list, progress, epics and members views to the server. Not signed in, or unbound: the full local store and UI are unchanged.
- In hosted mode the web service's repair pass issues no work-state push, and a push sends item rows only for stories with a local append the server has not confirmed. A row whose Apply failed is resent, and a story changed only by pulled events is never sent.

### Fixed
- Work done while the server is unreachable is kept: on reconnect, a story the server moved on since this machine last published is rebased. The server's new events are applied locally and the local ones re-appended after the server's head, with no conflict recorded. A story that moves again during the rebase is reported, as before. (sty_0dc600e1)

## [serve-v0.0.248] - 2026-10-30

### Changed
- The service binary follows each bound repo's story chains live instead of pushing on every repair pass, fetches a story's history from the server for the story page, and in hosted mode lists only this machine's stories with links to the server for the rest. (sty_0dc600e1)

## [0.0.807] - 2026-10-30

### Fixed
- `satelle sync status` and `satelle doctor` report a substrate area as unpushed when one of its files changed since the last push, naming the file and the push to run, instead of reporting it in step because the local base and hosted snapshot numbers match. This holds when the edited file carries a self-consistent `embedded_sha` stamp and the hosted copy is a kept binary default, and doctor keeps reporting an area the last hosted check found unpushed. A base marker naming a snapshot this machine never pushed or pulled is now detected (its files do not match that hosted snapshot) and the area is reported diverged, naming the differing files. `satelle sync push --dry-run` reads areas the same way. (sty_1481ac31)

## [serve-v0.0.247] - 2026-10-30

### Changed
- The service binary carries the work-state sync state's substrate area record, which now lists the files each area's state is about. (sty_1481ac31)

## [0.0.806] - 2026-10-30

### Fixed
- `satelle sync push --dry-run` lists only the files a push would send, matching what `satelle sync status` reports as unpushed. Per area it lists only the paths changed since that area's last push, an area this checkout has never pushed in full, and team files apart. It still contacts no server. (sty_18273486)

## [0.0.805] - 2026-10-30

### Fixed
- No sync pass appends an event to a story's chain. A conflict or overlap that a pull, snapshot, rehydrate or push finds between a story's local and hosted chains is recorded in the work-state sync state only, never as an `overlap`, `workflow_version` or `definition_snapshot` event, so a pull no longer forks the chain it is reconciling and a later push sends nothing a sync pass wrote. After a rehydrate each local chain equals the hosted one event for event. The session-start advisory and `satelle sync status` still report the recorded conflicts, respecting the repo's sync scope. (sty_199ad82b)

## [serve-v0.0.246] - 2026-10-30

### Changed
- The service binary carries the work-state sync state's story conflict record, which now names the local and remote holders and the pass that found it. (sty_199ad82b)

## [0.0.804] - 2026-10-30

### Fixed
- A story's recorded change set leaves out other stories' trunk commits when it is read, not only when a record is written. Each change_record's files are reduced by the files of commits in its `since_sha..head_sha` range that the remote trunk holds and whose subject does not name the story, unless the story's own commits in that range or its uncommitted changes also touched the file. A record written before 0.0.765, such as one from a park that spanned another story's trunk commit, therefore no longer drags that story's files into `satelle story diff --recorded`, `satelle-substrate-only-check` or `satelle-docs-only-check`. No row is rewritten. A record whose range git cannot show is used as recorded and named in `unresolved_records`, the `--recorded` note, and the output of both checks. (sty_5a6c06d3)

## [serve-v0.0.245] - 2026-10-30

### Changed
- The service binary carries the embedded `satelle-substrate-only-check` and `satelle-docs-only-check` skills that name a change record's unresolved range. (sty_5a6c06d3)

## [0.0.803] - 2026-10-30

### Fixed
- The local web service no longer pushes as a side effect of a pull, snapshot, rehydrate or stale-machine align: a store change those make does not trigger the service's hosted push. A push that runs after its cursor was reset sends no event the hosted chain already holds.

### Added
- `[hosted] push = false` in `~/.satelle/config.toml` turns hosted push off for this machine (machine-local, never synced). `satelle sync push` and `satelle sync workstate push` then refuse, naming the setting, and make no hosted write. Pulls still run, and satelled keeps serving the UI without ever running a push. `satelle doctor` reports push off as an informational finding, not a `sync.failing` or `sync.unbacked` FAIL. (sty_a21d9c07)

## [serve-v0.0.244] - 2026-10-30

### Changed
- The service binary's pusher ignores ingest notifications from pull, snapshot, rehydrate and align, and honours `[hosted] push = false`: its pusher and repair loop skip every push while the UI keeps serving. (sty_a21d9c07)

## [0.0.802] - 2026-10-30

### Fixed
- The local web UI's remote status reads `local` for a repo whose work state is local. The repo header no longer shows `push failing` with an older failure's reason and log path, or `pushed` from an older success, and the landing row drops its `Push Failing` tag. A push or snapshot pass now records the work-state scope it resolved in the repo's sync state, including when every area is local, and a push skipped because the work state is local is no longer recorded as a successful hosted push. With work state opted in, the header and landing row show push failing or pushed as before. (sty_3df5f95a)

## [serve-v0.0.243] - 2026-10-30

### Changed
- The service binary records the resolved work-state scope with each background push and snapshot pass, and renders a local repo's remote status as `local`. (sty_3df5f95a)

## [0.0.801] - 2026-10-30

### Fixed
- The session-start story-sync line respects the repo's sync scope. When every work-state area resolves to local (for example `[sync] all = "local"`), it no longer lists hosted story conflicts, and the recorded conflicts are dropped from the work-state sync file, so turning sync back on starts from a fresh check. Stories another location last worked on are still named from the local ledger. While any work-state area still syncs, conflicts are reported as before. (sty_c45cb8d5)

## [0.0.800] - 2026-10-30

### Fixed
- `satelle sync workstate pull` no longer reports a story as stranded, or counts it among the stories that left work with no recorded head, when a commit on the remote trunk branch names the story's id in its subject. A story whose head was rebased or squashed onto trunk, or never recorded, has its code on the remote, and the reconcile report now says so by leaving it out. A story with neither a recorded head nor such a trunk commit is still reported. (sty_e5cba77e)

## [0.0.799] - 2026-10-30

### Fixed
- A work-state push whose stories were all published no longer fails when it cannot check some stories' reachability because their engagement tree was recorded on another machine. The push exits 0 and prints one informational line counting the stories not checked, and `--verbose` (now on bare `satelle sync`, `satelle sync push` and `satelle sync workstate push`) lists them. Doctor no longer reports `FAIL [sync.failing]` after such a push. A push the server refuses still exits non-zero and names the refused story on stdout as well as stderr. (sty_906870a6)

## [0.0.798] - 2026-10-30

### Fixed
- While a step's performer runs, the local web UI's story list shows that step as the story's live step, pulsing as in flight, and the step it came from as completed, instead of leaving the row on the origin step until the performer hands back. The recorded status is unchanged. The row's last activity now follows the performer: on the command transport (pi and cursor seats), CPU-liveness progress from the performer's process tree refreshes the activity time, labelled `working`, so a coder that is writing files no longer reads "last activity 34 mins ago: start". An idle performer still shows its start, and the stall detector is unchanged. (sty_5650fbcc)

## [serve-v0.0.242] - 2026-10-30

### Changed
- The service binary renders a dispatched step as a performing chip from the seat's `dispatch:<step>` activity label. (sty_5650fbcc)

## [0.0.797] - 2026-10-30

### Fixed
- The local web UI shows every story's PROGRESS stages in a repo whose ledger is larger than the mirror export budget. A full push to the mirror now carries each exported story's progress rows (its status transitions and gate verdicts) beside the newest-10,000-row window, so a deep ledger or a burst of newer rows on other stories no longer empties a story's PROGRESS cell. The export stays bounded by the number of stories and their progress rows, not by the total ledger. (sty_434d2fd9)

## [serve-v0.0.241] - 2026-10-30

### Changed
- The service binary carries the ledger's progress-kind set used to build each story's PROGRESS stages from its own rows. (sty_434d2fd9)

## [0.0.796] - 2026-10-30

### Changed
- `satelle project bind` seeds a project that has no substrate. When the hosted project reports source `none` and the caller is its substrate holder, bind publishes the repo's team-owned files as the project's first version, whatever the repo's `[sync]` scope, team-owned files outside the config areas such as `scripts/*.sh` included. When the repo has none, it publishes the embedded defaults the team set owns. The seed passes the same validation as `satelle sync config push`, and a refused seed fails the bind. A project that already has substrate is not reseeded, and the repo takes its project tier. A non-holder, an older server that does not report substrate, or a signed-out user seeds nothing: the bind still succeeds, and its output names the holder or the reason. (sty_6d43eb74)

## [serve-v0.0.240] - 2026-10-30

### Changed
- The service binary embeds the scope-free team-owned file collection used to seed a project. (sty_6d43eb74)

## [0.0.795] - 2026-10-30

### Changed
- In a bound project, only the substrate holder publishes team-owned substrate. satelle reads the holder from the hosted project. A member's `satelle sync config push` with an edited team-owned file exits non-zero, naming the holder and each withheld path, and uploads none of them. A server refusal (403) is reported the same way, naming the holder from its body. A member's bare `satelle sync` withholds the same files with one warning naming the holder and the files, exits 0, and still pushes everything else, documents and work state included. Files outside the team-owned set push as before. A server that does not report a holder is pushed to as before. (sty_40c41d91)

## [0.0.794] - 2026-10-30

### Changed
- At session start and at bare `satelle sync`, a bound repo's team-owned files are written into the tree from the project tier. A tree copy satelle did not write there is first backed up to `<runtime>/backups/team-replaced/<path>` and named, with its backup, in a warning; a file whose backup fails is left as it is. `agents.local.toml` and `satelle.local.toml` are never written, an unbound repo is untouched, and `satelle sync --dry-run` lists what it would write. To make a tree edit the team version instead, run `satelle sync config push && satelle sync config deploy`. (sty_4cc971f7)

## [serve-v0.0.239] - 2026-10-30

### Changed
- The service binary embeds the project tier's written-file record and team file mode. (sty_4cc971f7)

## [0.0.793] - 2026-10-30

### Breaking
- **In a repo bound to a project whose hosted store holds substrate, team-owned paths govern from the project's deployed version, not from the in-tree copy.** The team-owned set is `[team] paths` in `satelle.toml`: globs relative to `.satelle/`, by default `workflows/agents.toml`, `workflows/**` and `skills/**`. `satelle sync config deploy` writes the deployed version's team-owned files to the project tier, `<runtime>/project-tier/`, and every read of workflows, skills, agents and the other authored kinds takes them from there. A repo file a team glob matches no longer governs, including a file that exists only in the tree. Deploy still writes the tree copy, but that copy is ignored. For a team-owned `agents.toml`, the tier file replaces the repo `agents.toml`, `agents.workspace.toml` is ignored, `sync bindings push` refuses to publish it, naming the project tier, and `sync bindings pull` (and bare `satelle sync`) skips writing `agents.workspace.toml`. `agents.local.toml` and `satelle.local.toml` still override the tier, and every settings key other than `[team]` still comes from the repo `satelle.toml`. `[team] paths` can name files outside the config sync areas, such as `scripts/*.sh` and `hooks/*`. Push carries them, and deploy writes them to the tier; files `satelle init` writes, such as `hooks/satelle-hook.sh`, are never team-owned. `satelle team path <rel>` prints where a `.satelle/`-relative path resolves, so substrate runs a team script as `"$(satelle team path scripts/<name>)"`. An unbound repo, and a bound project whose deployed version holds no team-owned file, resolve as before. `satelle init` does not migrate this, because it cannot publish or preserve a local edit:
  - To see what differs, run `satelle doctor`. It reports each tree copy that differs from the tier (`team.tree_differs`), each init-written file a team glob names (`team.init_owned`), and each substrate file that runs a team script by its literal `.satelle/` path (`team.literal_script`).
  - To keep a local agents or settings edit, move the keys into `agents.local.toml` or `satelle.local.toml`.
  - To make a tree edit the team version, run `satelle sync config push && satelle sync config deploy`.
  - To refresh the tier, run `satelle sync config deploy`. (sty_8dedc0a4)

## [serve-v0.0.238] - 2026-10-30

### Changed
- The service binary embeds the project-tier resolution of team-owned workflows, skills and agents. (sty_8dedc0a4)

## [0.0.792] - 2026-10-30

### Fixed
- `satelle story wave` resolves each child's delivery from the performing steps of its route. Since 0.0.790 it also asked the engine for a performer at the always-on step-summary gate node, which judges and has none, and printed that refusal as every child's delivery (`step=gate_satelle-step-summary, error: … role=reviewer`). The node is now passed over; a performing step that really cannot be resolved still reports its error. (sty_b40c1657)

## [0.0.791] - 2026-10-29

### Fixed
- `satelle story archive` accepts an epic or parent waiting at a step that waits on its children (the readiness step), when none of its members is performing, and archives its start-state members with it. Before, only a story at its start state could be archived, so an epic deferred by archiving its children stayed at `ready` with no open member, and the edit gate named it in every refusal as performing with a dropped seat. A waiting container with a performing member is refused, naming that member; any other story outside its start state is refused as before; `satelle story unarchive` returns it at its waiting status. (sty_2f836c56)

## [0.0.790] - 2026-10-29

### Added
- The orchestrator readies an epic first, unprompted, and records each child's delivery choice: it messages the epic's ready performer a line per child (`in-loop`, `local` for a subagent, or `team:<path>`, and why), the performer records the reasons under the readiness step's `delivery_heading` and reports each choice as a `placement:` tag (the step's `placement_tag`), the delivering steps honour that pin, and `satelle story wave <epic>` prints each child's order, lane, step, placement and reason. A value other than those three, `remote` included, is refused. (sty_c931cd90)
- The workflow advisor judges route shape: it tells the single-story, epic-child and epic-parent lanes apart, flags a release on the lane every epic child walks (naming the epic-branch lane, where children commit on `epic/<id>` and the parent merges and releases once) and an epic-parent lane with no readiness step, citing the route file and step. satelle prints one line naming `satelle-workflow-advisor`, and runs nothing, at the end of `satelle init`, of a passing `satelle validate` or `satelle workflow validate`, and when the repo's first epic-parent readiness round is accepted under a readiness step that declares `advise` (the shipped `[ready]` step does). Each optimisation the orchestrator accepts is filed as a substrate story. (sty_36b91fd1)

## [serve-v0.0.237] - 2026-10-29

### Changed
- The service binary embeds the readiness step's placement, delivery and lane keys and the route-shape advisor prompt. (sty_c931cd90, sty_36b91fd1)

## [0.0.789] - 2026-10-29

### Added
- A route can end a story at a waiting step that is neither blocked nor terminal: a step declared with `wait = true` in `step.toml`, such as finished work waiting for its release. A story entering it gives up its engagement seat and its working tree, so another story can engage from that tree, and nothing performs on it there. `satelle story drive` stops at the step and reports the story as waiting (exit 0), not parked. A wait step names no agent, cannot also be the start, a terminal, a container wait or a remote wait, and must have an edge onward other than park or cancel; `satelle validate` refuses each of these by name. Entering the step records a queue entry in the story's own history, naming the story, its branch and its head sha as read from its tree with local git, so it works with no remote. A tree with no branch (a detached HEAD) refuses the move. `satelle story queue` lists the open entries in the order they were recorded (`--state` narrows to one wait step, `--json` for the structured form), and `satelle story queue withdraw <id> --reason "…"` closes an entry and records the reason on the story without changing its status. Merging an entry is left to the release task the repo configures. (sty_b53b1b98)

## [serve-v0.0.236] - 2026-10-29

### Changed
- The service binary embeds wait steps (`wait = true`) and the `queue_enqueued` / `queue_withdrawn` ledger kinds. (sty_b53b1b98)

## [0.0.788] - 2026-10-29

### Fixed
- A story's change set no longer claims main-tree substrate that `satelle init` rewrote during its engagement, or that another engaged story wrote through the edit gate. Every substrate file init creates or changes (including `init --all` heals and `satelle node`) is recorded in one `substrate_seeded` row in that repo's ledger with its pre-write mtime and its content hashes before and after, and each substrate write the edit gate allows under the default substrate lock is recorded as a `substrate_written` row naming the story and path. `satelle story diff --include-substrate` and the transition-time change record leave out a file whose only changes since engagement are init's writes or another story's gate writes, and keep it when this story also changed it, when it was changed outside the gate, or when the substrate lock is turned off. (sty_ce858cb3)

## [serve-v0.0.235] - 2026-10-29

### Changed
- The service binary embeds the `substrate_seeded` and `substrate_written` ledger kinds and the change-set exclusion. (sty_ce858cb3)

## [0.0.787] - 2026-10-29

### Added
- `satelle stats` reports what each story finished in the window cost, beside its throughput and quality figures: fresh input, cache read, cache write and output tokens and the derived [pricing] cost, from the story's whole ledger folded as `satelle story cost` does, broken down by seat (agent) and by model, per repo and in total. For each estimated unit it shows estimate, actual and actual÷estimate per story, and per repo the spread of actual÷estimate (count, median, p90, min, max), counting the stories left out. A seat that recorded no usage, an unpriced model and a story with no estimate read `unavailable` (null with a reason in `--json`), never 0, and an unreadable repo's figures are excluded from the total as its other figures are. (sty_e6f6d0dd)

## [serve-v0.0.234] - 2026-10-29

### Changed
- The service binary embeds the cost and estimate figures of `satelle stats`. (sty_e6f6d0dd)

## [0.0.786] - 2026-10-29

### Added
- Tasks can declare local triggers (`triggers:` schedule or story-count condition); `satelle task trigger run-once|watch` runs them locally. A task file's `triggers:` line holds a comma list of `name=schedule:<duration>` (fires when the trigger has made no execution within that interval) or `name=count:<status>>=<n>` (fires when at least n stories sit at that status and the trigger's previous run has concluded). Each firing creates one execution of the task, tagged `trigger:<task>:<name>`, with the trigger's name and source in its `trigger` and `trigger_source` inputs when the task declares them, and drives it along the task's route in the local process with no placement or remote. `satelle task validate` reports a malformed `triggers:` line. (sty_0f11c304)

## [serve-v0.0.233] - 2026-10-29

### Changed
- The service binary embeds the task trigger fields: a task's `triggers:` line is parsed and stored with the task, and trigger-fired executions carry their `trigger` and `trigger_source` inputs. (sty_0f11c304)

## [0.0.785] - 2026-10-29

### Added
- `satelle stats` reports how good the work is, for the same window and scope as its throughput figures: the share of each gate's verdicts accepted on first presentation, per step and per gate; the mean and maximum plan and integration rejection rounds per story; coder re-dispatches after an exit rejection; reviewer dissents that name no criterion; and work that came back, meaning a story created after another reached done that cites it. Each figure names the stories behind it. (sty_ffe74f1c)
- `satelle story release-record <id>... --commit <sha> --release <id> [--field k=v ...]` records one release against a list of stories, done ones included. Each story gets one `release_record` ledger row with the commit, the release identifier, any extra fields and the recording session. It takes no seat and changes no status, so a release task can run it from any session. A story can also refer back to the story it follows with a tag in a namespace the repo lists in `[tags] back_reference_namespaces` (for example `follows:<id>`); the tag must name a story that exists. (sty_5201507b)

### Fixed
- A story can be closed by id from a session and tree that are not its own, and every gate on the edge still judges the story's own change set. `satelle story set` refuses to move a story out of a terminal state, even along an edge the route declares; new work on a done story is a new story carrying a back-reference tag. (sty_5201507b)
- `satelle agent validate` and `satelle doctor` fail a binding whose command loads a pi extension (`-e <path>`) that does not exist in the repo, naming `satelle agents install pi` as the fix. `satelle agents install pi` keeps the scaffold's `.pi/` out of git. (sty_d15f6713)
- A performer seat bound to a profile passes sync's context-channel check on the profile's grant, so the shared agents.toml needs no placeholder `tools` key and the profile's grant is the one that runs. (sty_50dae450)
- No test registers its temporary repo in the machine's real workspace registry; tests point satelle at a temporary home. (sty_e3a1c1d5)

## [serve-v0.0.232] - 2026-10-29

### Changed
- The service binary embeds the quality figures, release records and the updated agent validation and doctor checks. (sty_ffe74f1c, sty_5201507b, sty_d15f6713, sty_50dae450)

## [0.0.784] - 2026-10-29

### Added
- `satelle stats` reports how much work each repo finished and how long each phase took, per registered repo and in total, for a window set with `--since` (default 7 days). It counts stories and epics that reached a terminal state per day and per week, gives the median and 90th-percentile cycle time for phase 1 (backlog to the step that raises a pull request) and phase 2 (that step to the terminal state), and gives the median time spent in each step. A route marks its PR step with `pr_step = true`; a route that marks none reports phase 2 as not applicable and counts the whole cycle as phase 1. A figure satelle has no rows for is shown as `unavailable` (or `null` with a reason in `--json`), never as 0. The figures come only from recorded status transitions and story records, and no repo is written to. (sty_7a2be7da)

## [serve-v0.0.231] - 2026-10-29

### Changed
- The service binary embeds the throughput computation and the step catalogue's `pr_step` marker. (sty_7a2be7da)

## [0.0.783] - 2026-10-29

### Added
- A dispatch's log records what the agent was given as well as what it did: the system prompt, the payload and the named sections a gate or rework relies on (findings to answer, messages delivered, acceptance criteria, material paths), then its progress and its final answer. A developer can check from the log alone that an agent received what satelle meant to give it. Material satelle already stores in a file is referenced by path, and secrets expanded from `[vars]` are never written. (sty_2b89feab)

### Fixed
- A story worktree carries the gate wiring of every harness the repo uses (for example `.pi/extensions/satelle.ts` as well as `.claude`), so a repo whose seats run on pi can enter in_progress from a worktree instead of being refused with "worktree lacks gate wiring for harness pi". (sty_0dca3223)
- The substrate lock judges what a Bash command writes, not path text anywhere in its arguments. Naming a substrate file in a story body or reading one is no longer refused as an edit, and a write is still caught when it is wrapped in `bash -c`, routed through a variable, or written by a redirection. (sty_ca667036)
- `satelle validate` and `satelle principle validate` no longer fail on a config bundle that satelle generated into a story's attachment directory. The bundle embeds workflow TOML, and a TOML array-of-tables header such as `[[gate]]` was reported as a dangling wikilink. The wikilink scan now skips any attachment whose frontmatter declares `type: config-bundle`, whatever its file name. Every other attachment is still scanned, so a genuine dangling reference in a plan or note still fails. (sty_b48df715)
- `satelle init` does not add a model order naming a harness the repo no longer uses. It keeps a repo's own `[model_order]` and seeds lists only for executables the repo's bindings use, so init no longer re-adds `grok = ["grok-4.7"]` to a repo that removed Grok. (sty_a4b90ede)

## [serve-v0.0.230] - 2026-10-29

### Changed
- The service binary embeds the updated harness and worktree configuration and the substrate and worktree help topics. (sty_0dca3223, sty_ca667036, sty_a4b90ede, sty_2b89feab)

## [0.0.782] - 2026-10-29

### Fixed
- A story whose exit gate rejects is no longer stranded when its performer is a one-shot command seat, such as a pi coder. Re-presenting the step with `satelle story set <id> --status <its current status>` re-dispatches the step's named performer once, with the exit gate's findings to answer and any messages waiting for it in its payload. Before, `satelle story rework` refused a command seat as not live-capable, re-presenting the same status dispatched nothing, and the edit gate rightly kept the driver out, so the only way out was park/resume or rebinding the coder. One owner decides who re-runs after a rejection, for both `satelle story set` and `satelle story drive`; the headless executor's re-run stays with drive. (sty_4470bb38)

## [0.0.781] - 2026-10-29

### Added
- A pi seat can run in pi's RPC mode: bind it with `interface = "pi-rpc"` and satelle drives `pi --mode rpc` over its JSONL event stream instead of waiting for `pi -p` to print and exit. The seat ends as soon as pi settles after its answer: satelle takes the answer, closes pi's stdin and ends the seat's whole process group, even if pi would stay alive. Every turn, message and tool call reaches satelle as a real event, so a stalled model stream is ended by `idle_timeout` like any other seat, not held by CPU liveness until `busy_timeout`. A seat with a turn budget is aborted when it starts a request beyond it and recorded as over budget. Turns, tokens and cost come from the run itself, with no pi session file. The binding is refused at validate and dispatch unless its command keeps the seat cold (`--mode rpc --no-session --no-extensions --no-skills --no-prompt-templates --no-context-files --no-mcp`) and passes the system prompt as a startup flag. Any extension dialog is answered cancelled, so none can hold a seat open. satelle's pi extension treats an RPC run as non-interactive. Existing `pi -p` command bindings are unchanged. (sty_79ce4858)

## [serve-v0.0.229] - 2026-10-29

### Changed
- The service binary embeds the pi-rpc interface's validation and the updated agent-dispatch help topic. (sty_79ce4858)

## [0.0.780] - 2026-10-28

### Added
- A developer is told when the installed cursor-agent differs from the version satelle's Cursor behavior was captured against. A Cursor session start whose `cursor_version` differs carries a line naming both versions. `satelle doctor --live` reports a WARN `live.version_drift` for a cursor binding whose `--version` differs, naming both versions and the resolved binary path. Both list the captured contracts that may no longer hold, each with its basis: stop cap, hook timeout handling, envelope shapes, ACP methods and usage fields. Both name `satelle agent recheck cursor`, which reports each contract as holds, changed or unverifiable against the installed CLI and exits non-zero on any change. No warning is raised when the versions match, nothing uses Cursor, or an adapter has no captured version. The captured version has one owner, which the fixture metadata is tested against. (sty_32c61e68)

### Fixed
- satelle's ACP client never answers a denied permission with an allow option. With no reject option offered, it answers cancelled instead of falling back to the first option. It no longer advertises `fs.readTextFile`, which it does not serve. Cursor's `cursor/ask_question` gets the reply Cursor's protocol defines, and `cursor/create_plan` gets a rejected outcome instead of -32601 "method not found". This holds for every ACP adapter, Cursor and Grok alike. (sty_23e42627)

## [serve-v0.0.228] - 2026-10-28

### Changed
- The service binary embeds the updated agent-dispatch help topic and health checks. (sty_32c61e68, sty_23e42627)

## [0.0.779] - 2026-10-28

### Fixed
- An agent driving an epic gets one account of what readiness does to its children. The satelle-agent-goals principle, `satelle story ready` help, `satelle story route` and `satelle help epic-wave` say that readying an epic is its own judgement of order, parallelism and achievability, changes no child's status, readies each backlog child only when the readiness step leaves `ready_children` on, and that a ready round takes no edge: backlog → plan is taken only by `satelle story set <id> --status plan`. (sty_87403c0e)

## [serve-v0.0.227] - 2026-10-28

### Changed
- The service binary embeds the updated route text and epic-wave help topic. (sty_87403c0e)

## [0.0.778] - 2026-10-28

### Added
- An epic's page in the web UI shows, for every child, what `satelle story wave` reports for it: "startable now", or "held" with its cause, reason and each blocker's id and status. When the verb refuses, the page shows its refusal text instead. The UI snapshot carries the verb's own JSON response, so the page and the CLI cannot disagree. Each child with a dispatch in flight shows the same running indicator as the story list (agent, model, elapsed time, warning). While any child has agent work in flight, the epic's page and its story-list row say so and name how many children are being worked. (sty_a42f4add)
- A story or epic still in backlog shows its readiness work. While `satelle story ready` runs its dispatch, the story's seat is marked in flight with the dispatch's agent detail, so the running indicator shows. The round inserts and releases a seat when the story holds none, marks a settled existing seat in flight and settles it again afterwards, and leaves an already in-flight seat alone. Once rounds are recorded, the story shows a current chip with the readiness gate's accepted and rejected rounds (for example `[plan ✗2]`), as a story past its first transition already does. Before, a backlog story being planned showed nothing. (sty_a42f4add)

## [serve-v0.0.226] - 2026-10-28

### Added
- The service renders the epic wave, the per-child and epic in-flight indicators, and the readiness chip for backlog stories. (sty_a42f4add)

## [0.0.777] - 2026-10-28

### Fixed
- The shipped `ready` skill no longer tells a ready-reviewer that child plans wait for the wave when its route plans them in the same round. That claim now holds only when the step declares `ready_children = false`. Chaining children with `depends-on:` edges is now a named third kind of real dependency, used when the container's own body or criteria state an order, so it no longer conflicts with the "only for a real dependency" rule. The repair is stated once. (sty_2b737ed5)

## [serve-v0.0.225] - 2026-10-28

### Changed
- The service binary embeds the corrected `ready` skill text. (sty_2b737ed5)

## [0.0.776] - 2026-10-28

### Added
- An epic's readiness step can declare `ready_children = false`. With it, `satelle story ready <epic>` runs only the epic's own judgement: dependency order, parallel versus sequential running, and whether each child's intent can be achieved. It writes and reviews no plan for any child. Each child is planned through its own backlog → plan step when `satelle story wave` lets it start, so its plan is written against what its dependencies actually delivered. With the key absent or `true`, epic readiness still readies every backlog child, as before, and the shipped route leaves the key absent. The route parser and `satelle validate` refuse the key on a step that does not declare `waits_on_children`, or with a non-boolean value. The shipped `ready` skill now judges each child's achievability. It repairs order, depends-on edges, a missing child and a needed one-at-a-time run (by chaining children with depends-on tags, never by setting the user's `sequential` key) without asking, and rejects only when no allowed repair makes an intent achievable. `satelle help workflow-convert` and `satelle help epic-wave` document the key. (sty_2b737ed5)

## [serve-v0.0.224] - 2026-10-28

### Changed
- The service binary embeds the route rendering and help topics that name `ready_children`. (sty_2b737ed5)

## [0.0.775] - 2026-10-28

### Fixed
- A story that commits, parks and resumes keeps the commits it made before the park in its change set (`satelle story diff`, `story proof` and every reviewer's diff). Before, the resume re-anchored the change set at the resume head, so a story's own pre-park work dropped out of review. Commits that other stories landed while it was parked still stay out, unless their subject names the story. A trunk merge into the story's branch after resume contributes only that merge's own conflict resolutions, never the files the trunk brought in. The same applies to a never-parked story that merges the trunk. `story diff` reports the engagement baseline and names each excluded park interval in its note. Seen in id4me: a reviewer judged only what main brought in, not the story's 9 files. (sty_666e6110)

## [0.0.774] - 2026-10-28

### Added
- `satelle story wave <epic> --json` is a documented contract a script can read without parsing text. `runnable` lists exactly the children the text form prints as startable. Every held child appears in `omitted` with its `id`, a machine-readable `cause` (`waiting_on_dependency` or `no_route`), its prose `reason`, and for a dependency hold the `waiting_on` blockers with their id, status and cancelled flag. Both `runnable` and `omitted` are always present as arrays, empty when nothing qualifies. The text and JSON forms render the same verb response, so they cannot disagree. A sequential refusal still exits non-zero with nothing on stdout. `satelle help epic-wave` and the command help name the fields. (sty_6d6cf69d)

## [serve-v0.0.223] - 2026-10-28

### Changed
- The service binary embeds the updated `epic-wave` help topic, which documents the wave's JSON fields. (sty_6d6cf69d)

## [0.0.773] - 2026-10-27

### Changed
- The default done review (`satelle-story-done-review`) accepts a finished change only when the behavior stated on the story card holds. For a non-parent story it compares the change to that behavior, and when the behavior does not hold it rejects with a criterion-0 entry whose finding quotes the behavior, even if every numbered criterion is met. For a story with no numbered criteria (a route with no plan step) it judges the story body directly and does not accept for lack of criteria. The numbered criteria are a derivation of the card's behavior, not a second spec (the `satelle-reviewer-verdict` principle says so too).
- A reviewer verdict may record a numbered criterion `unmet`, for a criterion that cannot be met while the behavior on the card holds (for example a proof that could only have been captured earlier). The gate accepts such a verdict only when it also carries a criterion-0 accept entry and every other judged criterion is accepted; the `unmet` entry stays in the recorded criteria and is named in the gate notes ("criteria recorded unmet, behavior holds: AC6"), so it is recorded rather than silently passed, and needs neither a criterion amendment nor a waiver to close. Without a criterion-0 accept, `unmet` fails like `unchecked`. A panel's combine tally counts `unmet` entries separately. Before, a criterion that could not be met could only be closed by amending it. (sty_3b789fa5)

## [serve-v0.0.222] - 2026-10-27

### Changed
- The service binary embeds the updated default done review and reviewer-verdict principle (behavior-first judgment, criteria recorded `unmet`). (sty_3b789fa5)

## [0.0.772] - 2026-10-27

### Changed
- The default plan review (`satelle-story-plan-review`) accepts a plan only when its acceptance criteria and its implementation produce the behavior the story card states. Every criterion must cite the card behavior it proves; a set of criteria narrower than the card is refused, with the uncovered card text quoted; an implementation that drops or contradicts a partial solution or technical decision the card records as context is refused; and the criteria do not replace the card. The default review also judges the criteria named by the payload's `criteria_source` (each traced to the plan's recorded evidence, testable, outcome rather than mechanism, faithful to the card), the proof path and root cause of each criterion, and defers to the reviewer verdict skill in fail-fast mode. Before, the default judged only that each criterion the planner wrote had a plan claim, so criteria narrower than the card were accepted and a repo had to carry its own override of the rubric to get the stricter judgments. A route with no plan step is unchanged: its result is still judged against the card. (sty_97c88302)

## [serve-v0.0.221] - 2026-10-27

### Changed
- The service binary embeds the updated default plan review (criteria judged against the story card's behavior). (sty_97c88302)

## [0.0.771] - 2026-10-27

### Changed
- The default create review (`satelle-story-create-review`) admits a story only when its behavior moves the repo's constitution forward. It judges the story against the constitution delivered in its briefing and the principles that constitution links, and an accepted submission's notes name the constitutional goal or linked principle the behavior advances, and how. A story that advances none is refused even when it contradicts nothing, and the refusal says that no constitutional goal is advanced. A story whose behavior contradicts the constitution, a linked principle, or architecture recorded on the card is refused, and the refusal quotes the rule it works against; a decision absent from the card and the principles is never cited, and a decision recorded on the card is context with the behavior still required beside it. A technical story is judged on the same terms: it is admitted when it names the constitutional goal its system property serves, with no customer scenario or acceptance criteria. The create review is the one gate that makes this judgment; no workflow binds a separate admission reviewer. Before, no default rubric judged whether a story advances the constitution, so a harmless story that served no goal was admitted. (sty_d3898554)

## [serve-v0.0.220] - 2026-10-27

### Changed
- The service binary embeds the updated default create review (constitutional admission of story cards). (sty_d3898554)

## [0.0.770] - 2026-10-27

### Changed
- The default create review (`satelle-story-create-review`) creates a story only when its card states desired behavior that holds whichever implementation produces it, because that behavior is what a later check validates. An accepted submission's notes open with `Behavior:` and a one-sentence restatement of the stated behavior, independent of implementation. A technical decision or partial solution recorded alongside stated behavior stays on the card as context and is not a reject ground. A submission that contains only a solution, with no behavior, is refused, and the refusal names the behavior that is missing. Empty or unnumbered acceptance criteria are still not a reject ground at create. Before, the rubric rejected any submission that required a particular function, type or file, and did not restate a submission as behavior. (sty_37c762f5)

## [serve-v0.0.219] - 2026-10-27

### Changed
- The service binary embeds the updated default create review (behavior-first story cards). (sty_37c762f5)

## [0.0.769] - 2026-10-27

### Fixed
- `satelle story ready <epic>` marks an epic when its readiness round is accepted and every child that has a readiness step is ready. A child on a route with no readiness step (a substrate or docs route) has nothing to ready, so it no longer holds the epic back; it is reported as having no readiness step, not as refused. Before, the epic was reported "children not ready" forever and none of its children could leave backlog. A child whose readiness lookup fails for any other reason still holds the epic back. (sty_4cbe00aa)

## [0.0.768] - 2026-10-27

### Fixed
- An unmoved `git push <remote> <branch>` is attributed to the seat of the worktree that has `<branch>` checked out, so a session holding seats in several worktrees can publish from the main tree without `EnterWorktree`. Before, only an edit or a commit a `cd` or `-C` moved was attributed by the tree it targeted; a push that moved nothing fell back to the tree the hook runs in, which holds no seat, and was refused ("this session holds N seats … none is in the tree this event runs in"). The refspec's source side names the branch (`feat/x`, `feat/x:release`, `+refs/heads/feat/x`); a bare `git push`, `HEAD`, `--all`, `--mirror`, `--tags`, a delete, a branch no worktree has checked out, branches in different worktrees, or a payload that runs any git command other than a push (`git commit … && git push …` commits in the hook's own tree) stay attributed to the hook's own tree and are refused as before. An explicit `cd` or `-C` still decides first. Edits and moved commits were already attributed by target and are now proven from the main tree against real `git worktree add` trees under the default foreign-tree fence; a scratch path outside every repo tree stays allowed while another seat's transition is in flight. (sty_68a4d071)

## [0.0.767] - 2026-10-27

### Added
- `satelle story archive <id>... [--reason <text>]` and `satelle story unarchive <id>...` dispose of stories without running a workflow route, so a backlog is cleared without a cancel review per story. Archive is record disposition, not a status: the story keeps its status and chain, drops out of the default `satelle story list`, the web story board and `satelle story wave`, and `satelle story get <id>` still returns it. Each story gets one `story_updated` event changing `archived` from `false` to `true` (and back on unarchive) with the reason and the actor, and the flag is folded from the chain, so it reaches another machine on a pull and a cleared row is restored by the fold rebuild. Only a story at its workflow's start state may be archived; any other is refused by id and status and one refusal archives nothing. Naming an epic also archives its children at their start state and lists the others as skipped. `satelle story list --archived` lists only archived stories. (sty_28665836)

## [serve-v0.0.218] - 2026-10-27

### Changed
- The service binary folds a story's archived flag from its chain and hides archived stories from the board by default. (sty_28665836)

## [0.0.766] - 2026-10-27

### Fixed
- `satelle sync workstate rechain` discards a local `status_reconcile` event when the status it set is the status the hosted chain folds to, so a story whose row `satelle story reconcile --repair` repaired can still be put on its hosted chain. Before, that event was a local event the hosted chain lacks that is not a seeded genesis, and the story was refused ("local events the hosted chain lacks are not only a seeded genesis: evt_… (status_reconcile)", backelle sty_4be32144) although the event's only effect is a status the hosted fold already reaches; its push stayed refused as moved since its start. The hosted chain is folded with the same route the local fold uses. A `status_reconcile` that set a different status is refused by name with both statuses (`local status_reconcile evt_… sets status in_progress but the hosted chain folds to done`) and the chain is left unchanged; one that repaired no status is still local work. `--dry-run` reports the same outcome as the real run, and the report line counts the reconcile events removed beside the seeded ones. (sty_7f977031)

## [0.0.765] - 2026-10-27

### Fixed
- A story's change set no longer counts the commits other stories landed on the trunk while it ran. When a tree fast-forwards over another story's merge, `satelle story diff` (live and recorded), the substrate-only check, the scope review and every other gate that reads the change set listed that merge's files as the story's, so a story that changed only the gitignored `.satelle/` was refused for `.github/workflows/ci.yml`, `Makefile`, `.version` and the rest of another story's pull request (id4me sty_d9904629). A commit between the engagement point and HEAD is now left out when the remote trunk already holds it and its subject does not name the story; the story's own commits, a commit that names it even if published, and its uncommitted and untracked changes stay. Without a remote trunk ref, nothing is left out, as before. Change records written before this release are not rewritten. (sty_659c4133)

## [0.0.764] - 2026-10-27

### Fixed
- A status transition whose `from` is not the story's status at its place in the chain is skipped only when the story is already done or cancelled at that place; anywhere else it applies, as it did before 0.0.763. 0.0.763 skipped every such transition, so a shipped story whose legacy chain was linked once in insertion order (its transitions out of time order, sty_df21b2db) stopped folding at the first mismatch and read `in_progress` although it was done, and `satelle story reconcile --repair` would have moved its row back. The transition is still reported either way: `satelle ledger verify` lists it as a `stale_from` break, and `satelle story reconcile` says whether the fold skipped it or applied it out of order. The sty_41f61669 shape, a stale transition after `done`, still stays done. (sty_5c9b295d)

## [serve-v0.0.217] - 2026-10-27

### Changed
- The service binary carries the corrected fold: an out-of-order transition is skipped only at a final state, so a legacy chain folds to the status its transitions reached. (sty_5c9b295d)

## [0.0.763] - 2026-10-26

### Fixed
- A status transition whose `from` is not the story's status at its place in the chain no longer sets the story's status. Before, an earlier attempt's events that reached a machine after the story was done (they arrive without a base and are linked to the tail) put a stale `plan → in_progress` after `done`, the fold took it, and the story reopened silently (sty_41f61669 read `in_progress` although shipped). Such a transition is now skipped by the fold and reported by `satelle ledger verify` as a stale transition; `satelle story reconcile` reports a row it disagrees with and `--repair` rebuilds that row from the corrected fold. A pull, plain or `--force`, that moves a done or cancelled story's row to a non-terminal status now names the story, both statuses and the event, and `--force` no longer writes a story's status around its chain. To heal a story already reopened this way, run `satelle story reconcile --repair`. (sty_6f952dd0)

## [0.0.762] - 2026-10-26

### Fixed
- `satelle sync workstate rechain` repairs a story whose seeded events sit anywhere in its local chain, and its `--dry-run` applies exactly the checks the real run does. The chain is compared with the hosted one: the longest start the two share event for event is kept, everything after it is removed and the hosted events are taken in its place, so the local chain ends as the hosted chain with its ids, bases and order, and the push head is the hosted latest. Before, only a seeded genesis at the very start was repairable; a seeded pair a multi-page pull had linked later hosted events onto was refused by the real run ("remove refused: ... builds on ...", "the hosted chain could not be taken") after the dry run had listed it as repairable. The dry run and the real run now share one decision, so each story gets the same outcome and reason in both: `rechained`/`would rechain` or `refused`/`would refuse`, one line each, and the exit status is non-zero when a story was refused. (sty_8fc70019)
- `rechain` with no ids now also takes every story whose chain differs from its hosted one, not only those recorded in conflict, which includes a story whose events are all on the hosted chain but linked in another order. Stories the hosted project holds nothing of, and stories that are only the hosted chain plus unpushed local events, are skipped. A story with a local event the hosted chain lacks that is not a seeded genesis is still refused by name and left unchanged. (sty_8fc70019)

## [0.0.761] - 2026-10-26

### Breaking
- **Location holds are removed from the client entirely, and `satelle doctor` no longer reports the retired `[sync] hold_stale_after` key.** The key has been ignored since hosted holds went in 0.0.737. A `satelle.toml` that still carries it loads without error and without a finding. The server counterpart, removing the hold refusal from Apply, is satelle-server sty_595b95bc. (sty_642275eb)
- init-heals: `satelle init` (or `satelle migrate --yes`) deletes the retired key and leaves every other line of `satelle.toml` as it was.

## [serve-v0.0.216] - 2026-10-26

### Changed
- The service binary no longer carries the retired `[sync] hold_stale_after` key or its doctor finding. (sty_642275eb)

## [0.0.760] - 2026-10-26

### Fixed
- A story pulled from the hosted project now arrives with its hosted event chain. `satelle sync workstate pull` reads each pulled story's chain from the server's Chain service and stores its events in chain order, keeping their ids and bases, before the story is folded. A pull therefore no longer seeds a local `workflow_version` and `definition_snapshot` for a story whose hosted chain has events, and the next push sends nothing for it. Before, a story created on one machine and pulled to another was given its own two-event history on the second machine, and every later push was refused with "moved since its start: re-read and decide". A server without the Chain service is skipped with one warning, and pulls behave as before. (sty_9b1dcf4b)

### Added
- `satelle sync workstate rechain [id...]` repairs a story that an earlier pull forked: when its only local-only events are the ones seeding made, it removes exactly those, takes the hosted chain, rebuilds the story and clears its conflict. A story with any other local-only event is refused by name and left unchanged. With no ids it checks every story recorded in conflict; `--dry-run` lists what it would do. It never pushes. A pull that finds such a fork names the story for `rechain`. The ledger's append-only rule has this one guarded exception: only events the caller's guard confirms as seeded, and on which no remaining event builds, can be removed. (sty_9b1dcf4b)

## [serve-v0.0.215] - 2026-10-26

### Changed
- The service binary carries the ledger's guarded removal of seeded events. (sty_9b1dcf4b)

## [0.0.759] - 2026-10-25

### Added
- A remote channel now survives a relay restart. When the relay connection closes for a restart-class reason (going away, an abnormal close, a network error, or a 5xx answer), the performer node (`satelle node join`) and the local starter each redial the same relay session, backing off from 500ms to 15s, for up to two minutes. They then take a fresh key exchange against the same pinned node key, and the channel resumes. Either end may arrive first: the node re-sends its hello every 250ms until the local end answers, so a hello the relay drops while the node waits alone is not lost. A job running when the connection dropped fails with the named reason `relay_restarted`. The node cancels its handler and stops that job's heartbeat, so the job is reported once and never left hanging. A close that means refusal is never retried: signed out (4401), `bad_session`, `slot_taken`, or a refused sign-in. The node keeps sending its status heartbeat while it redials, and it still posts `/end` when it gives up. Each resume, and each time an end gives up, is recorded in the ledger as `remote_channel_resumed` or `remote_channel_resume_failed`. (sty_ee78148f)

## [serve-v0.0.214] - 2026-10-25

### Changed
- The service binary carries the ledger kinds for a resumed or abandoned remote channel. (sty_ee78148f)

## [0.0.758] - 2026-10-25

### Added
- A step placed on a remote now runs there. A reviewer gate whose binding declares `placement = "remote"` runs through a performer node over the relay channel instead of locally: its payload is sent inline, and the node is ended once the verdict is in, including when the run fails. With no channel the gate is refused with an error naming the binding, and a reviewer binding with a team placement is refused when agents.toml loads. A binding may also declare `remote_path`, allowed only with `placement = "remote"`; the performer start sends it as `path` so satelle.dev resolves the definition by path, and omits the key when it is unset. A performer or plain node now sends a `running` status at the task path's interval for its whole life, from the moment it joins, through definition install and the `[host]` bootstrap, idle on the channel or working a job. The beat stops before a failed start is reported, so the concluded status is the last one, and before `/end`, which is sent on every exit. Before, a node fell silent after joining and satelle.dev ended it as lost mid-step. (sty_2ebb8200)

## [serve-v0.0.213] - 2026-10-25

### Changed
- The service binary carries the remote reviewer placement, the `remote_path` binding key and the whole-life node heartbeat. (sty_2ebb8200)

## [0.0.757] - 2026-10-25

### Fixed
- `satelle story diff --json` now reports `engagement_sha`, the HEAD a story first engaged at. Parking and resuming a story never changes it. `baseline_sha` keeps its meaning: it is the point the diff is taken from, and it moves to the resume point, so a park does not pick up other stories' commits. The version-stamp check now compares `.version` against `engagement_sha`. Before, a story parked after its release commit and then resumed was compared against the release commit itself, and was rejected for a version it had already bumped. (sty_1168e79f)

## [0.0.756] - 2026-10-25

### Fixed
- `satelle story ready` now runs and records a readiness round when the route's readiness step declares no `mark`. Before, it refused such a step, while the epic gate still demanded an accepted round before any child could engage, so a repo whose readiness step omitted `mark` could never start an epic's children. The accepted round is written to the ledger as usual and no tag is applied; the result line says the round holds and that no mark is configured, and the JSON result carries `no_mark`. A route that declares a mark behaves as before: the tag is applied and the guard on the mark still holds. When an epic has not been readied and `story ready` cannot run for it, the epic gate's refusal now says why, instead of telling the caller to run a command that would refuse. (sty_6172eb76)

## [0.0.755] - 2026-10-25

### Added
- `[engagement] parallel` accepts a third mode, `"worktree"`: any number of stories hold seats at once, from different epics or none, each from its own git working tree. A second engagement from a tree that already holds a seat is refused, naming the holder. A seat lease now records whether it is tree-scoped and whether its key is exclusive, so a worktree-mode claim co-holds with every other tree-scoped claim whatever its key, while the children of a sequential epic share the epic's key, exclude each other and still admit one at a time. Worktree mode refuses an engagement when the lease store is unavailable rather than guessing. `none` and `epic` behave as before. This repo now runs `parallel = "worktree"`. (sty_69ec7fbc)

### Fixed
- Under `epic` and `worktree`, a child's seat key comes from the same membership rule the wave and readiness use: its `parent_id`, or the container of an `epic:<theme>` set it carries, falling back to the plain `parent_id` when no epic container resolves. Before, the key read only `parent_id`, so a sibling joined to its epic by tag alone was refused while another sibling held the seat (sty_9e3520c2). (sty_69ec7fbc)

## [serve-v0.0.212] - 2026-10-25

### Changed
- The service binary carries the `worktree` seat mode, its settings enum entry and the updated epic-wave help topic. (sty_69ec7fbc)

## [0.0.754] - 2026-10-25

### Added
- The default substrate's epic and parent routes offer the epic-branch lane. A child of an epic or parent with no `lane:trunk` tag commits on its own pushed branch `epic/<child-id>` from its own worktree and never touches trunk; a child tagged `lane:trunk` keeps the full release route. The new shipped functional check `satelle-epic-branch-check`, on every working-lane close, accepts a branch-lane member only when its branch is pushed and not yet on trunk, and accepts a `lane:trunk` story or a story in no container (read straight from the transition payload, so no CLI call is needed). The container routes gain a `merged` step after the children close, performed by the shipped `satelle-epic-merge` skill, which merges every un-laned child's branch with `--no-ff` and puts trunk on the remote once through `satelle trunk publish`; the shipped `satelle-epic-merged-check` refuses the container's close until every such branch is on the pushed trunk. The route validator now judges an `after_children` step per route that lists it, rather than refusing it across the whole catalogue. (sty_b9d9a3df)

## [serve-v0.0.211] - 2026-10-25

### Changed
- The service binary carries the default substrate's epic-branch lane: the `merged` container step, the epic-branch and epic-merged checks, and the per-route `after_children` validation. (sty_b9d9a3df)

## [0.0.753] - 2026-10-25

### Fixed
- The default substrate's epic and parent routes now carry the readiness round: a `ready` step entered from the start, which waits on the children, carries the `ready` mark, and may add `order:`, `surface:`, `lane:` and `depends-on:` tags or create a child. It is performed by the shipped read-only `ready-reviewer` seat with a new repo-agnostic `ready` skill. Before, in a repo with no workflow of its own, the epic readiness gate skipped every epic, `satelle story ready <epic>` failed with "declares no readiness step", and children started with no dependency edges. Now a child of an un-readied epic is refused with text naming `satelle story ready <epic>`, and that command runs the round. A repo whose own workflow already declares the step resolves the same route as before. `satelle help epic-wave` now says the gate applies when the epic's route declares a readiness step, and that the default substrate's epic route declares one. (sty_25064a6c)

## [serve-v0.0.210] - 2026-10-25

### Changed
- The service binary carries the default substrate's readiness step for epic and parent routes. (sty_25064a6c)

## [0.0.752] - 2026-10-25

### Added
- A functional-check skill can declare `check_reuse: tree` in its frontmatter to run once per tree. When such a check passes, the ledger records a `suite_run` entry with the check, the story, the tree it proved (a content hash of HEAD plus working-tree changes, untracked files included), the command, the outcome and the duration. When the same check meets the same story, command and tree again, for example on an edge presented a second time after another gate refused it, it accepts without running the command and its notes cite the recorded pass. Any change to the tree, or a recorded failure, runs it again. A check that does not declare the key runs every time, as before. Functional checks are still never carried like reviewer verdicts: each presentation still dispatches the check, which may be satisfied from the evidence. `satelle help reviewer-checks` describes the key. This repo's own suite gates opt in through a separate substrate story. (sty_dc554488)

## [serve-v0.0.209] - 2026-10-25

### Changed
- The service binary carries the check-reuse mechanism. (sty_dc554488)

## [0.0.751] - 2026-10-25

### Fixed
- An engagement seat taken from a Claude Code session is now stamped with that session's id, so the session that drove a story can edit at its executor steps (integration, release). satelle now reads `CLAUDE_CODE_SESSION_ID`. Before, it fell back to a per-process file under `$XDG_RUNTIME_DIR/satelle-sessions`, and those files were never cleaned up, so a reused process id could stamp the seat with an unrelated session. The edit hook then refused every edit and asked for a `SATELLE_SESSION` stamp that Claude Code's hook cannot see. Each published session file now records the process's start-time identity: a file whose process is gone or was replaced is removed and never trusted. When a seat's stamp matches no live session and the caller engaged the story, the refusal now names a remedy that works, re-stamping with `satelle story set <id> --status <same>`, and that same-status set does re-stamp such a seat. (sty_36d6f155)

## [serve-v0.0.208] - 2026-10-25

### Fixed
- The service binary carries the session and seat attribution fix. (sty_36d6f155)

## [0.0.750] - 2026-10-25

### Fixed
- `satelle node join` now runs the definition's `[host] bootstrap` commands. They run in order, once per node, after the node reads its definition and before it works the run, with no stdin and a bounded timeout per command. Until now nothing ran them: satelle.dev's sprite driver only applies the network policy, installs satelle and starts the join, so a remote whose bootstrap installed a tool (such as `gh`) started without it. A failing command (non-zero exit or timeout) fails the start, and the node reports a concluded reject naming the command's position and the reason (`bootstrap command 2: exit status 3`), never its output, environment or a secret; no run is worked. The report goes through the node's own status reporter, so it still reaches satelle.dev when the node is being stopped. (sty_99bb51e1)

## [0.0.749] - 2026-10-24

### Changed
- A child of an epic can no longer leave its route's entry state into an engaging status, by a transition or by `story create` straight into one, until its epic has passed its readiness round. The refusal names the epic and the command that readies it: `satelle story ready <epic>`. The epic's readiness review is what sets each child's `depends-on` edges and `lane:trunk`, so skipping it sent children down the wrong release path. Children on routes with no readiness step (substrate, docs) are covered too. Cancelling or parking an un-readied child stays open, an epic's own readiness round is not refused, and stories in no epic are unchanged. `satelle help epic-wave` states the rule. (sty_a476b913)

## [serve-v0.0.207] - 2026-10-24

### Changed
- The service binary carries the updated epic-wave help. (sty_a476b913)

## [0.0.748] - 2026-10-24

### Added
- A workflow step can wait on a team remote's run for its story: `remote_wait = { remote = "<rmt_ id>", on = { accept = "<status>", reject = "<status>", stopped = "<status>" } }` in `step.toml`. `satelle validate` refuses an outcome mapped to an edge the route does not declare. `satelle story get` and `satelle story route` show where the story stands with that remote: queued with its position, running with its step, or concluded with its outcome. When the run concludes, the driver takes the mapped edge through the step's normal gates and relays the run's findings to the next performer. A park outcome goes through the drive's ordinary park, and an unmapped outcome parks. A run that ended without reporting counts as `stopped`. The run is read from satelle.dev (`GET /api/v1/remotes/{remote}/stories/{id}/run`); until that route is served, the step keeps waiting. A node whose status report carrying findings is refused resends it once without them. The binary holds no PR or GitHub logic: what a remote's outcome means is entirely workflow configuration. (sty_8c0f521d)

## [serve-v0.0.206] - 2026-10-24

### Changed
- The service binary reads `remote_wait` on workflow steps. (sty_8c0f521d)

## [0.0.747] - 2026-10-24

### Added
- An execution that stops short of its route is recorded as stopped. This covers an execution that is refused (including before its first presentation), fails, times out, or ends on a performer's question. The execution gets one `drive-stopped` row with outcome `stopped`, the step, the performer and the performer's last message, taken from the already-redacted invocation record so no secret value appears. When the execution's `story_id` input names a story, that story also gets one `execution_stopped` row naming the execution, its task, the step, the performer and the last message, so the developer sees it on the story. An execution that reaches its route's end records neither. A story's own drive parks exactly as before. (sty_132e7781)

## [serve-v0.0.205] - 2026-10-24

### Changed
- The service binary knows the `execution_stopped` ledger kind. (sty_132e7781)

## [0.0.746] - 2026-10-24

### Fixed
- The changelog, and the embedded copy that `satelle changelog` reads, carry the 0.0.745 entry, which the 0.0.745 release left out. (sty_7028d996)

## [0.0.745] - 2026-10-24

### Added
- A binding in `agents.toml` can declare `placement = "local" | "remote" | "team:<path>"`; the default is `local`. `satelle validate` refuses any other value and names the bad binding. `satelle validate` and `satelle workflow show` print each binding's placement. A step that allocates a `remote` binding runs its performer on a node that satelle.dev starts for that step. The local end pairs with the node over the relay and trusts it only once the node's published channel key matches; there is no enroll token and no trust-on-first-use. The node runs the allocated binding with the step's prompt. A step that allocates a `team:<path>` binding wakes that team remote for the story and waits on its run (queued, running, concluded), dispatching no local performer. A repo with no `placement` field behaves exactly as before. The live remote and team paths need satelle.dev's performer-node, channel-key and story-run routes; until they ship, the remote leg refuses with an error naming them. (sty_7028d996)

## [serve-v0.0.204] - 2026-10-24

### Changed
- The service binary reads the `placement` field on agents.toml bindings. (sty_7028d996)

## [0.0.744] - 2026-10-24

### Added
- When a task-kind team remote's run concludes, the node sends its execution, the execution's ledger rows, the run-output document and one `execution_link` row on the subject story to satelle.dev in a single push under the node's own token (`POST /api/v1/remote-nodes/<id>/workstate`). The push needs no satelle login and names no project: satelle.dev records the batch in the remote's team project. After `satelle sync workstate pull`, `satelle story route` on the subject story shows a Remote executions line naming the execution, the task, the remote and the outcome. Personal execution sync is unchanged. Until satelle.dev accepts the route, the node logs the refused push and still ends cleanly. (sty_0ed01372)

### Fixed
- The embedded changelog that `satelle changelog` reads now includes the 0.0.743 entry, which the 0.0.743 release left out. (sty_0ed01372)

## [serve-v0.0.203] - 2026-10-24

### Changed
- The service binary knows the `execution_link` ledger kind. (sty_0ed01372)

## [0.0.743] - 2026-10-24

### Added
- A team remote whose definition declares `[drive] kind = "task"` runs one execution of its task per wake. The node installs the bundle's `tasks/tsk_<name>.md` and refuses to prepare when the task named by `drive.task` is missing. It maps the wake's subject (`story_id`, `trigger`, `trigger_source`) onto the inputs the task declares, and drives the execution through its own route without touching the subject story. It reports the run on its status heartbeat as queued, running with its step, and concluded with the outcome, re-sending well inside the server's lost-node window. `satelle execution record --outcome accept|reject|performed|no-op` lets the performer or validator record how the run ended, and a run that found nothing to do records `no-op`. A story-kind remote behaves as before. (sty_2af24ef9)

## [0.0.742] - 2026-10-24

### Added
- A task can declare typed inputs (`string`, `int` or `bool`, required or optional) in its task file. `satelle execution create --input name=value` records values for them and refuses a missing, undeclared or mistyped one. The execution's performer and validators receive the inputs as `SATELLE_INPUT_<NAME>` environment variables and in their payload. A task that declares no inputs behaves as before. (sty_fb0b2b91)

## [serve-v0.0.202] - 2026-10-24

### Changed
- The service binary carries typed task inputs on executions. (sty_fb0b2b91)

## [0.0.741] - 2026-10-24

### Fixed
- satelled is rebuilt and released as serve-v0.0.201, catching up the serve-path changes shipped since serve-v0.0.200 that the 0.0.737–0.0.740 releases did not bump. (sty_73c9aa10)

## [serve-v0.0.201] - 2026-10-24

### Changed
- The service binary carries the serve-path changes since serve-v0.0.200: no hosted holds (sty_40853d02), the local-first-team epic (sty_340f3ae3), and the story page's Messages panel (sty_73c9aa10).

## [0.0.740] - 2026-10-24

### Added
- Agents and people can leave messages on a story for the story, a role or a named session: `satelle story message --to <story|role|session|*>`. `satelle story messages` lists a story's messages, `satelle story message-mark` marks one delivered, answered or undeliverable, and `satelle board` lists messages across stories with filters for state, overdue, session and story. A performer's payload delivers the messages addressed to it and marks them delivered; reviewers' and chat payloads leave them waiting. The board is local and needs no connection. `api/board.proto` (`satelle.board.v1` Board: Post, Fetch, Mark) is the contract for the hosted board. The mirror's story page shows a Messages panel. (sty_73c9aa10)

### Fixed
- `satelle sync workstate push --help` is back under the help-length ceiling. (sty_73c9aa10)

## [0.0.739] - 2026-10-24

The local-first-team epic (sty_340f3ae3) is complete. The CLI is local first, story events are a linked chain published by compare-and-append, no story is held across machines, and remotes are run only through satelle.dev.

### Removed
- The remote control plane has left the CLI. Remotes are defined, published, given secrets and run only through satelle-server and the satelle MCP. `satelle remote` (start, run, test, join, preflight), `satelle secret` and run secrets are gone, and so is `.satelle/config/remotes`. There is no compatibility path. (sty_9d85a9c3)

### Changed
- A node that satelle.dev starts runs through the hidden `satelle node join`. It fetches its definition with its own node token, reads its secrets, and ends itself through satelle.dev. `--token-stdin` reads that token from the first line of stdin, so it never appears in argv, a log or the process list. satelle-server launches nodes pinned at 0.0.739 or later with `node join`. (sty_63f30d3b, sty_74c02774)
- A story's session (the configuration, inputs, logs and outputs of each run) travels with its project copy, so any member or machine can inspect and rebuild a run with `satelle story session`. Every agent invocation records the digests of the substrate and bindings it ran under. (sty_9914df10)
- When a sync pass finds a story's two copies disagreeing, it appends an `overlap` row to the story's ledger, naming the other location that last worked on it. The row travels with the story. (sty_9914df10)

## [0.0.738] - 2026-10-24

### Fixed
- When the server refuses a story's events because the story has moved, the "N event(s) not seen" count and the recorded conflict now use the server's own count (`missing_count`). The server lists at most a few of those events in the refusal, so the count was too low on a long chain. An older server that sends no count still gets the length of the list. (sty_e292e7f5)

## [0.0.737] - 2026-10-24

### Changed
- No story is held across machines any more. The CLI makes no hosted hold calls (checkout, release, takeover, checkout-log or the hold heartbeat), and `satelle story hold` is removed. Engaging a story that another machine worked last just works. When two machines move the same story, the second push reports the compare-and-append conflict ("moved since …: re-read and decide").
- Engaging a story appends a `working_on` event to its ledger, naming the location, session and worktree. Other machines see it after a pull in `satelle story get`, `story list` and the session-start line. It never blocks anyone.
- One story per session is still refused locally from the local seat, with no network call.
- Engaging a story assigned to someone else prints a note naming the assignee instead of refusing.
- `[sync] hold_stale_after` is retired. init and migrate no longer write it, `satelle migrate` removes it, a config that still has it loads, and `satelle doctor` reports it as retired.

### Fixed
- A pulled story's events are stored in chain order, so its local chain verifies and the story can be pushed again from the machine that pulled it. (sty_40853d02)

## [0.0.736] - 2026-10-24

### Fixed
- Publishing story chains no longer gives up on a story whose earlier Append landed without this checkout seeing the reply. When the server's stale-base refusal arrives without its detail, push reads the story's latest event from the server and resumes from it, or reports the story as moved. It is never reported as a generic error.
- Transient refusals of an Append (unavailable, a proxy 502, or a stream reset such as ENHANCE_YOUR_CALM) are retried a few times with backoff before being reported.
- The record of what each story last published is saved under a file lock through a unique temporary file, once per push, so another satelle process writing the same state file can no longer make a push lose it. (sty_9bdf08c4)

## [0.0.735] - 2026-10-23

### Changed
- `satelle sync workstate push` publishes each story's new ledger events to satelle.dev by compare-and-append over gRPC (`satelle.chain.v1.Chain/Append`, satelle-server 0.1.208 or later). Each Append names the last event this checkout had published for that story, per server, project and repo. Story-scoped events no longer travel in the bulk `Apply`, and a refusal never places a hold.
  - A stale base is reported as `the story <id> moved since <base>: re-read and decide`. Nothing more is sent for that story, local events are untouched, a story conflict is recorded, other stories continue, and push exits non-zero. If the server's newer events are this checkout's own earlier publish, push says `already published up to <id>: resuming from it` and sends only what follows.
  - Large stories go as several Appends of at most 3 MiB, chained on the server's latest event, and replies up to 16 MiB are readable.
  - Nothing is appended when the ledger area is not opted in or no server is configured.
  - The first push after upgrading publishes every story's existing chain once. (sty_9bdf08c4)

## [0.0.734] - 2026-10-23

### Changed
- A story's status, definition, tags and park origin are now derived from its ledger chain. The stored story row is a cache, rebuilt from the chain after every change and after a sync pull. `satelle story get`, gate evaluation and `satelle story wave` read the chain, while listing still reads the row. Tasks are unchanged: their file stays the source of truth.
  - On first open, every legacy story gets one `definition_snapshot` event taken from its current row (status and park origin included), so the chain agrees with the row however sync ordered its older events. On this repo that took about 17s, once, for 1,553 stories.
  - `satelle story reconcile` reports every field where a row disagrees with its chain, and `--repair` rebuilds those rows from the chain.
  - `satelle sync workstate push` checks each story's row against its chain before publishing. A story that disagrees is held back, and the report names the story, the field and the chain's value. Run `satelle story reconcile --repair`, then push again.
  - Each distinct workflow version a story's events recorded travels in its chain as a `workflow_version` event carrying the route source. A story's transitions can be replayed, each against the workflow version it was made under, and an edge valid in neither version is reported as a break.
  - Stories that `satelle reindex` files automatically start their chain with `story_created` and still skip the create gate. (sty_d84be8bc)

## [serve-v0.0.200] - 2026-10-23

### Changed
- The service binary reads story state through the chain fold. (sty_d84be8bc)

## [0.0.733] - 2026-10-23

### Added
- Every ledger event for a story names the event it builds on (`base_id`) and the version of the workflow route that governed the story when it was written (`workflow_hash`), so a story's history is one linked chain that can be checked and replayed. On the derived route the version is a hash over both route halves as they govern, a legacy markdown workflow uses its own doc hash, and "" means nothing governed.
  - The first time a database is opened, its existing events are linked into one chain per story, in their insertion order. Nothing else is rewritten, and old events keep an empty workflow hash rather than an invented one.
  - Workstate sync carries both fields, so a pulled event keeps the chain it was written in.
  - `satelle ledger verify [--story <id>]` reports every chain break (a missing base, a base after its event, more than one root, or a fork with no `chain_fork` record) and exits non-zero on any. It never repairs anything. (sty_41f61669)

## [serve-v0.0.199] - 2026-10-23

### Changed
- The service binary reads and writes the linked, workflow-stamped ledger. (sty_41f61669)

## [0.0.732] - 2026-10-23

### Fixed
- A number quoted mid-sentence in an acceptance criterion (for example "The handler returns 403. A retry succeeds.") is no longer read as a criterion number, so a planner is no longer failed for "missing acceptance criterion 403". Only a number that begins a criterion line, after optional indentation, counts; the `1.` and `1)` forms on their own lines parse as before. Criteria that put several numbers on one line are now read for the first only, as judged criteria already were. (sty_14f521e5)

## [serve-v0.0.198] - 2026-10-23

### Fixed
- The service binary carries the line-anchored criterion-number parsing. (sty_14f521e5)

## [0.0.731] - 2026-10-23

### Fixed
- A finished story's hold no longer stops another machine's work-state push. When a push or sync pass finds another location's hold on a story that is terminal on the hosted copy, satelle takes the hold over and releases it on the server, without needing the holder, and drops it from the local hold registry. Previously such a hold was recorded and the push's items cursor stopped before it permanently, so every push re-collected every story since then and was killed at the service's 2-minute budget ("push failing"). The cursor now moves past hosted-terminal stories and stops only before a real hold on a story still in flight. (sty_aa9eaaba)
- A story terminal on the hosted copy is never recorded or reported as held: `satelle story hold show <id>` prints `<id> unheld (terminal)`, and `satelle story list` and the session-start advisory no longer list it. Holds on stories still in flight, the `skip <id> (held elsewhere)` line and the reachability and push-failing warnings are unchanged. (sty_aa9eaaba)

### Changed
- A takeover refused because the holder is live now names the holder's heartbeat time and the story record's last update, and says that the heartbeat proves only that the holder's daemon is running, not that its session is working on the story — confirm with the holder before `--force --reason`. (sty_aa9eaaba)

## [0.0.730] - 2026-10-22

### Changed
- Locked substrate is never edit-exempt, in every repo. A change to a path under `[gate] lock_substrate_paths` (the `.satelle/` workflows, skills and agent bindings by default), made through Edit/Write or a shell command, is now refused unless this session is engaged in a substrate-category story. That includes the case where no story is engaged at all: before this change `.satelle/` was freely editable with nothing engaged, because the seeded `edit_exempt_paths` entry was consulted first. File a story with `--category substrate` and engage it. If the substrate itself prevents engagement (an invalid `step.toml`, say), ask the developer to repair it outside the agent session. Exempt paths that are not locked (`.gitignore`, the harness scaffolds, `/tmp`, story dumps) are unchanged. The rule is stated once, in `satelle help substrate`. (sty_66abc0e3)

## [serve-v0.0.197] - 2026-10-22

### Changed
- The service binary carries the updated `satelle help substrate` topic (lock precedence over edit_exempt_paths). (sty_66abc0e3)

## [0.0.729] - 2026-10-22

### Added
- A step can refuse a re-presentation that does not answer the prior blocking findings. `[<step>.answer_progress]` takes `enabled = true` and `min_progress = N`, and is allowed only on a `propose` step. A re-presented performer must answer (`answers`) or change at least `min(N, findings offered)` of the prior blocking findings, or the round is refused as `unanswered findings` before any gate runs. The refusal lists the finding ids, counts against `reject_budget`, and saves the panel's cost. A finding counts as changed when the quotation it recorded no longer appears in the material the gates will judge. `satelle workflow show-route` shows the rule. (sty_73224b86)
- `satelle story cost --rounds` reports each round's performer cost (turns, tokens, duration), the findings it answered against those it was offered, and where each edge converged. (sty_51d0f74a)

## [serve-v0.0.196] - 2026-10-22

### Changed
- The service binary carries the answer-progress refusal and the per-round performer cost. (sty_73224b86, sty_51d0f74a)

## [0.0.728] - 2026-10-22

### Fixed
- A read-only git command into a linked worktree (`git -C <worktree> diff|status|log|show|rev-parse|rev-list|ls-files`) is no longer refused as a mutation of that tree. Before this fix, inspecting a worktree was blocked whenever a coder seat was live. A `--output` flag or any other subcommand still counts as a mutation, and the substrate lock still refuses even a read of locked substrate.
- A shell mutation from a session holding seats in several worktrees is attributed to the seat of the tree it lands in, and a refusal names that seat. Before this fix it was attributed to whichever seat came first. (sty_56d50bf6)

## [0.0.727] - 2026-10-22

### Added
- Every step now records which skills reached its performer and checks that their use left the evidence they declare.
  - Each delivered step skill is recorded with its name and sha256: on the agent_invocation row of a dispatched performer, or on a `skill_delivery` row when an in-loop step is entered. A skill that does not resolve is recorded as undelivered, never run silently.
  - A skill may declare `evidence_sections`, `evidence_keys`, `evidence_commands` and `evidence_tools` in its frontmatter. `satelle validate` fails a malformed declaration and warns on a step skill that declares none.
  - An always-on check compares the step's output and run trace with that evidence. It names each missing item, and reports an item as unverifiable (naming the adapter) when the harness has no trace channel for it. Back-edges, park and cancel are never judged.
  - `skill_evidence` in the route source's `[meta]` chooses `record` (the default) or `reject`.
  - Tool traces are written only while a story is engaged, and only for tools or commands a delivered skill declares. (sty_23ceeca2)

## [serve-v0.0.195] - 2026-10-22

### Changed
- The service binary carries skill-delivery recording and the skill-evidence check. (sty_23ceeca2)

## [0.0.726] - 2026-10-22

### Added
- `satelle story waive <id> --gate <skill> --check <name> --reason <text>` records one developer waiver of one named check of one gate on one story, as a `gate_waiver` ledger row with actor `developer`.
  - It writes the row only after the story id is retyped on the controlling terminal. It is refused from an agent session or without a terminal, and there is no stdin fallback.
  - `satelle ledger append` refuses the kind.
  - The waiver reaches only that story's payload for that gate, as `waivers`. A verdict that honours it lists the check in `waived`, which the review row records. A `waived` entry with no matching payload waiver is dropped.
  - The embedded release-review rubric honours a developer waiver for one named check and still judges every other check. (sty_bef97244)
- satelle writes a `plan-consumed` row (attachment, content hash, attempt, `source: dispatch`) immediately before a dispatched performer runs whenever its payload carries the plan. The payload says `plan_attached: false` when it does not. The embedded release-review rubric accepts the dispatch-written row alone for `check_plan_consumed`, so the check no longer depends on the coder saying so. (sty_1b0233ba)

### Changed
- Help tests pin that `satelle help agent-dispatch` documents the satelle-delivered `findings_to_answer`, and that no help topic teaches a manual findings relay. (sty_d7e4f539)

## [serve-v0.0.194] - 2026-10-22

### Changed
- The service binary carries the waiver verb, the dispatch-recorded plan consumption and the updated release-review rubric. (sty_bef97244, sty_1b0233ba)

## [0.0.725] - 2026-10-22

### Fixed
- A reviewer verdict whose `criteria[]` holds only the general entry (criterion 0) is read as a whole-story verdict: that entry decides, and no unchecked entries are invented for criteria the rubric never itemised. Before this fix, an axis gate such as scope review that accepted the story as a whole was recorded as "criteria not accepted: AC1, AC2, …", costing a full extra gate round. A rubric that wants one entry per criterion declares `verdict_criteria: required`; a lone general entry then rejects with notes naming the numbering mismatch (`expected AC1–ACn, got 0`). (sty_48a86a38)

## [serve-v0.0.193] - 2026-10-22

### Changed
- The service binary carries the updated agent-dispatch help. (sty_48a86a38)

## [0.0.724] - 2026-10-22

### Added
- A reviewer rubric may declare `payload_citations: plan` in its frontmatter. Its gate payload then carries every `path:line` and `path:start-end` citation in the story's plan, resolved to the cited lines (with three lines of context either side) in the tree the gate judges. A citation whose file or line no longer exists is listed as unresolved with the reason. The block is bounded at 24 KiB, with truncation marked, and `satelle validate`'s budget report counts it for a declaring seat. The embedded plan-review rubric declares it. On a real plan reviewed by the Opus and Grok panel, Opus made 9-12 tool calls instead of 14-17, and Grok 35-49 instead of 51-57. (sty_d0c9245a)

## [serve-v0.0.192] - 2026-10-22

### Changed
- The service binary carries the updated agent-dispatch help and plan-review rubric. (sty_d0c9245a)

## [0.0.723] - 2026-10-22

### Fixed
- A Grok reviewer seat's turns and token usage are read from its real output and recorded on its invocation row, so `satelle story cost --rounds` measures every panel seat, not only Claude's. Before this fix, the default Grok reviewer command's plain output recorded "no turn count" and "transport reported no usage". (sty_5ebcd6f9)
- An unchanged whole-verdict accept whose quotation the reviewer re-flowed (line breaks collapsed to spaces) is carried under `reviewer_carry`, matched by words as per-criterion quotations already are since 0.0.722. A quotation that differs in any word is still judged fresh. (sty_6ccbc5aa)

## [serve-v0.0.191] - 2026-10-22

### Changed
- The service binary carries the updated agent-dispatch and workflow-convert help. (sty_5ebcd6f9, sty_6ccbc5aa)

## [0.0.722] - 2026-10-22

### Fixed
- A per-criterion quotation is matched against the current material by its words, not its layout, so an unchanged criterion is marked settled and carried even when the reviewer re-flowed the plan's line breaks in its `reviewed` text. A real Opus reviewer did exactly that, so on 0.0.721 no criterion ever settled. A verdict that recorded no criteria is still carried by exact match, as before. (sty_99d3162b)

## [serve-v0.0.190] - 2026-10-22

### Changed
- The service binary carries the per-criterion quotation match. (sty_99d3162b)

## [0.0.721] - 2026-10-22

### Added
- A reviewer seat may return a verdict per acceptance criterion: `criteria[]` entries `{criterion, status accept|reject|unchecked, finding, reviewed}`, stored on the verdict row and on each panel seat. A verdict that sends criteria decides from them: any rejected, unchecked, missing, unparseable or out-of-range criterion means it does not accept, whatever its top-level decision says. On re-presentation, each seat's payload marks every criterion settled (its accept carries and its text is unchanged) or open, and a settled accept is carried without re-judging. Each panel seat also sees the other seats' open findings. A verdict with no `criteria[]` behaves as before. (sty_99d3162b)

## [serve-v0.0.189] - 2026-10-22

### Changed
- The service binary carries per-criterion verdict records. (sty_99d3162b)

## [0.0.720] - 2026-10-22

### Added
- A review gate, or a step's gates, can declare fail-fast (`fail_fast = ["<skill>"]` on a step, or `review_mode` on a `[[gate]]`). The seat's payload names the mode. A verdict records its coverage (complete, or stopped at a named finding) on the verdict row and on each panel seat. A partial (fail-fast) reject is never carried as acceptance of the unreviewed remainder: on re-presentation, the seat is told its prior coverage was partial and judges the remainder. A gate with no declared mode behaves exactly as before. The embedded `satelle-reviewer-verdict` principle gains "Review mode and partial verdicts". (sty_93359e0f)
- A re-presented step's performer receives every gate seat's latest verdict on its edge (skill, seat, decision, notes, attempt), with a panel's seats listed individually. Notes over the bound are cut with an explicit marker and a fetch command. It also gets a `findings_to_answer` list. The performer's per-finding answers (adopted or rebutted, with evidence) are recorded on its invocation row, and the gates receive `finding_answers` with any finding left unanswered. Before this, the performer saw a rejection only if the orchestrator relayed it by hand. (sty_f948d345)

### Fixed
- The performer's message window keeps the newest directed messages within its bound, not the oldest, so a later round's relay is never the one dropped. (sty_f948d345)

## [serve-v0.0.188] - 2026-10-22

### Changed
- The service binary carries the review-mode and coverage fields, and the performer findings and answers records. (sty_93359e0f, sty_f948d345)

## [0.0.719] - 2026-10-21

### Added
- Every reviewer seat attempt's agent_invocation row records its turns and tool calls, each with an adapter-named reason when the harness reports none (never a bare zero). An attempt the harness stopped at its turn cap is recorded with outcome `max-turns`, distinct from a reject, and a seat with no verdict still leaves its usage row. `satelle story cost` reports a story's review rounds per gated edge (presentations, and per seat attempt its turns, tool calls, outcome and duration), with an epic form. (sty_46e950ab)

### Fixed
- Engagement bookkeeping rows the binary writes for itself (`trunk_check`, the engagement baseline) no longer end a reviewer's verdict cycle. Each presentation to plan re-engages the story, so before this fix every re-presented edge had its prior verdict marked `reviewed_stale` and was re-judged in full instead of on the difference. (sty_2d877fa2)
- The embedded `satelle-agent-goals` principle ties "one story at a time" to being outside a container, or to the container's `sequential = true` override, not to a missing schedule. Since 0.0.718, a container with no schedule runs its children in parallel. (sty_eb5135ca)

## [serve-v0.0.187] - 2026-10-21

### Changed
- The service binary carries the run-count and outcome fields on reviewer invocation rows, the corrected engagement-bookkeeping classification, and the revised agent-goals principle. (sty_46e950ab, sty_2d877fa2, sty_eb5135ca)

## [0.0.718] - 2026-10-21

### Changed
- A container's children run in parallel when nothing is configured. `satelle story wave` no longer refuses a container whose waits_on_children step declares no schedule, and under `[engagement] parallel = "epic"` its children may hold seats alongside each other from their own worktrees. (sty_eb5135ca)
- `sequential = true` on the waits_on_children step is the one schedule setting, a user override. Wave then returns the runnable set only when it holds exactly one child, and siblings are limited to one seat. It is refused on any other step or beside `schedule = "parallel"`. (sty_eb5135ca)
- The `schedule = "parallel" | "sequential"` key keeps its meaning, and `satelle validate` now warns that it is deprecated: omit it (parallel is the default) or set `sequential = true`. `satelle help epic-wave`, the workflow-convert help and `satelle story wave --help` describe the default and the override. (sty_eb5135ca)

## [serve-v0.0.186] - 2026-10-21

### Changed
- The service binary resolves an unset container schedule as parallel. (sty_eb5135ca)

## [0.0.717] - 2026-10-21

### Fixed
- A panel gate's seats each keep their own `reviewed` quotation (bounded and truncation-flagged like a single-seat verdict) and attempt on the combined review row. On the next presentation of the same edge, each seat is given its own prior verdict, so it judges only what changed. An unchanged seat accept carries, and a seat that was unavailable is judged in full. Before this, every panel round re-judged the whole artifact, because the combined row carried no seat's `reviewed`. (sty_da9e8d0a)

## [0.0.716] - 2026-10-21

### Added
- The rework relay's consult seat is told the coder's binding and grant (its tools, verbatim) and the later route gates that own evidence. A consult objection the coder cannot clear (its remedy is outside the grant, or a later gate owns the evidence) can be marked `DEFERRED <owner>: …`. It then does not hold the relay, which ends `converged_with_deferrals` with the deferrals in its JSON result and its ledger row. (sty_83620b39)
- A deferred item reaches the gate that owns it as a `deferred` material file in that gate's payload, including when several gates share one bundled reviewer session. (sty_83620b39)

## [serve-v0.0.185] - 2026-10-21

### Changed
- The service binary carries the rework-relay deferral ledger kind and the updated agent-dispatch help. (sty_83620b39)

## [0.0.715] - 2026-10-21

### Changed
- A Grok reviewer binding written by the starter or by `satelle migrate` runs with a 32-turn budget (`--max-turns 32`); every other Grok role keeps 16. A code-verifying review batches 3-5 tool calls per turn and was measured at up to 60 calls, so 16 turns ended most first attempts at "Max turns reached" and an Opus+Grok all-accept panel could not pass. A binding's own command is unchanged. (sty_e5fb3626)
- `satelle agent validate` reports every binding's effective turn budget and where it comes from, or the adapter's reason it has none. (sty_e5fb3626)

## [serve-v0.0.184] - 2026-10-21

### Changed
- The service binary carries the role-specific Grok turn budget. (sty_e5fb3626)

## [0.0.714] - 2026-10-21

### Changed
- A session name shows the engaged story id once when the linked worktree's directory is already named after that story: `<host> · <repo> · <id>`, not `<host> · <repo> · <id> · <id>`. A worktree whose name differs still shows both segments. (sty_f0a9e098)

## [serve-v0.0.183] - 2026-10-21

### Changed
- The service binary carries the updated session help topic (the story id appears once). (sty_f0a9e098)

## [0.0.713] - 2026-10-21

### Added
- A story shared with a team workspace (`[sync] stories = "shared"` with a resolved team workspace) has its markdown attachments pushed to that workspace's documents collection at `stories/<id>/<file>.md`, byte for byte, on a work-state push, and pulled back the same way. A team remote's run reads them there (for example a pr-route story's plan). The same rules as the personal leg apply: binaries never leave, held or unpushed stories wait, and unchanged files are skipped. A refused upload is reported, never counted as success. On `pull --force` the team copy wins for a shared story. (sty_61872918)

### Changed
- `satelle sync workstate` help no longer says attachments only ever go to the personal collection. (sty_61872918)

## [0.0.712] - 2026-10-21

### Added
- A headless `satelle story drive` never stops without a record. After its first presentation, every way a drive can end short of its route's exit parks the story at the route's park state, or, when the park itself cannot be set, writes a `drive-stopped` ledger row and releases the drive's own hold. Both rows carry `source: drive`, the step, the performer, the node's `location_id`, and the most recent performer run's own last output as `last_message`, so a performer that ends on a question is never left only in its transcript. satelle-server alerts on these rows. (sty_aaafe4e2)

### Security
- `last_message` never carries a value satelle resolved into the performer's environment: each such value is replaced by `[redacted:<KEY>]` before it is written. (sty_aaafe4e2)

## [serve-v0.0.182] - 2026-10-21

### Changed
- The service binary knows the `drive-stopped` ledger kind and the updated agent-dispatch help. (sty_aaafe4e2)

## [0.0.711] - 2026-10-21

### Fixed
- The internal/remotechan relay tests no longer time out at their 20-second wait under full-suite or CI load. The test relay (relaytest.Relay) now registers the local end before it answers the upgrade, so a remote that arrives in between is paired instead of refused with a 404; a remote with no local end, a second remote on a paired session and an unknown role are still refused. Test support only; no shipped behaviour changes. (sty_656b6cfc)

## [0.0.710] - 2026-10-21

### Fixed
- A substrate-lane story can change the substrate it was filed for while sibling epic children perform: when a session holds several seats and none matches the edited tree, the edit passes if exactly one of the session's own seats is a substrate story, and every other live seat gets one `substrate_change_note` ledger row naming the substrate story and the path. An edit attributed to a non-substrate story is still refused, for Edit and for a shell redirect alike, each path judged by its own tree. (sty_08569de9)
- The substrate lock's refusal names each outside holder with its status and category, the condition that applied (own story not substrate, ambiguous across the session's seats, the session's own seat stale, or no seat attributed) and the fix for it, and never tells you to re-engage a lease that is not yours. (sty_08569de9)

## [serve-v0.0.181] - 2026-10-21

### Changed
- The service binary knows the new `substrate_change_note` ledger kind. (sty_08569de9)

## [0.0.709] - 2026-10-21

### Added
- `satelle remote join --token-stdin` reads the node's single-use enrollment token from the first line of stdin instead of argv, so it is not in the process list or an exec record. It stops at the newline without waiting for stdin to close, refuses an empty token, and never echoes, logs or writes the token or passes it to the node's agent. `--token` and `--token-stdin` are mutually exclusive and one is required; `--token` still works. (sty_5d7fa7f8)

## [0.0.708] - 2026-10-21

### Fixed
- `satelle agent validate` and `satelle init` no longer warn `reviewer.unsafe` for a reviewer binding whose tool grant satelle already holds read-only, such as pi `--tools read,grep,find,ls`; a reviewer satelle cannot classify still warns, and a provable escape stays an error. (sty_ea948877)

## [0.0.707] - 2026-10-21

### Added
- `satelle session name` prints one name for the agent session in this tree: short host · project · the worktree directory for a linked tree · the engaged story when this tree holds the live seat. `--harness <h>` reports `title: unavailable` for a harness with no session-name flag. (sty_88603414)
- The claude launcher `satelle agents install claude` writes starts claude with `--name "$(satelle session name)"`, so ListAgents, SendMessage and the terminal show which machine, repo and story a session belongs to. A name you pass yourself (`--name`, `--name=`, `-n`) wins. Put `$SATELLE_HOME/agents/bin` on PATH and launch `satelle-claude`; `satelle help session` has the details. (sty_88603414)

### Changed
- `satelle init` rewrites an installed satelle-owned launcher whose content is out of date, and hints at the launcher for a harness that can name its sessions. (sty_88603414)
- A checkout already registered with satelle.dev under an older label is registered again with its readable host · project label, so a hold owned by another machine shows that label. (sty_88603414)

## [0.0.706] - 2026-10-20

### Added
- New session-resident principle `satelle-review-structure-not-process`: agents review a workflow's structure (well formed, resolves, can run) and never its process. The practice a developer or team configures (who accepts, which gates, a skipped step) is carried out without stopping, asking, warning or objecting, by every agent satelle runs, reviewer gates included. (sty_a8a308aa)

### Changed
- `satelle-agent-goals` stops only on a structural gap and links the new principle. Its wording is tightened so the session-resident set still fits every harness limit. (sty_a8a308aa)

## [serve-v0.0.180] - 2026-10-20

### Changed
- The service binary embeds the new `satelle-review-structure-not-process` principle and the revised `satelle-agent-goals`. (sty_a8a308aa)
- The service binary carries the per-harness session-name flags the launcher reads. (sty_88603414)

## [0.0.705] - 2026-10-20

### Added
- Remote secrets. A remote declares each secret it reads as `[[secret]]` in its definition: key, scope (personal or team), source (`from = "env"` or a command) and an optional `expires` or `ttl`, never a value. `satelle secret push <remote>` sends each declared value to satelle.dev under its scope and grants it to the remote; `satelle secret list` shows keys, scopes, readers and expiry, never a value. A node reads each secret through a signed, run-bound request and hands it only to that run's agent, never writing it. (sty_e951fd7a, sty_0b1c72a9)
- `satelle remote key create|register|rotate` manage a remote's Ed25519 identity, `satelle remote identity` binds a platform workload identity (OIDC) instead, and `satelle remote check` reports a declared secret that is missing, ungranted or within 14 days of expiry. A definition records its satelle.dev id as `remote_id`. (sty_e951fd7a, sty_0b1c72a9)

### Changed
- A story a remote leaves at its own workflow's declared park state (`park = { state = "blocked" }` in the remote's done.toml) is a valid hand-back, not an off-contract status. (sty_e951fd7a, sty_c44d1f94)

## [serve-v0.0.179] - 2026-10-20

### Changed
- The service binary carries the agent-environment and `.env` handling the remote secrets path shares, and the remote-secrets help topic. (sty_e951fd7a)

## [0.0.704] - 2026-10-20

### Added
- Remote workflows (epic sty_bcbfd920, first eighteen stories). A remote is an optional, separately defined satelle that runs a story's or a task's workflow on another host. Nothing changes for a repo with no remote. (sty_688ca074)
  - `satelle remote create|validate` scaffold and check a remote definition under `.satelle/config/remotes/<name>`: a pinned satelle version, its own workflows, skills, principles and agent bindings, host commands (start, run, network, bootstrap, stop), an opaque host identity label, lifecycle segments with a hand-off contract (accepted statuses and categories, and the exits it may leave a story at), triggers, a per-step security preflight, and secret key names only. Credentials in a definition are refused. (sty_ce770f12, sty_5096ab53, sty_80fe4dd4)
  - `satelle remote test` runs a definition's own workflow on a sample story in a scratch worktree against a stub host that executes no host command or probe, and refuses cloud dispatch. (sty_cd09eeca, sty_80fe4dd4)
  - `satelle remote start` builds a fresh node at the pinned version, runs the declared preflight on the host before bootstrap, locks egress to the declared allowlist, fetches repos read-only, and joins the node over a sealed, outbound-only relay channel (X25519 + AES-256-GCM, single-use enrollment, launch-pinned key, per-job heartbeats) proven against the production relay. (sty_b20b3956, sty_2b6d0f7f, sty_b6b0518e)
  - `satelle remote trigger register|list|fire|pause|resume` manage a remote's named triggers on satelle.dev, forwarding each trigger's params unchanged; `satelle remote wake` handles a fired trigger within the hand-off contract, and every hand-off or hand-back that would leave a story holding a local engagement seat is refused. (sty_a9768210, sty_3da40e43, sty_8ddc056c, sty_8fa6790c)
  - `satelle remote run <story-id>` hands one story to the remote whose segment accepts it; `satelle remote run <name> --story|--tag … --story-of-record <id>` runs a workflow on selected stories on a fresh node and reports verdicts, sessions, spend and start-configuration gaps. A remote's sweep runs each accepted story on the real host through the one host.run resolver, and every step it runs is recorded with the remote, its version, the host, the identity label and the story, step and run ids. (sty_2bc9eebf, sty_1e4f7a2a, sty_2f3baf0a)
  - `satelle remote publish|list|fetch` publish a validated remote to the team workspace as numbered versions; generic `publish push` and `adopt` refuse remotes. `satelle remote retire` retires a registered remote after confirmation. (sty_1c2b81cf, sty_6b03d673)
- `satelle story drive <id>` takes a story through its configured route with no interactive session, relays review findings to the performer, and parks it blocked with the question when a person must decide. (sty_c0c795df)
- `.satelleignore` keeps matched paths, and `config/.env` always, out of sync push, snapshot, status, doctor and publish. (sty_73765f96)

## [serve-v0.0.178] - 2026-10-20

### Changed
- The service binary carries the remote-workflows ledger kinds, the story-drive executor step and the workflow and readiness changes they need. (sty_688ca074)

## [0.0.703] - 2026-10-19

### Changed
- The `satelle-reviewer-verdict` principle, delivered with every judging reviewer dispatch, now states what a blocking finding must carry: a cited location or quoted text, a path that is actually reached (a hypothetical is not a finding), the reviewer's own axis, and not style or a preferred design. A review with no such finding accepts. The per-skill copies of the "no preference" rule in the embedded plan-review, create-review and instruction-change-review skills are removed, so the principle is their single owner. (sty_b5e0d394)

## [serve-v0.0.177] - 2026-10-19

### Changed
- The service binary carries the updated embedded reviewer-verdict principle and reviewer skills. (sty_b5e0d394)

## [0.0.702] - 2026-10-19

### Fixed
- `satelle sync status`, `sync pull` and `doctor` no longer report an area behind, unpushed or diverged when the only difference is a hosted copy of an unedited embedded default (for example the default `tsk_*.md` tasks), which the binary supplies and sync never versions. Such an area reads in step, `sync pull` exits 0 and suggests no next step, and `sync reconcile` no longer lists the default as "deleted here only". Hosted bytes that are not an unedited default are still pulled, including on a first pull with no base, and a push never drops an edited task from the hosted copy. (sty_04e8c778)
- An area with no local files (a directory holding only README.md) reads in step whenever `satelle sync reconcile` lists no differences for it, so `sync pull` no longer loops on "still behind". (sty_04e8c778)
- `satelle sync reconcile` and `--diff` name a directory-area path by its server key once (`tasks/tsk_x.md`, never `tasks/tasks/tsk_x.md`); `--diff settings/satelle.toml` still resolves. (sty_04e8c778)

## [serve-v0.0.176] - 2026-10-19

### Changed
- The service binary carries the config helper that lists the shas of an area's unedited embedded defaults (internal/config `AreaDefaultShas`), which sync and doctor use to classify a hosted default. (sty_04e8c778)

## [0.0.701] - 2026-10-19

### Fixed
- Cancelling a story is refused while the working tree its own live seat records holds uncommitted changes outside the edit-exempt paths, the same as closing it to done. 0.0.692 caught only the workflow's terminal states, so a cancel, which the route adds as a park that never resumes, still released the seat and left the slice orphaned. Parking to blocked is still allowed, because the story keeps its way back to the same tree. (sty_37436717)

## [0.0.700] - 2026-10-18

### Changed
- `satelle login` signs in at the authorization server the satelle server advertises (WorkOS AuthKit for satelle.dev), not at satelle-server's own OAuth server. It reads `<server>/.well-known/oauth-protected-resource/api`, then that authorization server's metadata, and runs PKCE S256 with a loopback redirect using the client id the server publishes (`satelle_client_id`). No identity provider, host or client id is built into the binary. Every authorize and token request carries `resource` set to the API resource (`https://satelle.dev/api`), and the scope is `openid email profile offline_access`. (sty_620c56ea)
- The login callback listens on `http://127.0.0.1:53682/callback` by default. `--callback-port` changes the port, and `--callback-port 0` picks a free one for providers that accept any loopback port. (sty_620c56ea)
- Token refresh, for REST and for gRPC sync, goes to the provider token endpoint saved with the credential. satelle-server's `/oauth/token` and `Sync.Refresh` are no longer called. A credential saved by an earlier `satelle login` keeps working until its access token expires; after that, satelle asks for `satelle login` once instead of refreshing. (sty_620c56ea)
- `satelle logout` deletes the local credential, then opens (or with `--no-browser` prints) `<server>/logout?session_id=<sid>` to end the provider session. With no session id, or no browser, it says the local tokens were deleted but the provider session may still be signed in. (sty_620c56ea)

### Added
- A server refusal with `unknown_subject` (the account has never signed in at satelle.dev) is shown with the server's message, with no refresh and no retry, on REST and gRPC. (sty_620c56ea)

## [0.0.699] - 2026-10-18

### Fixed
- A judging reviewer's diff now includes the gitignored substrate the story changed (for example `.satelle/skills/` in a repo that keeps `.satelle/` out of git). Before, the reviewer's `material/diff.json` held no files and no patch for such a change, so instruction-change review could accept new skill text as "n/a" without seeing it. A new file appears as a whole-file add (`--- /dev/null`), a deleted one as a deleted-file entry, and the text goes through the same size limits and `satelle retrieve` offload as any patch. This applies to every judging gate and to `satelle story rework`. `satelle story diff` without `--include-substrate` is still git-only. (sty_98a2816b)
- A closed story's substrate change set now comes from its recorded change records, so a substrate file another story edits after it closed is no longer listed as its own. (sty_98a2816b)

### Added
- When another story's engagement overlaps the judged story's (read from the ledger's engagement and terminal-transition rows, or a live lease), the substrate section of the reviewer's diff names that story, so a reviewer does not judge a sibling's edits as this story's own. (sty_98a2816b)

## [0.0.698] - 2026-10-18

### Added
- A route step can declare `reviewer_carry = "until_accept"`. When its edge is re-presented after a rejection, a reviewer whose latest verdict is an accept, with a complete `reviewed` quotation still found word for word in the material, is not dispatched again. Its accept is recorded as carried (`carried_from` on the `review_accept` row, no agent invocation) while the rejecting reviewers iterate. Once every dispatched reviewer accepts, the carried reviewers are dispatched fresh in the same gate call, so the round that advances the edge has a fresh verdict from every reviewer. A reject, an empty, truncated or stale quotation, a quotation no longer in the material, a functional check or a panel is always dispatched. Carry works the same under serial, `parallel` and `bundle` scheduling. Without the key, every reviewer runs every round as before, and an unknown value is refused by the route parser. See `satelle help workflow-convert`. (sty_150a5b4d)

### Changed
- On a step that declares `reviewer_carry`, re-attaching the step performer's own output document and a directed message to the step performer no longer mark earlier verdicts stale. Any other new evidence still does. Before, every plan revision cleared every reviewer's quotation, so all reviewers re-judged in full every round. (sty_150a5b4d)

## [serve-v0.0.175] - 2026-10-18

### Changed
- The service binary carries the `reviewer_carry` step key in the route parser (internal/wfdot). (sty_150a5b4d)

## [0.0.697] - 2026-10-18

### Fixed
- The edit gate now refuses a source write made through an interpreter, the same as an Edit, `sed -i` or a redirect, when no story is engaged in an executor step. This covers python, node, perl and ruby running inline code (`-c`, `-e` and the like), and a shell running `-c`, a heredoc, a pipe, `-s` or `-`. Running a named script file (`python3 tool.py`, `bash script.sh`) is not affected, even with stdin piped in. An inline program run outside the tree counts as a write only when its text names the tree's root path. The substrate lock and the cross-repo fence are unchanged. (sty_5fac62b9)
- A quoted argument to a satelle verb, such as a `--body "$(cat <<'EOF' … EOF)"` holding source paths, an odd quote, `>` or `rm`, is no longer read as a write. (sty_5fac62b9)

## [0.0.696] - 2026-10-18

### Added
- A story can depend on a story in another registered satelle repo: `depends-on:<repo>/<story id>`, where `<repo>` is the name `satelle workspace list` shows. `satelle story wave` holds the dependent child until the other repo's story, and every container above it (its parent chain and its epic), is terminal on that repo's own route. An epic child's done therefore counts only once its epic, which carries the release, has closed. A repo, workflow, database or story that cannot be read is reported as `unresolved:` and blocks; it never counts as satisfied. The other repo is read in-process and read-only (no create, no migrate, no index write). A tag naming an unregistered or ambiguous repo, or this repo, is refused when written. See `satelle help epic-wave`. (sty_d00307f7)

## [serve-v0.0.174] - 2026-10-18

### Changed
- The service binary carries the shared repo-name rule (`config.RepoName`), the read-only workflow read (`docindex.ReadWorkflowDocs`) and the cross-repo `depends-on` help text. (sty_d00307f7)

## [0.0.695] - 2026-10-18

### Fixed
- Re-release of 0.0.694 with a fresh build stamp: 0.0.694's published binary carried the previous release's `satelle.build`, and the release workflow does not rebuild a version whose tag exists. No behaviour change. (sty_105360e2)

## [0.0.694] - 2026-10-18

### Fixed
- A dispatched Claude performer whose grant holds Claude's `Read` tool has a context channel, so a read-only performer can run with no shell and still pull its story. Each adapter's disk-read tool is now the name it actually offers (`Read` for claude, `read` for pi, `read_file` for grok and unknown adapters); before, a Claude grant of `Read,Grep,Glob` failed `satelle agent validate` and was refused at dispatch with a hint naming `read_file`, which Claude does not have. A grant with no read tool and no satelle shell is still refused, and the hint names the right tool for that adapter. (sty_105360e2)

## [serve-v0.0.173] - 2026-10-18

### Changed
- The service binary carries the per-adapter disk-read tool names used to judge a performer's context channel (internal/config/agents.go, internal/agentcli/pi_seat.go). (sty_105360e2)

## [0.0.693] - 2026-10-18

Epic remote-ready (sty_0e87acd6): Ready runs without holding a seat.

### Added
- `satelle story ready <id>` readies a backlog story or epic without engaging it. It runs the route's readiness performer and gates, records every verdict on the story, and marks it ready only when the readiness gates accept; a rejected or failed round leaves status unchanged and counts as a rejected round against the reject budget. A step declares which mark it confers with `mark = "ready"`, and the performer that runs a step is resolved once for both the engaged and unengaged paths. (sty_a32bd6a9)
- A read-only readiness performer can report the story configuration it would change (tags, priority, new child stories), and satelle applies the report on the record when the step declares `apply_changes`. Changes apply as one atomic batch, each recorded once on the ledger naming the performer; the title, body and acceptance criteria of an existing story are never changed this way, and a child created from a report passes the same create rules as one created by hand. A route that declares `apply_changes` on a step whose performer does not run before its gates is refused when loaded. (sty_af0b1761)
- A readiness seat can run on an OpenRouter model. A binding declares `auth = "api_key"` or `auth = "subscription"`, the auth mode is stamped on each verdict and shown in `satelle story cost`, and a step marked `untrusted_input` refuses any binding that is not read-only. (sty_416cb61e)

### Changed
- A story is never ready when it is created, whoever creates it: a create that asks for the ready mark is refused, and only accepted readiness can confer it. (sty_6e3ffa2f)

### Fixed
- `satelle sync pull` of an epic by id on a machine that holds none of its stories brings the epic's members, not only the container. (sty_7b4742d0)

## [serve-v0.0.172] - 2026-10-18

### Changed
- The service binary carries the readiness, performer-changes and binding-auth route keys (`mark`, `apply_changes`, `untrusted_input`, `auth`) so the project page parses routes that declare them. (sty_0e87acd6)

## [0.0.692] - 2026-10-17

### Fixed
- Closing a story is refused while the working tree its own live seat records holds uncommitted changes outside the edit-exempt paths. A close (done or cancelled) releases the seat, and with it the permission to commit, so the slice used to be left orphaned where no workflow step could commit it. The refusal names the paths (up to 20) and says to commit or discard them, then re-run the same command; status and seat are unchanged and no gate or performer runs. It applies to every workflow, including ones with no commit-evidence gate. A clean tree, changes only on exempt paths, a park to blocked, a non-terminal move, a story with no live seat, and any failure to read the tree all behave as before. (sty_37436717)

## [0.0.691] - 2026-10-17

### Fixed
- A quoted `tee` destination behind a wrapper command is judged as a write. `true | env tee "README.md"` (and the same through `sudo`, `nohup`, `xargs` or `stdbuf`) passed the Bash hook with no engaged story, because the wrapper is the command word and the only scan that saw `tee` read bare words, dropping the quoted destination. One place now decides what `tee` writes, whether it is the command word or behind a wrapper, and a quoted destination is the same target as a bare one: into the repo it is refused without an engaged story, under `[gate] edit_exempt_paths` it passes whatever the seat state. Plain `tee` keeps its verdicts. (sty_e79be13a)

## [0.0.690] - 2026-10-17

### Fixed
- `satelle doctor --all` judges each registered repo against that repo's own process of record, exactly as `satelle doctor` run inside it would. It used to reuse the invoking repo's process for every repo, so running it from one repo reported every other repo as a divergent worktree of it, with workflow FAILs for skills only the invoking repo ships. A separate repository is never reported as a worktree of the invoking one; process.divergent still fires for a real linked worktree, and a repo whose process config cannot be read gets one process.unreadable finding. (sty_2e86c0cc)

## [serve-v0.0.171] - 2026-10-17

### Changed
- The service binary carries the per-repo process loader used by `satelle doctor --all` (internal/config/process.go). (sty_2e86c0cc)

## [0.0.689] - 2026-10-17

### Fixed
- A shell redirect to a quoted path is no longer refused as a source edit. The Bash hook's write classifier dropped a double-quoted redirect target and took the next word instead, so `make test > "/tmp/scratch/out.log" 2>&1` resolved the operator `2>` as a file inside the repo and was denied unless this session held an executor seat (seen as "more than one performing story, none holds a seat for session …"). A quoted redirect target is now unquoted and resolved exactly as the same word is for a mutation-verb argument, so a scratch write under `[gate] edit_exempt_paths` passes whatever the seat state. Redirect operators (`>`, `>>`, `>|`, `>&`, `2>`, `&>`, …) are never taken as targets and a heredoc is never a target. A redirect into the repo, quoted or not, is still judged as an edit, and a `$VAR` target the hook cannot expand stays gated. (sty_e642df17)

## [0.0.688] - 2026-10-17

### Fixed
- `satelle sync pull` settles an area whose hosted copy has per-file heads but no snapshot. It writes a first snapshot and then pulls it, so `satelle sync status` reports the area in step instead of "behind, NEXT pull" forever. That covers the case where the hosted store was replaced or reset under a base this checkout still holds. The settle publishes through the same snapshot push the reconcile uses, never overwrites a local file, treats losing a race to another machine as benign, and removes the "documents: the hosted copy's snapshot could not be read" error for a heads-only documents area. Status never suggests a pull that cannot change the area. (sty_1217a400)

## [0.0.687] - 2026-10-17

### Fixed
- No test writes into the repository's source tree any more. The serve-gate probe test (cmd/satelled TestServeGateSeesNewFiles) used to plant `zz_gate_probe` files in the real tree, which raced the parallel tests that walk `internal/`. It hit TestNoPriceLiteralsInProductCode in CI run 38990644923. The probe now plants its files only in a private clone, using the working-tree gate script. The strict unit run (`make test STRICT=1`) fails, naming the files, if any test leaves the real tree changed. (sty_6b98ba1f)
- The serve-gate probe runs its assertions on a tree whose serve version is bumped but not yet tagged, the normal state between a serve push and its release. It no longer skips there, and the skip's CI allowlist entry is removed. This supersedes sty_5395c80a. (sty_6b98ba1f)

## [0.0.686] - 2026-10-17

0.0.685 was never published. Its CI test run failed on the relative-cost bound fixed below, so the release workflow never cut it. Everything listed under [0.0.685] ships in this release.

### Fixed
- The command-rule relative-cost checks pass on healthy CI hardware. The verdict now compares the median rule-evaluation sample to the median tokenizer sample, instead of the fastest of each, so one quick tokenizer outlier cannot tip it. The bound is 20x, set from measured ratios: about 3x locally and about 8x on a CI runner. The regression it guards against (re-tokenising per rule) still measures 84x to 598x. A test replays the samples from the failed CI run. (sty_bc19e95d)

## [0.0.685] - 2026-10-17

### Fixed
- The strict unit run (`make test STRICT=1`, which CI runs) no longer fails a release when a slow runner pushes internal/cli past Go's default 10-minute package timeout. The run now sets an explicit `-timeout 30m`. A unit-only per-test stall deadline (`UNIT_STALL`, 5m) still names a test that really hangs, long before that limit, and kills its whole process group so no test binary is left behind. The integration run keeps its own budgets. Locally, internal/cli takes 203s across three repetitions; the slowest single unit test takes 13.5s. (sty_3f2a5c78)

## [0.0.684] - 2026-10-17

### Fixed
- The process-tree CPU sample no longer reports a reaped child's CPU as 0. When a child is reaped mid-scan, the scan is torn; it is now detected and taken again. This also stops TestProcTreeCPUKeepsReapedChildCPU from failing intermittently in CI. (sty_673ec869)

## [serve-v0.0.170] - 2026-10-17

### Changed
- The service binary carries the process-tree CPU sampling fix. (sty_673ec869)

## [0.0.683] - 2026-10-17

### Fixed
- The gate hand-off tests no longer fail CI on TempDir cleanup. They wait for the detached gate child, a re-exec of the test binary that writes the verdict into the test's gates directory, before cleanup runs. `gatehandle.ProbePID` exposes the existing liveness probe for that wait. (sty_66439878)

## [0.0.682] - 2026-10-17

0.0.681 was never published. Its CI test run failed on every attempt because of the timing check fixed below, so the release workflow never cut it. Everything listed under [0.0.681] ships in this release.

### Fixed
- The command-rule relative-cost checks no longer fail CI on a loaded runner. Both checks now use one shared measurement and one bound: interleaved, fixed-iteration samples with the noise tolerated. A real slowdown still fails them. The proof that rule evaluation adds no measurable latency (sty_a74551d6 AC5) stays a real check. (sty_c77a3cf2)

## [0.0.681] - 2026-10-17

### Added
- `satelle update` and the session-start release check authenticate to the GitHub API with the operator's token, when one is set. They read `GH_TOKEN` and then `GITHUB_TOKEN`, the GitHub CLI's own conventions. That gives the authenticated rate limit instead of the 60-an-hour anonymous limit every caller on an IP shares. Asset and checksum downloads stay anonymous. The token is only ever a request header: it is never printed, logged or stored. (sty_66dce129)

### Changed
- A GitHub API rate limit is now reported as one, with the URL, the status and when it resets. Previously it showed as a bare `403 Forbidden`. When no token was sent, the message says to set `GH_TOKEN` or `GITHUB_TOKEN`. (sty_66dce129)

## [0.0.680] - 2026-10-17

### Fixed
- The integration suite and CI no longer spend the shared GitHub API rate limit. `scripts/hermetic.sh` pins the release-check endpoints (`SATELLE_RELEASE_API`, `SATELLE_RELEASE_LIST_API`, `SATELLE_RELEASE_BASE`) to a closed loopback address. A session-start release check in a test's fresh satelle home therefore never reaches GitHub and leaves no process behind. A full `make integration` run now makes no GitHub API requests; previously one run could exhaust the anonymous 60-an-hour limit and fail the next `satelle update`. (sty_02579374)

## [0.0.679] - 2026-10-17

### Added
- A repo can subscribe to published command-policy lists with `[[gate.command_policy]]`. Each subscription has a `name`, exactly one source, and an optional `max_age`. The source is a hosted catalog `workspace` + `path` (satelle.dev by default) or a third-party `url`.
  - satelle ships no list and fetches nothing until a repo subscribes.
  - Subscribed rules use the command-rule grammar and run after the repo's own `[[gate.command_rule]]` entries, so a repo rule for the same command wins.
  - The rules are cached per repo under the satelle home, never in the tree. Session start reads only that cache. When the cache is stale, session start starts at most one background `satelle policy refresh` and never waits on it. The command hook makes no network call.
  - A failed refresh keeps the last good list and warns at the next session start, naming the subscription and the reason. Failures include an unreachable source, a catalog body that does not match the catalog's checksum, and a list that does not parse.
  - A `url` source provides no checksum, so its list is labelled unverified.
  - See `satelle help command-rules`. (sty_041bf275)

### Fixed
- An accepted `satelle story amend` of a story's acceptance criteria survives re-entry to a step that applies the plan's criteria. Re-entering `in_progress` used to copy the plan's older criteria back over the amendment silently. Now the apply is skipped and the transition reports that the plan's criteria differ. (sty_89804a0e)

## [serve-v0.0.169] - 2026-10-17

### Changed
- The service binary carries the shared config change for command-policy subscriptions. (sty_041bf275)

## [0.0.678] - 2026-10-16

### Breaking
- **A command rule pattern token that holds `*` must now be one of the documented forms.** A `[[gate.command_rule]]` pattern written for 0.0.677 with a `*` inside a token (for example `a*`, `*a` or `*a*b*`), or with `**/NAME` anywhere but first, now fails to load. While it fails, commitgate refuses every shell command, naming the file, the rule index and the field `pattern`. To fix it by hand: run `satelle validate` to find the rule, then rewrite that token as an exact word, `*`, `**`, `**/NAME` (first token only) or `*TEXT*`. See `satelle help command-rules`. A pattern with no `*` inside a token is unaffected. (sty_f54540d1)

### Added
- Command rule patterns can match a program however it is invoked, and text anywhere in an argument. `**` matches any run of tokens. `**/NAME` as the first token matches a program whose last path element is `NAME`: a bare name, a path, `~/...` or `$HOME/...`. `*TEXT*` matches one token containing `TEXT`, such as a host inside a URL. Matching compares text only, with no PATH lookup or variable expansion, and adds no process. (sty_f54540d1)

## [serve-v0.0.168] - 2026-10-16

### Changed
- The service binary carries the shared config change: the command rule pattern grammar and its validation. (sty_f54540d1)

## [0.0.677] - 2026-10-16

### Added
- A repo can deny or warn on agent shell commands with its own `[[gate.command_rule]]` entries. Each entry has a `pattern`, an `action` (`deny` or `warn`), an optional `allow` list of statuses or steps, and a `message`. satelle ships no rules: with none configured, nothing is refused or warned. A deny refuses the command before it runs, naming the rule's message and what to engage. A warn lets the command run and tells the agent why (claude; grok, pi and cursor are recorded as unavailable, each with its reason). A deny with no `allow` list refuses in every status. Rules apply in the driving session, in dispatched agents and in fresh clones. Every matched allow or warn is recorded on the engaged story's ledger. `satelle help command-rules` has the format, and it notes that satelle judges only the command line it sees: wrappers and scripts bypass it, and the account a command runs as is what limits it. (sty_a74551d6)
- `satelle validate` and `satelle doctor` report an `allow` entry that names no status or step in the repo's workflows. (sty_a74551d6)

### Changed
- A malformed command rule, or a TOML syntax error in the repo's satelle config, now makes commitgate refuse every shell command. The refusal names the file, the rule index and the field, never the value, and says how to fix it. The Edit and Write tools stay open so the file can be fixed. Previously a broken config let read-only commands through. (sty_a74551d6)
- commitgate opens the store from the config it has already loaded, so a hook call starts git once on every path. (sty_a74551d6)

## [serve-v0.0.167] - 2026-10-16

### Changed
- The service binary carries the shared config and store changes: typed config load errors and opening the store from an already-loaded config. (sty_a74551d6)

## [0.0.676] - 2026-10-16

### Breaking
- **A `cloud` table now fails to load: satelle no longer manages cloud identity or credentials.** Signing in, choosing an identity and limiting its rights are your own tooling and the cloud's RBAC, not satelle's. A catalog profile or repo binding that declares `[profiles.<name>.cloud…]` or `[<seat>.cloud…]` is refused by every command that reads it, and by `satelle agent validate` and `satelle validate`, with a message naming the table. To migrate by hand: delete the `[profiles.<name>.cloud…]` table from `~/.satelle/agents.toml`, and any `[<seat>.cloud…]` table from the repo's `.satelle/workflows/agents.toml`. A `launch` table is refused the same way. Then run `satelle agents install all` to rewrite the installed launchers, which no longer call back into satelle. `$SATELLE_HOME/cloud/` is no longer written or read and can be deleted. (sty_c00782a3)

### Removed
- `satelle agents launch` and the hidden `satelle agents credential`, the launcher's cloud sign-in, and the `SATELLE_LAUNCHER` and `SATELLE_CLOUD_PROFILE` markers. (sty_c00782a3)
- The session-start warning that a session skipped the launcher, the `cloud identity` column of the capability table, and the `cloud identities` section of `satelle validate` (now an `agent catalog` load check). (sty_c00782a3)
- `satelle agents install` for pi and cursor reports `no satelle launcher for <name>` instead of `cloud identity: unavailable`. (sty_c00782a3)

## [serve-v0.0.166] - 2026-10-16

### Changed
- The service binary drops the cloud-identity profile schema; a `cloud` table is refused as in the CLI. (sty_c00782a3)

## [0.0.675] - 2026-10-16

### Fixed
- **A dirty working tree no longer turns a dispatched reviewer's accept into "no verdict".** The Stop hook's check for uncommitted changes with no engaged story now applies only to the driving session. A process satelle dispatched (reviewer, planner, coder or any other seat) is never blocked or noted by it, so its final output stays the verdict satelle reads. Before, a repo left with uncommitted changes and no story could not raise a new story: every create review was read as "no verdict". The driving session's own Stop still blocks on a dirty tree with no seat, as before. (sty_c7577f0e)

## [0.0.674] - 2026-10-15

### Fixed
- **A repo whose opted-in areas hold nothing to push is reported fully in step.** An area with nothing local and nothing on the hosted copy is now in step instead of 'never synced'. `satelle sync status` never suggests a `satelle sync push` that cannot change an area, and an area with hosted content but nothing local is behind, with NEXT `satelle sync pull`. doctor and the session-start warning agree. A freshly opted-in repo that holds only init scaffold, such as the id4me service repos, can now show every area in step. (sty_09b6b0e4)
- This completes epic:sync-empty-areas. (sty_e0a8d3a4)

## [0.0.673] - 2026-10-15

### Changed
- The review corpus covers create review. A rejected case, modelled on the cancelled sty_b61d2066 draft that hardcoded Azure and AWS sign-in into the binary, and an accepted generic-mechanism case pin the satelle repo's rule that user configuration is never hardcoded in the binary. The rule lives in this repo's own reviewers (create, intent, architecture and code-AC review) and principle, and satelle's built-in reviewers that other repos inherit are unchanged. (sty_476e9bf3, sty_82659a8a)

## [0.0.672] - 2026-10-15

### Added
- **An agent session can run cloud commands as its own Azure or AWS identity.** A profile in `~/.satelle/agents.toml` can name a `cloud` identity per provider. Its credential is a `${NAME}` reference into the machine's `[vars]` (or the gitignored `satelle.local.toml`), never a committed value. Start the session through a launcher from `satelle agents install` (`satelle-claude`, `satelle-grok`). It signs in as the identity the repo's in-loop profile names, then runs the harness. With no identity configured, it behaves exactly as before. See `satelle help global-agents`. (sty_b61d2066)
- **satelle never puts the long-lived secret in the agent's environment, argv, repo or logs.** On AWS the agent receives only STS session credentials, through `credential_process`. On Azure the `az` CLI keeps the service principal's secret in its own cache under the profile's `AZURE_CONFIG_DIR`. The agent runs as the same OS user, so only a separate OS user truly isolates a secret. satelle selects the identity; it does not enforce rights. (sty_b61d2066)
- When a repo's profile names an identity but the session was not started through the launcher, session start warns the agent. The warning reads local state only. pi and cursor have no launcher, so they report `cloud identity: unavailable`. (sty_b61d2066)
- `satelle validate` and `satelle agent validate` reject a malformed identity, an unknown provider or a literal secret, without echoing the value. (sty_b61d2066)

## [serve-v0.0.165] - 2026-10-15

### Changed
- The service binary carries the cloud-identity profile schema and validation. (sty_b61d2066)

## [0.0.671] - 2026-10-15

### Fixed
- **`satelle init` and `satelle migrate` (including the `satelle init` an update tells you to run) no longer leave `.gitignore` modified when nothing is missing.** satelle adds an entry only when it is needed and the file has no setting for it, and it never rewrites the managed block to a release's new wording. The defaults are `.satelle/` and the harness dirs satelle deploys into (`.claude/`, `.grok/`, `.pi/`, `.cursor/`). An operator rule for one of them, including a `!` negation, is kept as written. A dir whose files git tracks (for example, a committed `.claude`) is never ignored. A repo that already ignores what satelle needs sees no change, and a fresh `init` settles `.gitignore` in one run. (sty_dbc0be85)

### Changed
- The integration suite starts every child process outside any satelle repo by default, and a guard test fails if that default is removed. A verb that behaves differently inside a repo is therefore exercised the same way in a worktree, in CI and in an operator checkout that has `.satelle`. The per-test working-directory workarounds are removed. (sty_5f3d3da6)
- The plan skill and the architecture review name the kinds of existing owner a plan must search for: where state is stored, how a process is spawned or detached, how a root or pin is detected, which constant decides freshness or liveness, and which record already holds the same fact. (sty_32917083)
- This completes epic:currency-advisory-followup. (sty_19849046)

## [0.0.670] - 2026-10-14

### Added
- **A session start tells the user when this machine's substrate is behind, unpushed or unmerged**, and names the command that brings it in step, on the same user notice as the release line. The line comes from recorded state only and never waits on the server. It is marked unverified exactly when `satelle doctor` reports the area unchecked. A stale record starts one detached `satelle sync status`, at most once every 10 minutes. `.local` files are never counted as drift. (sty_f1a3851d)
- **Stories held on another machine or by a remote agent are visible.** Every work-state pass, including the since-mode snapshot satelled runs, records which stories another location holds (with its label and when it was last seen) and clears released or finished holds. A session start, `satelle story get` and `satelle story list` show them. Stories whose local and hosted copies differ in content are recorded as conflicts and named at session start. They are never resolved silently, and they clear once the copies agree. (sty_6e5fc41c)
- **`satelle story hold takeover` waits until the holder is out of the way.** The hold moves only when the holder released it, the story is terminal on the hosted copy, or the holder was last seen longer ago than `[sync] hold_stale_after` (default 2h, seeded into `satelle.toml` by `satelle init`). In addition, the story's status and code must both be in step. A holder whose work started but whose produced code is not reachable here is refused, because the code may exist only in its tree. `--force` overrides the refusal and is recorded on the ledger. See `satelle help story-hold`. (sty_6e5fc41c)

### Changed
- The session-start notice composes the release, substrate and story-sync lines as one user notice (Claude Code `systemMessage`, pi `notify()`). Cursor and Grok have no user-facing channel, so they carry it in model context only, and Grok gets it at its first tool event. (sty_f1a3851d, sty_6e5fc41c)
- The update integration tests run outside any repo, so a checkout's own `.satelle` no longer makes the post-update assessment exec their fake binary. (sty_5c820360)
- This completes epic:currency-advisory: a session start checks the binary (0.0.669), the repo substrate and the stories, and tells the user what to run when any is out of date. (sty_5c820360)

## [serve-v0.0.164] - 2026-10-14

### Changed
- The service binary carries the hold-recording snapshot pass, the extended hold registry and the story-conflict record. (sty_6e5fc41c)

## [0.0.669] - 2026-10-14

### Added
- **`satelle update` assesses the repo right after installing.** Inside a satelle repo it runs the newly installed binary's `satelle doctor`, so the repo is judged against the new default substrate in the same step. A failing assessment exits non-zero and names `satelle init`. The install is kept. In a repo pinned with `.satelle/satelle`, update says the repo runs its pinned binary and names `satelle update --local` instead. Outside a repo, including `$HOME` with its global `~/.satelle`, no assessment runs. The guidance for other registered repos is unchanged. Update now prints one `satelle <ver> · satelled <ver>` line. (sty_15a374a8)
- **A session start tells the user when a newer satelle or satelled release exists.** The check reads a machine-wide release cache under the satelle home and never waits on the network. A cache older than 24h, or a missing one, starts one detached `satelle update --check`, at most once every 10 minutes. A line from a stale cache is marked unverified. A dev build, or a binary ahead of the latest release, stays silent. `satelle update` and `satelle update --check` write the cache. (sty_15a374a8)

### Changed
- Session-start advice reaches the user, not only the agent. Claude Code shows it as a hook `systemMessage`, and pi shows it through `notify()`. Cursor and Grok have no proven user-facing session-start field, so their advice stays in the model's context. Every harness's hook output shape (the context envelope and the Stop allow-with-note) is now built in one place. (sty_15a374a8)

## [serve-v0.0.163] - 2026-10-14

### Changed
- The service binary carries the shared hook-output envelope and the global-home path helper. (sty_15a374a8)

## [0.0.668] - 2026-10-13

### Fixed
- `satelle doctor` and `satelle agent validate` no longer warn that a step's rework consult binding "grants shell … never used". The consult is a live session that reads state its seed does not carry (an attachment re-attached mid-relay, the working tree), so following the warning would have blinded it. A reviewer binding whose shell grant no gate, advisor or rework consult uses is still warned. (sty_e6fd97d9)

## [0.0.667] - 2026-10-13

### Added
- **`satelle sync pull`**: configuration that is only behind the hosted copy is brought in as a fast-forward, through the same area-restricted snapshot path reconcile uses, so nothing local is ever overwritten; every other area is reported with its next command (unpushed -> `satelle sync push`, diverged -> `satelle sync reconcile`). satelled runs it as `--fast-forward-only` beside its automatic push, so a machine catches up on its own. A refused push now tells behind (`satelle sync pull`) from diverged (`satelle sync reconcile`), and status, doctor and reconcile name the git-aligned verbs. (sty_0fa4f732)

### Changed
- Ledger rows and executions are pushed append-only: a story held on another machine no longer stops this machine's ledger entries, and executions are never held (one predicate covers engage, push, pending claims, held-back refresh and the hold verbs). A record the server still refuses is skipped and named without aborting the push. Story fields as ledger events and server-side execution holds are follow-ups (sty_d5dd2c14, sty_1cb9f98f). (sty_52b93691)

### Fixed
- `satelle init`'s role migration adds `role=` to the binding's own table, never inside a sub-table such as `[coder.env]`. (sty_7356785b)
- This completes epic:repo-substrate-sync: a repo's configuration travels to every machine identically, trunk-style. Refused pushes name reconcile (0.0.663); machine-specific agent fields live in `agents.local.toml` (0.0.664); configuration is validated before push and the hosted copy is exactly what was pushed (0.0.665); unedited embedded defaults never travel (0.0.666); and behind configuration is pulled on its own, operational data is append-only, and init's migration is fixed (this release). (sty_0abdcc03)

## [serve-v0.0.162] - 2026-10-13

### Changed
- satelled's reconcile pass also runs `satelle sync pull --fast-forward-only`, and the service binary carries the append-only work-state push. (sty_0fa4f732, sty_52b93691)

## [0.0.666] - 2026-10-13

### Changed
- **Only substrate you edited or authored is versioned and synced; an unedited embedded default always comes from the installed binary.** A file that is an unedited copy of an embedded default (a self-consistent stamp, or byte-identical to the default) is left out of every push bundle and of every local-side comparison (`satelle sync status`, pull planning, reconcile), so upgrading the binary on one machine no longer looks like a substrate edit that conflicts with another, and a pull no longer replaces a newer default with an older materialised copy. The push output names such files separately from `.local` ones. (sty_097833e8)
- `satelle init` and `satelle rebase` no longer write copies of the embedded default tasks; they resolve from the binary, an existing unedited copy is still converged in place, and `satelle substrate prune` and `satelle migrate` now remove unedited task copies through one shared enumeration. (sty_097833e8)

## [serve-v0.0.161] - 2026-10-13

### Changed
- The service binary leaves unedited embedded defaults out of the substrate it pushes. (sty_097833e8)

## [0.0.665] - 2026-10-13

### Breaking
- A push of configuration (`satelle sync config push`, satelled's automatic push, `satelle publish`, `satelle sync bindings push`) now refuses a shared `agents.toml` that carries machine-specific content instead of silently stripping it: `profile=` or `harness=`, an absolute command path, a literal (non-`${VAR}`) value in a binding's `env` or a settings `env` table, a secret-shaped settings key, an absolute path anywhere in settings, or `use_global_roles`. To heal: run `satelle agent validate` to list each value, move those fields into `agents.local.toml` beside `agents.toml` (it overrides field by field and never leaves the machine), then push again. `satelle sync status` and `satelle sync reconcile` keep working meanwhile.

### Changed
- **Configuration is validated before it is pushed, and the hosted copy is exactly what was pushed.** Push runs the same deployed-system validation `satelle init` runs (without this machine's local and pulled workspace agent layers) and refuses before any network call; the pushed bytes are the local file's bytes. The only exception is `satelle.toml`'s `[sync]`/`[hosted]` project binding, which stays on the machine and is now named in the push output. Pull and deploy no longer merge local values back into a redacted file; they take the hosted content as is or refuse. (sty_89b9df20)
- `satelle agent validate` and `satelle doctor` warn on every machine-specific value in the shared file, using the same checker the push uses. (sty_89b9df20)

## [serve-v0.0.160] - 2026-10-13

### Changed
- The service binary carries the pre-push configuration checker. (sty_89b9df20)

## [0.0.664] - 2026-10-13

### Added
- **`agents.local.toml`: machine-specific agent configuration that never leaves the machine.** A file beside the repo's shared `agents.toml` overrides it field by field for this machine only (command paths, `profile=` references into the machine catalog, `[defaults]` such as `use_global_roles`). It never syncs, satelle init's managed gitignore block ignores it, and it is not indexed as a document. The resolver loads the repo, workspace and local layers through one owner, used by dispatch, `satelle agent validate`, `satelle doctor` and `satelle workflow show`. Only agent bindings get a local layer; workflows, principles, skills and settings do not. (sty_5c56143b)
- `satelle agent validate` and `satelle doctor` show, for every effective agent field, the layer that supplied it and the file to edit (`source: model = "opus" ← local (<path to the local file>)`). (sty_5c56143b)
- A shared `agents.toml` that names `profile=` or sets `use_global_roles = true` depends on one machine's catalog; doctor and agent validate now warn and point at `agents.local.toml`. (sty_5c56143b)

## [serve-v0.0.159] - 2026-10-13

### Changed
- The service binary resolves agent bindings with the machine-local agents layer. (sty_5c56143b)

## [0.0.663] - 2026-10-13

### Fixed
- A refused substrate push now names `satelle sync reconcile` as the way forward. Config and documents pushes refused because the hosted copy is ahead or has changes not yet applied here (including satelled's automatic `--snapshot-only` push) used to say `run "satelle sync rehydrate" first` or `satelle sync documents pull`, which overwrite the local tree; reconcile shows both sides and backs up the overridden one. The remedy is fixed in one place, so no push path can name a different command. (sty_aeff3dc7)

## [0.0.662] - 2026-10-13

### Changed
- This completes epic:substrate-lean, the follow-up to epic:substrate-hygiene: a rule every reviewer obeys is stated once, in the embedded `satelle-reviewer-verdict` principle delivered with every judging dispatch (0.0.660), the override-skill test holds authored overrides to that single source (0.0.661), and this repo's authored substrate dropped its own copies and its dead and duplicated references and budgets every dispatched seat and the per-prompt reminder, so `satelle doctor` reports no duplicated guidance, unresolved reference, stale index entry or unbudgeted seat. (sty_c9514d92)

## [0.0.661] - 2026-10-13

### Fixed
- The test that reads a checkout's authored reviewer-skill overrides now holds them to the single-source verdict rules: an override that restates the `satelle-reviewer-verdict` rules is what it flags, matching the embedded-skills test, instead of demanding the old copies. It failed on any developer machine whose substrate followed the new rule; CI skips it because the authored substrate is not in the checkout. (sty_60fe625e)

## [0.0.660] - 2026-10-13

### Changed
- **Every gate reviewer receives the verdict rules from one embedded principle, `satelle-reviewer-verdict`.** The re-presented-edge (quotation) rules and the rule that every verdict sets `reviewed` were restated verbatim in eleven embedded reviewer skills; they now live once and are appended to every judging dispatch, single-gate and bundled, after the judging briefing and before the rubric. Non-judging dispatches (performers, consultants, the always-on summary seats) do not receive it. Each skill keeps its own verdict JSON shape and points at the principle. A repo overrides the rules by authoring its own principle of the same name. On the review corpus the verdicts are unchanged (recall 5/6, false blockers 1/6, escapes 0/6 before and after). (sty_79df1e7e)
- The injected-size estimate (`satelle validate`) and `satelle doctor`'s seat audit add the shared verdict rules only to seats that judge, keyed on judging rather than seat kind, so both report what a dispatch actually sends. (sty_79df1e7e)

## [serve-v0.0.158] - 2026-10-13

### Changed
- The service binary carries the shared reviewer verdict rules and the judging-keyed seat prompt composition. (sty_79df1e7e)

## [0.0.659] - 2026-10-13

### Added
- This completes epic:substrate-continuity: a developer's authored substrate now has the same continuity as their work state. Story attachments, change patches included, travel with work-state sync (0.0.654); a machine knows before work starts whether its substrate is unpushed, behind or diverged, and `satelle doctor` fails on it (0.0.655); diverged machines are reconciled deliberately with `satelle sync reconcile`, the overridden side saved first (0.0.656); satelled pushes authored config and documents on its own and never over a behind or diverged hosted copy (0.0.657); and `satelle sync status --story` with the `satelle-substrate-pushed-check` gate keeps a story from closing while the substrate it changed exists only on one machine (0.0.658). (sty_e4c22781)

## [0.0.658] - 2026-10-13

### Added
- **`satelle sync status --story <id>`** answers whether the authored substrate a story changed is on the hosted copy. Its touched paths are the union of the story's recorded change sets and its live substrate diff, so edits made in the step being closed count before any change record exists; a story engaged from a linked worktree is mapped and probed against the main tree. It exits non-zero, naming the area, its state, the touched paths and the next command, when a touched area is unpushed, never synced, diverged, unmerged or unavailable; it passes in step and behind (the hosted copy already holds the change); a story that touched no synced substrate passes without contacting the server. (sty_fcfe32e5)
- **`satelle-substrate-pushed-check`**, an embedded functional-check skill that runs `satelle sync status --story <id>` on the edge it is named on and accepts only on exit 0. It is not wired into the embedded route; a repo adds it to a step's `reviewers = [...]` in `step.toml`. (sty_fcfe32e5)

### Changed
- A repo-relative tree path maps to its sync area through one helper built on the area-location owner, so relocated `[substrate_roots]`, a non-default data dir, the settings, agents and constitution files are all recognised. (sty_fcfe32e5)

## [serve-v0.0.157] - 2026-10-13

### Changed
- The service binary carries the shared repo-path-to-area mapping used by `satelle sync status --story`. (sty_fcfe32e5)

## [0.0.657] - 2026-10-13

### Added
- **Authored substrate and documents now reach the personal project on their own, the way work state does.** satelled's reconcile pass, after the work-state push, runs `satelle sync config push --snapshot-only` and `satelle sync documents push --snapshot-only` for every registered repo, so an authored change (by a verb or a direct file edit) is on the hosted copy within one reconcile interval with no `satelle sync`. It never pulls, deploys, or passes `--force`/`--prune`; a repo whose areas are all local is a silent no-op, and the push verbs keep sole ownership of opt-in and project binding. (sty_a66e1846)
- **`--snapshot-only` on `satelle sync config push` and `satelle sync documents push`** refuses, uploading nothing and exiting non-zero, when an area is behind, diverged or unmerged, or when the hosted snapshot or manifest cannot be read, instead of falling back to a plain per-file upload. A behind, diverged or unmerged refusal is recorded so `satelle sync status`, the engagement advisory and `satelle doctor` report it; an unavailable refusal records nothing and shows as a live `unavailable`. Without the flag the verbs behave as before. (sty_a66e1846)

## [serve-v0.0.156] - 2026-10-13

### Changed
- satelled's reconcile pass pushes opted-in authored config and documents with `--snapshot-only` after the work-state push, logging one line per failure streak, so a diverged machine is never overwritten and never fills the log. (sty_a66e1846)

## [0.0.656] - 2026-10-13

### Added
- **`satelle sync reconcile`** brings two machines whose substrate has diverged back in step deliberately. Run bare, it only reads: for every opted-in area (or those named) it lists each path whose local and hosted copies differ, its class (changed here only, changed on hosted only, changed on both, added or deleted on either side, still unmerged) and what a plain pull would do. `--diff <area>/<path>` prints a unified diff of the hosted copy against the local one, labelled with the hosted snapshot. `--apply` carries out the pull with per-path `--take-local`/`--take-hosted` or per-area `--take-local-all`/`--take-hosted-all` choices; an unchosen path changed on both sides is parked as a conflict copy, and a choice that cannot be honoured is refused by name before any file is written. Whatever a choice replaces is first saved through the substrate backup under the runtime `backups/sync-replaced/{local,hosted}/<area>/<path>`. It never pushes: run `satelle sync` afterwards, with no `--force`. Settings keep the local project binding and agents are rehydrated, as a deploy does. (sty_b0873ec3)

### Changed
- `satelle sync status` and `satelle doctor` name `satelle sync reconcile` as the next step for a diverged or unmerged area, in place of deploy. (sty_b0873ec3)
- `satelle workflow` line diffs are now unified diffs from the shared diff helper. (sty_b0873ec3)

## [0.0.655] - 2026-10-12

### Added
- **`satelle sync status`** contacts the bound project and prints, for every non-local config area and `documents`, whether this machine is `in step`, `unpushed`, `behind`, `diverged`, `unmerged`, `never synced` or `unavailable`, with the local base version, the hosted effective version and the next command to run. The state comes from the same push-planning path `satelle sync` uses, so an orphaned record is not reported as behind and a pending pull is. It exits non-zero when any area is not in step. (sty_7562055c)
- **Substrate drift is visible before work starts.** Engaging a story into a performing state runs that check within a short timeout and prints one advisory line naming each out-of-step area and the command to run; offline or unbound it uses the recorded state and says `unverified (offline/unbound)`. It never refuses engagement. The SessionStart context prints the same line from recorded state, and an in-step machine prints nothing. (sty_7562055c)
- **`satelle doctor` reports unpushed, unmerged, behind or diverged substrate as an error**, without contacting the server: local files that differ from the last synced base, unmerged paths in the base, a recorded behind/diverged check, or a check older than `[sync] stale_after`. An unbound server or project with a non-local area names `satelle login` / `satelle project bind`. With `stale_after` unset it warns that substrate staleness is not judged. (sty_7562055c)

### Changed
- Every `satelle sync status` run and every config/documents push, pull, deploy and rehydrate records each handled area's state, derived from the push-planning path after the operation rather than assumed from its success. (sty_7562055c)
- `satelle sync rehydrate` restores `satelle.toml` when the bound project's settings snapshot names it; when it does not, the note names the hosted key and points to `satelle sync status`. (sty_7562055c)

## [serve-v0.0.155] - 2026-10-12

### Changed
- The service binary carries the shared sync-status and substrate-health packages used by `satelle sync status` and `satelle doctor`. (sty_7562055c)

## [0.0.654] - 2026-10-12

### Added
- **A story's attached documents now travel with the story.** When `[sync] stories` is personal or shared, `satelle sync workstate push` (which satelled already runs) uploads every markdown attachment of the local stories, including the `type: change` patch, into the bound project's personal collection; `satelle sync workstate pull` and `snapshot` write them into the other machine's attachment store, so `satelle story doc <id> <name>` returns the same body there. Which files upload is decided by a per-file uploaded-state record, not by the ledger area or the rows a push sent, so attachments made before this release upload on the first push after upgrade with no extra command. The attachment leg runs on every exit path of the push, skips held and out-of-scope stories, and retries a failed upload on the next push. A pull never silently overwrites a local attachment that differs from the hosted copy (`--force` lets hosted win). Binary attachments stay local and are reported as not mirrored. This reverses sty_948ad5df's "patches stay local" decision. (sty_4d42e13e)

## [0.0.653] - 2026-10-12

### Added
- **Every agent satelle starts receives all of its resident context within its harness budget.** Context is fitted when it is assembled, along a configured ladder (full → filler-dropped → lead → pointer): the lead rung keeps headings, code, each paragraph's and list item's first sentence and the whole clause around every protected token. A condensed body carries a `satelle retrieve` marker that returns the authored text byte for byte, so the authored constitution, principles and skills stay as long and readable as their authors write them. The SessionStart render and every dispatched seat's payload go through one shared fit (`internal/condense`); a seat's budget is `[validate.injected.budget]` in estimated tokens, and `satelle validate` and `satelle doctor` report the fitted size. A budgeted seat that still needs the pointer rung is the error finding `context.dispatch.pointered`. (sty_df21b2db)
- **The condensing rules are configuration.** The embedded `condense.toml` (drop phrases, protected patterns, clause delimiters, sentence terminators, sentence exceptions and ladder order) can be overridden per repo under `.satelle/config/condense.toml`; Go recognises only markdown structure. `satelle substrate condense` shows what the ladder does to a document. (sty_df21b2db)
- **`satelle review score --seat-budget <tokens>`** scores a reviewer binding with a chosen seat budget (`0` turns the ladder off, `-1` uses the repo's budget), so condensed and full prompts can be compared with `satelle review compare`. (sty_df21b2db)
- **`satelle-substrate-simplify-audit`**, an embedded proposal-only skill with the task `tsk_substrate-simplify-audit`, proposes terse rewrites of the constitution, principles, skills and workflows under the condensing rules and never applies them. (sty_df21b2db)

## [serve-v0.0.154] - 2026-10-12

### Changed
- The service binary carries the shared context-fitting package used by the SessionStart hook and dispatched seats. (sty_df21b2db)

## [0.0.652] - 2026-10-12

### Added
- **`satelle doctor` checks the context every agent actually receives.** It renders the SessionStart context once per harness the repo binds, with the same renderer and limit as `satelle hook context --harness <h>`, and warns when a document is replaced by a read pointer, naming the harness, its limit, the rendered bytes and each omitted document. It reports the size of each dispatched seat's context and of the per-prompt reminder against budgets from configuration (`[validate.injected.budget]`, or the harness's `tool_context_limit_chars`): over budget is a WARN, no budget is an INFO naming the row as unbudgeted, and no budget number lives in Go. It reports guidance copied verbatim between the parts of a context or from any principle or skill in the substrate, including on-demand ones the context never links; the overlap threshold is `duplicate_min_words` under `[doctor]` (embedded `doctor.toml`, overridable). It reports unresolved satelle references (unknown or retired `[[name]]` and backticked `satelle-…` names), skills nothing wires, and `documents/index.md` entries whose document is gone. (sty_a17afa15)
- **`satelle doctor --context-review`** (opt-in, like `--live`) runs the semantic half: for each assembled context it spawns the repo's reviewer binding read-only with the `satelle-context-audit` rubric, resolved disk-first so a repo-authored copy of that skill overrides the embedded default, and reports each paraphrased restatement as a finding. Plain `satelle doctor` spawns no agent. (sty_a17afa15)

### Changed
- **The resident-principle ceiling is judged against every bound harness**, not the largest of any harness. `satelle init` and `satelle principle validate` report a set that overflows a bound harness's limit as a WARN for that harness, so neither fails on it. (sty_a17afa15)
- `[[name]]` resolution has one implementation (`internal/ctxaudit`), shared by `satelle validate` and `satelle doctor`; `satelle validate` output is unchanged. The retired-name registry gains rows for the bare structure reviewers `satelle-skill-review`, `satelle-workflow-review`, `satelle-principle-review` and `satelle-story-review`. (sty_a17afa15)

## [serve-v0.0.153] - 2026-10-12

### Changed
- The service binary carries the shared context-audit packages used by `satelle doctor`. (sty_a17afa15)

## [0.0.651] - 2026-10-11

### Added
- **`satelle status` names the hosted server and the hosted project this checkout syncs to.** Two rows follow `web service`: `hosted server` (the machine `[hosted] server`, else https://satelle.dev) and `hosted project` (the explicit `[sync] project` binding, or "not bound", naming the directory-derived slug sync would use and `satelle project bind <slug>`). Read from config alone, with no network call and no sign-in needed. (sty_1c4ad7e2)
- **`satelle status` names the identity source.** An `identity` row says whether satelle acts through a session token (`SATELLE_TOKEN`), a stored login (`satelle login`), or no hosted identity (local-only). It reads one local credential file and prints no credential, account or fingerprint. (sty_e82d5f49)
- This completes epic:remote-proof-epic, raised by the remote-Ready proof on a Fly.io Sprite: a remote node can now be checked with `satelle status` alone. (sty_6ffa494a)

## [0.0.650] - 2026-10-11

### Added
- **Sync one story or epic by id.** `satelle sync workstate` push, pull and snapshot accept story ids: push sends only those work items and the ledger rows whose story id is among them, and pull uses the server's story-scoped, paged Snapshot (satelle-server 0.1.159, `story_ids`) and applies the same reconcile as a full pull. A scoped pass never touches the project's since-cursor or checkpoint and never falls back to a full pass. An epic container id expands to its members by the epic tag from the local store; a container with no local members is named with a notice to pass the member ids. An unknown id, or a server that ignores the scope, is refused with a message saying which. The canonical proto adopts `repeated string story_ids = 6`. (sty_991b6900)
- This completes epic:sync-paging: the client pages Snapshot and sends only what changed since its last pass (0.0.648, 0.0.649), and can sync a named story or epic. (sty_af63104e)

## [0.0.649] - 2026-10-11

### Fixed
- **Work-state Snapshot is paged, and a synced client receives only what changed.** `satelle sync workstate snapshot`, `satelle sync workstate pull` and `satelle sync rehydrate` request Snapshot in pages of 500 rows and follow `next_page_token` to the end of the pass, instead of asking for the whole project in one message (about 85,000 ledger rows for the satelle project). After a complete pass, its `sync_token` is saved per server, project and repo root in `~/.config/satelle/document-sync-state.json` and sent as `since_token` on the next pass, so an already-synced client receives only changes: against satelle.dev a full pass is 171 pages and the next since pass is 1 page. Each page is applied as it arrives, and an interrupted pass (for example the reconcile loop's 2-minute timeout) keeps its applied pages and a resume checkpoint, so the next pass continues from there. A repeated row is applied once. The client runs a full pass when no token is stored, when `--force` is given, when the opted-in areas or the local store changed, or when the server rejects the token; in the last three cases it prints one line on stderr saying the token was discarded and why. The summary names the pass kind (full, since or resumed) and the page count. While an older satelle binary still runs against the same state file (for example a not-yet-updated service), it can drop the saved token; the next pass is then a full pass. `satelle update` brings the CLI and the service to this version together. (sty_2cb4e1f6)

## [serve-v0.0.152] - 2026-10-11

### Changed
- The serve reconcile loop runs the paged, since-cursor `satelle sync workstate snapshot` each interval, so a periodic pass no longer pulls the whole project. (sty_2cb4e1f6)

## [0.0.648] - 2026-10-11

### Changed
- **The canonical sync proto carries Snapshot paging and since-cursor fields at satelle-server's numbers.** `api/checkout_sync.proto` adds SnapshotRequest `page_size = 3`, `page_token = 4`, `since_token = 5` and SnapshotResponse `next_page_token = 3`, `sync_token = 4`, matching satelle-server's published contract; existing fields keep their numbers. The proto comment documents the meaning (page_size 0 keeps the unpaged contract; tokens are opaque; a repeated row is possible, a missed row is not), `internal/hosted/syncpb` is regenerated, the `api` JSON mirror gains the same fields, and the README no longer calls Snapshot unpaged. No caller sends the new fields yet. (sty_70ba9615)

## [0.0.647] - 2026-10-11

### Added
- **A story's cost is priced at read time from its tokens per agent and model.** Tokens stay the recorded truth. `satelle story cost` and the web cost panel show a BY SEAT section (agent and model, with the driving session as `driver@<model>`) and one derived cost, computed each time it is read from `[pricing.models."<model id>"]` rates (USD per million fresh input, cache read, cache write and output tokens) or a `[pricing.subscription]` (`fee_usd`, `tokens_per_period`) in `~/.satelle/config.toml`. Changing the configuration re-prices past stories with no ledger write. A model with no configured rate reads as unavailable, naming the model, never as zero; with no configuration the cost reads `unavailable (no pricing configured)`. Provider-reported dollars are labelled `reported`. No price table exists in Go, and a test enforces it. See `satelle help cost-pricing`. (sty_6267abb7)
- **The estimate gate judges the comparison, not the presence of a number.** `satelle story comparables <id>` lists comparable finished stories (same category first, then shared tags) with their measured token actuals. `satelle-estimate-actual-review` compares each token estimate against them, names the comparables, their median and the ratio, and rejects an estimate outside the tolerance. The tolerance and minimum comparables are repo configuration under `[estimate_review]` in `.satelle/satelle.toml` (defaults 1.5 and 3); too few comparables accepts with a "no comparison available" note. A passing functional check now keeps its bounded output in its verdict notes. (sty_e00e6a7a)
- **A backlog definition edit is judged and recorded.** Changing a backlog story's title, body, acceptance criteria or category runs the create-review gate (a reject leaves the stored text unchanged), and the `story_updated` row records the editor's session and executable and each field's before and after. (sty_a8885220)

### Fixed
- **A driving session run as a subagent or from a worktree records its usage.** The session record is found by session id across trees (story tree, the session's published root, then a store-wide search) for claude, grok and pi. Claude subagent transcripts are included, deduplicated by message id. A row whose window overlaps another story in the same session is marked ambiguous, naming the overlapping stories, and kept out of the exact totals. grok and cursor rows say subagent spend inclusion is unverified. A record found nowhere, or more than once, is an adapter-named unavailable. (sty_6deecf76)
- **Create-review and amend-review runs are recorded on the ledger** like every other reviewer dispatch, so their spend appears in story cost and the gate-value view. (sty_c5171eb9)
- **A functional check's verdict notes carry its result, not its script.** An accept reads `functional check passed (<skill>)` with the check's output; a reject keeps its full failure output. (sty_e2b78a8d)

### Changed
- epic:token-accountability's criteria now treat tokens per agent as the true cost, with dollars derived from them at read time. (sty_1e037210)

## [serve-v0.0.151] - 2026-10-11

### Changed
- The serve binary carries the per-seat cost view with read-time pricing, the ambiguous-driver-row classification and the cost-pricing help topic. (sty_1e037210)

## [0.0.646] - 2026-10-11

### Added
- **A dispatched pi binding is treated as pi.** One classifier names the adapter, so a dispatched pi command is no longer recorded as an unknown command. Its usage, cache split, cost and resolved model are read from pi's own session record, or recorded as a pi-named unavailable reason. pi's `read` tool counts as a context channel, and isolation preflight checks pi's `--tools` / `--exclude-tools` against the grant. Usage ledger rows now name their adapter. (sty_58a9bdc8)
- **A rework relay stops when a round brings no new evidence.** When a performer reply changes nothing (no file change, attachment or counted ledger row) and the consultant's objection is unchanged, the relay ends with a `no_progress` outcome instead of spending its budget. The ledger row records the outcome, rounds, budget and last objection. Which ledger rows count as evidence is decided in one place, `verb.IsEvidenceRow`, which the stale-verdict check now uses too. (sty_e596be56)

### Fixed
- **A gate agent that fails records why.** Retry, failure and timeout telemetry rows on the reviewer, executor and summariser paths carry a redacted, bounded `cause`, and a cause the telemetry validator refuses no longer drops the row. (sty_9bc496e1)
- **A story moved back to a non-performing state frees its engagement seat**, so the next story engages without a manual `satelle story seat release`. (sty_e6755855)

### Changed
- This completes epic:improve-loop-followup: the improve-loop retrospective follow-ups and the remote-Ready proof findings. The planner now records where the rule or seam it touches already lives before designing, and the architecture review owns the single-ownership check (authored substrate, sty_650d0834). (sty_3705b8bb)

## [serve-v0.0.150] - 2026-10-11

### Changed
- The serve binary carries the pi adapter classification and usage reading, the adapter-aware context-channel rule and the agent-dispatch help topic. (sty_3705b8bb)

## [0.0.645] - 2026-10-11

### Fixed
- **A container's readiness performer receives the container's children.** A dispatched performer step — local executor dispatch and the cloud/remote performer prompt alike — now carries the container's children (id and status) in its payload, from the same resolver the reviewer, retrospective and chat payloads use, so an epic readied by a performer with read-only tools is judged on its real children. Both performer sites build their payload through one shared builder. (sty_b91dc1ac)

## [0.0.644] - 2026-10-11

### Added
- **A malformed readiness plan is refused before any reviewer runs.** A plan contract that declares `output_criteria_section` now has its `## Acceptance criteria` section checked: any line that is not a numbered criterion (a stray bullet, a tool line-number prefix) refuses the edge with the offending lines named, before the reviewers are dispatched. (sty_44dd5159)
- **The accepted plan's criteria are applied when implementation starts.** A step that declares `apply_criteria` copies the accepted plan's `## Acceptance criteria` section onto the story verbatim, recorded as a definition edit from the plan, and the entry gates judge the applied text. (sty_4d9df9a0)
- **A container's retrospective runs the advisor its route declares and sees its children.** `satelle story retrospect` runs the route's `advise = { agent, skill }` for the item's current step (falling back to the built-in retrospective). A container's payload carries its children. The embedded `satelle-epic-retrospective` skill drafts each finding as a story classified bug or context under a new follow-up epic, implementing nothing. (sty_da018b06)

### Fixed
- **A gate re-judges when evidence was recorded after its last verdict.** A verdict now records when its presentation cycle ended. A later story log or attach marks the previous verdict stale instead of letting the gate re-issue it unchanged. The gate's own bookkeeping rows never count. (sty_0225fc2f)
- **A rework relay opens with the performer when it has an unanswered directed message**, and a consultant's READY cannot end the relay while that message is unanswered. (sty_ec74ab22)
- **A story's change set is measured from its engagement baseline**, not from a park/resume re-anchor written while it sat in backlog. (sty_12ce4271)
- **`satelle ledger list` shows a story's newest rows** and says on stderr how many older rows were left out. Gates see the same window and are told when rows were omitted. An oldest-first paging mode remains for readers that walk a whole ledger, such as review capture. (sty_523edea7)
- **A story engaged from its own worktree is not refused because another engaged story is editing the main tree.** The trunk line names whose changes they are; a dirty main no engaged story accounts for is still refused. (sty_f1db1260)

### Changed
- This completes epic:improve-loop. Its reviewer comparison chose opus as this repo's gate reviewer: same measured quality as Fable at about a third of the cost. (sty_3ca9c3c9)

## [serve-v0.0.149] - 2026-10-11

### Changed
- The serve binary carries the ledger newest-window reader, the prior-verdict staleness rule and the plan-criteria checks. (sty_3ca9c3c9)

## [0.0.643] - 2026-10-11

### Added
- **`satelle review score`, `capture` and `compare` measure a reviewer's quality, not its rejection count.** `review score --binding <name> --corpus <dir> [--replay <dir>]` runs a reviewer binding through the same isolated path a real gate uses over cases whose right verdict is known, and reports per rubric and in total: recall (defects rejected), false blockers (valid cases rejected), escapes (defects accepted), whether its notes cite the known defect, wall time, and tokens and cost — or the adapter's named reason when a CLI reports none. `review capture <story> --ledger <evt> --expect accept|reject` turns a recorded gate row into a replay case (reconstructed from the ledger, which keeps only the payload size). `review compare` sets two reports side by side and refuses reports scored over different cases. The frozen review corpus now lives in `internal/reviewscore`, with `tests/reviewcorpus` re-exporting it. (sty_29741ad6)

## [serve-v0.0.148] - 2026-10-11

### Changed
- The serve binary carries the shared review-corpus model and the ledger replay readers. (sty_29741ad6)

## [0.0.642] - 2026-10-10

### Added
- **`satelle trunk publish` releases onto a main other machines have moved.** It merges origin/main into the release (no history rewrite, so the release's commits and an epic's merge commits stay ancestors), runs an optional `[trunk] stamp` command on the integrated tree and the `--prove` / `[trunk] prove` command on the combined head, then pushes with one plain push. A push refused because main moved again is answered by another merge-and-prove round, up to `[trunk] publish_rounds` (default 5); it never forces. A conflict lists the unmerged paths, and any other merge failure, failed proof or spent bound exits non-zero with trunk restored and the remote untouched. `--story` ledgers the combined and pushed heads as `trunk_publish`. (sty_6af229f1, sty_f77b40bb)
- **Cutting an epic child from the trunk starts from main as published.** `satelle story worktree <id> --base <trunk>` (`main`, `refs/heads/main`, `origin/main` or `refs/remotes/origin/main`) first brings local main level with origin and cuts from the updated tip, printing what came in. A dependency or epic-branch base is cut as before. An unresolved remote HEAD is retried with `git remote set-head --auto`; `--trunk-branch` or `[trunk] branch` names the trunk otherwise. (sty_92337a13, sty_f77b40bb)
- **`satelle trunk sync --strict [--refuse …]`** exits non-zero when the trunk is in a stop state; the container merge step runs it before merging children. `[trunk] base_refuse` (default dirty, diverged, ahead, behind-not-moved, offline, unresolved) sets the stop states for cuts and strict syncs; the engage-time `[trunk] refuse` default is unchanged. (sty_92337a13, sty_f77b40bb)

### Changed
- `satelle trunk sync --branch` is deprecated in favour of `--trunk-branch`. (sty_92337a13)
- This completes epic:trunk-sync: starting, cutting, merging and releasing all begin from main as it is on origin. (sty_f77b40bb)

## [serve-v0.0.147] - 2026-10-10

### Changed
- The serve binary carries the `trunk_publish` ledger kind and the `[trunk]` publish and base-refuse configuration. (sty_f77b40bb)

## [0.0.641] - 2026-10-10

### Added
- **Starting work checks main against origin first.** The first time a story, epic-parent or parent enters an engaging status, satelle fetches the trunk (the remote's HEAD branch, `origin` by default) before any entry gate runs and before the engagement baseline is recorded. A clean trunk that is behind is fast-forwarded. Any other finding prints one `satelle: trunk …` line: fast-forwarded with the count and old..new SHAs, behind but not moved, unpushed commits, diverged, a dirty tree on the trunk, or an offline fetch (work proceeds). For an existing story the same line is written to its ledger as `trunk_check`. In an agent session the line is carried in the delivered gate verdict. Dirty and diverged trunks refuse the engage by default and name the reconcile commands; `[trunk] refuse` and `[trunk] check = false` in `satelle.toml` change that. A level trunk prints nothing. (sty_9f3e51d1)
- **`satelle trunk sync [--fast-forward] [--json] [--remote r] [--branch b]`** runs the same check on demand and prints the same line or the report JSON. (sty_9f3e51d1)

## [serve-v0.0.146] - 2026-10-10

### Changed
- The serve binary carries the `trunk_check` ledger kind. (sty_9f3e51d1)

## [0.0.640] - 2026-10-10

### Fixed
- **The strict CI run no longer fails `TestEmitStopBlock` after a cursor stopcheck test.** `runHookStopcheck` kept the harness of the Stop answer in a package variable and never cleared it, so in the shuffled in-process test run a later Stop block was encoded for cursor. The value is now scoped to the stopcheck call, with a regression test. Each hook is its own process in production, so no hook output changes. This release also carries 0.0.639, whose tag was not cut because this failure stopped its CI run. (sty_ba302c1b)

## [serve-v0.0.145] - 2026-10-10

### Changed
- The serve binary carries the scoped Stop-answer harness. (sty_ba302c1b)

## [0.0.639] - 2026-10-10

### Added
- **cursor-agent session and dispatch usage is recorded.** A cursor command dispatch records its token usage and cache split from the json envelope, read exclusively as cursor reports it (`inputTokens` excludes cached input; fresh = `inputTokens`, total = input + cache read + cache write). An interactive cursor session's driver spend is read from the usage cursor reports in each `stop` hook payload, read inclusively (fresh = input − cache read − cache write), recorded once per turn. Cost is a cursor-named unavailable (cursor publishes no per-token price), model calls are a cursor-named unavailable (a stop marks a turn, not a call), and the resolved model comes from the hook payload. Print-mode cursor has no stop event, so its driver spend is a cursor-named unavailable. This completes epic:cursor-harness: cursor-agent is detected, gated, able to claim a verdict, bindable to a step, and measured. (sty_a3258bb3, sty_ba302c1b)

## [serve-v0.0.144] - 2026-10-10

### Changed
- The serve binary carries cursor usage recording. (sty_a3258bb3)

## [0.0.638] - 2026-10-10

### Added
- **A gate started from a cursor-agent session has its verdict delivered to that session.** When a gated `satelle story set` run from cursor outlasts the foreground and hands off, cursor's interactive `stop` hook (`satelle hook stopcheck --harness cursor`) delivers the finished verdict as a follow-up turn, once. The gate is stamped with cursor's own conversation id (`ResolveSession` reads it from the cursor adapter after `SATELLE_SESSION`), and the stop hook claims only gates of the same conversation. cursor acts on a follow-up only while the turn's `loop_count` is below 4 (measured), so satelle delivers and claims only then and otherwise leaves the verdict pending for the next stop. The installed stop entry carries a `timeout` above satelle's stop wait, because cursor's default hook timeout is under 75 seconds (measured). Print-mode cursor has no stop event, so delivery there is a cursor-named unavailable. Run `satelle agents install cursor` (or `satelle init`) to refresh the stop entry. (sty_2439f4fd)

## [serve-v0.0.143] - 2026-10-10

### Changed
- The serve binary carries the cursor session identity and stop-continuation cap. (sty_2439f4fd)

## [0.0.637] - 2026-10-10

### Added
- **A workflow step's performer or reviewer can be bound to cursor-agent.** In `agents.toml`, `command = "cursor-agent -p --trust --output-format json --model {model}"` (or `interface = "acp"`, `command = "cursor-agent acp"`) dispatches a cursor performer or reviewer headless. cursor has no system-prompt flag, so the step's instructions and the work item arrive together on stdin. A json result is read as cursor's (decision from `.result`; usage recorded as cursor-named unavailable for now), and a plain-text failure surfaces as a dispatch error. A cursor reviewer is forced read-only: satelle appends `--mode ask` (ACP: `session/set_mode ask`), which refused writes and mutating shell even with cursor's Run Everything approval, and refuses a reviewer template naming any other mode — `plan` included, because a plan-mode reviewer returns a plan instead of a verdict. Seats cursor cannot honour are refused at validation and at dispatch with a cursor-named reason: the stream interface, the cloud interface, any `--output-format` other than `json`, and `{system}`/`{payload}` argv tokens. A bare ACP model name such as `composer-2.5` maps onto cursor's parameterised ACP model value. Proven on real cursor-agent 2026.10.01 dispatches (performer and reviewer, command and ACP). (sty_10c52ab3)

## [serve-v0.0.142] - 2026-10-10

### Changed
- The serve binary carries the cursor dispatch adapter and its validation answers. (sty_10c52ab3)

## [0.0.636] - 2026-10-10

### Added
- **A cursor-agent session is held to satelle's edit and commit gates.** `satelle agents install cursor` writes `.cursor/hooks.json`: an unmatched `preToolUse` routed to the gate and commit gate through the installed wrapper (absolute paths, as cursor needs), `sessionStart` to `satelle hook context --harness cursor` and `stop` to `satelle hook stopcheck --harness cursor`; `satelle agents remove cursor` takes out only satelle's entries. cursor's tools are classified by the shared tool table: `Write` and `Delete` are edits, `Shell` goes to the commit gate including the tree-mutation check (so `rm` and `sed -i` are refused with no story engaged, as for Claude's Bash), and an unrecognised tool is refused with no story engaged. Session context reaches cursor as `additional_context`; an interactive stop is answered with stopcheck's text as a `followup_message`. Print-mode stop and the prompt reminder are recorded as cursor-named unavailable. The absent-wiring check knows cursor's wiring. Proven on real cursor-agent 2026.10.01 (captures under `internal/agentcli/testdata/cursor/14-*`). To use it, run `satelle agents install cursor` in the repo. (sty_7d098d50)

## [serve-v0.0.141] - 2026-10-10

### Changed
- The serve binary carries cursor's harness wiring, tool classification and context encoding. (sty_7d098d50)

## [0.0.635] - 2026-10-10

### Added
- **The hook wrapper passes cursor's own deny through.** `satelle hook gate --harness cursor` and `commitgate --harness cursor` deny in cursor's encoding, `{"permission":"deny","user_message":…,"agent_message":…}` — the one native shape that both blocks the tool and shows satelle's reason to the agent (a plain `reason` field blocks but hides it). The installed `satelle-hook.sh` recognises that deny and passes it through unchanged, and turns a real failure (missing binary, malformed output) into a cursor-shaped infrastructure deny. The per-harness deny encodings for claude, grok, pi and cursor now live in one place (`internal/agentcli`); claude, grok and pi output and wrapper bytes are unchanged. Run `satelle init` in a repo to regenerate its wrapper with the cursor arm; nothing else needs it. (sty_be756616)

## [serve-v0.0.140] - 2026-10-10

### Changed
- The serve binary carries the agentcli deny encodings, including cursor's. (sty_be756616)

## [0.0.634] - 2026-10-10

### Added
- **A cursor-agent session is recognised as cursor.** satelle detects cursor from its own markers — `CURSOR_AGENT` in agent-run shells and `CURSOR_INVOKED_AS` in shell and hook children — and from the `cursor_version` field every cursor hook envelope carries, checked before claude because a cursor run started from a Claude session inherits `CLAUDECODE=1`. `CLAUDE_PROJECT_DIR`, which cursor sets itself, is evidence of neither. Desktop variables such as `XCURSOR_SIZE` are not cursor evidence. cursor has its own harness-facts row (shown in `satelle help agent-dispatch`): a 30s background cutoff measured from cursor's own shell `timeout:30000` with background behaviour, and cursor-named unavailable entries — a verdict that outlasts that foreground is not delivered to a cursor driver until sty_2439f4fd. Driver-usage records for a cursor session say `cursor: no driver-usage reader for this harness`. (sty_c368826d)

## [serve-v0.0.139] - 2026-10-10

### Changed
- The serve binary carries cursor harness detection and the cursor harness-facts row. (sty_c368826d)

## [0.0.633] - 2026-10-10

### Fixed
- **The strict CI unit run no longer fails on TestCredguard's temp-dir cleanup race.** The guard's `go build` started Go telemetry inside the test's fake HOME, and a detached telemetry child kept writing there after the guard exited, so the temp dir's cleanup failed with "directory not empty". The fake HOME now seeds Go telemetry mode `off`, and a cleanup check fails the test if anything writes under its `go/telemetry` directory. This release also carries 0.0.632, whose tag was never cut because this flake failed its CI run. (sty_095d1f5b)

## [0.0.632] - 2026-10-10

### Added
- **Real cursor-agent captures are pinned as test fixtures.** Runs of the installed cursor-agent 2026.10.01 (json and stream-json output, instruction delivery through stdin, `AGENTS.md` and `.cursor/rules`, read-only `--mode plan`/`ask` and `.cursor/cli.json` deny, print-mode and interactive hook events, ACP, and the environment markers a cursor session sets) are committed under `internal/agentcli/testdata/cursor/` with their provenance, redacted of the operator's email, and a test pins what each showed. No cursor detection, wiring or dispatch is added; the per-seat verdict the cursor-harness stories build on is attached to the story. (sty_383ff068)

## [serve-v0.0.138] - 2026-10-10

### Changed
- The serve binary's dependency tree carries the new cursor-agent fixtures under `internal/agentcli/testdata/cursor/`; no serve behaviour changes. (sty_383ff068)

## [0.0.631] - 2026-10-10

### Changed
- **The on-demand agent model calls the driving session the orchestrator only when that seat is in-loop.** The driving session alone requests the next status, whoever performs the step, and it performs an in-loop step. It is the orchestrator when the orchestrator seat's resolved command is also in-loop. An empty orchestrator command still resolves to the isolated default, so that baseline is not one role. (sty_29ce243b)

## [serve-v0.0.137] - 2026-10-10

### Changed
- The serve binary embeds the agent-model wording that names the driving session as the orchestrator only when that seat resolves to in-loop. (sty_29ce243b)

## [0.0.630] - 2026-10-09

### Changed
- **The session principle names one driver, and doctor and init warn when both seats are in-loop.** The auto-loaded goals principle says the executor seat and the orchestrator seat are the same agent when both resolved commands are the in-loop command. A user message `complete sty_<id>` starts that story's drive, and a user message `drive epic <id> to done` starts the drive under *Driving an epic*. A named performer does the step and does not change status; when it returns, this session runs the next `satelle story set`. The on-demand agent model says the driving session is the orchestrator and that it alone requests the next status. When both seats resolve to in-loop, `satelle doctor` and `satelle init` each report a same-role warning naming both seats. The warning does not fail init and does not refuse a transition. The shipped baseline, whose orchestrator command is empty and resolves to the isolated default, does not warn. (sty_29ce243b)

## [serve-v0.0.136] - 2026-10-09

### Changed
- The serve binary embeds the session principles that name the driving session as the orchestrator, and the same-role warning when both seats resolve to in-loop. (sty_29ce243b)

## [0.0.629] - 2026-10-09

### Added
- **`satelle sync --force` publishes this tree over a newer hosted snapshot.** `sync config push --force`, `sync documents push --force`, and bare `sync --force` publish the local tree as the next snapshot, parented on the current hosted snapshot, when this machine is behind or has hosted changes it has not applied. Files this tree lacks are absent from that snapshot. Without `--force` a behind push is still refused and uploads nothing. `--force` still refuses an unresolved local conflict, and it does not change the work-state push. (sty_796fc16d)
- **Strict CI allows `TestServeGateSeesNewFiles` to skip while `satelled.version` is ahead of the newest `serve-v*` tag.** That skip is what the tree produces until the serve tag for the current serve version exists, and `scripts/skip-allow.txt` now names it. (sty_796fc16d)

## [0.0.628] - 2026-10-09

### Changed
- **The docs lane can close on a document delivered outside the repo.** A docs story whose prose lives in Confluence, Jira or a hosted document changes no repo path, so `satelle-docs-only-check` used to reject its close as an empty change set, while the create review routed such a draft to `docs` in the first place. The story could never reach done. The check now also accepts an attached document of type `external-change` with a non-empty body that names the document, its new version and what changed, recorded with `satelle story attach <id> --name <doc> --type external-change --body '<url or page id> — version <n>: <what changed>'`. That evidence counts only when the repo change set is empty, and it never excuses a non-doc repo path. An empty claim is still rejected, and the rejection now prints the attach command. The create review's route-proportionality clause says the docs lane covers both kinds of prose. The evidence type is configuration (`external_type` in the check), like `doc_paths`. (sty_03414f12)

## [0.0.627] - 2026-10-09

### Changed
- **The web UI footer no longer shows the user's email.** The satelle user is shown once, in the top bar; the footer keeps the product name and version. The dead footer-email template func, package var and CSS rule are removed. (sty_0612af6b, relands sty_20aad72a)

## [serve-v0.0.135] - 2026-10-09

### Changed
- The serve path picks up the footer without the user email (sty_0612af6b).

## [0.0.626] - 2026-10-08

### Changed
- **The test suites give the same verdict however they are ordered, repeated or timed, and CI runs them strictly.** CI now runs `make test STRICT=1` (unit tests with `-count=3 -shuffle=on`) and `make integration STRICT=1` (the integration suite, which did not run on CI before, with `-shuffle=on` and the browser tests against the runner's Chrome), both through a skip checker that fails the run on any skipped test not listed, with a reason, in `scripts/skip-allow.txt`. Without `STRICT=1` the make targets run as before. (sty_53c3f311)
- **Tests no longer depend on the developer's machine.** `make test` and `make integration` run inside `scripts/hermetic.sh`, which gives the suites an empty test-owned HOME and XDG dirs and a minimal PATH while passing through the Go caches. The checks of the operator's real reviewer/profile catalog and this repo's own authored route moved behind an opt-in `make operator-check`; integration tests that only need a working route seed their own. (sty_ec30f859, sty_53c3f311)
- **A test that touches the operator's real satelle state fails the run.** The credentials guard now also fails `make test` and `make integration` when a test adds or changes anything at the top of the real `~/.satelle` or changes the installed `satelle`/`satelled`, and it fails closed when it cannot check; one `internal/hostguard` package does this for both suites. (sty_1b739a74)
- **Running the CLI in-process inside a test leaves package state as it found it**, so repeated and shuffled runs no longer leak wiring between tests. (sty_d832e56f)
- **Test verdicts no longer depend on machine speed:** fixed sleeps that waited on work a test does not control were replaced by bounded waits on an observable condition, and timing-dependent tests were fixed, including the pi gate-wake test that once failed a release on CI. (sty_48b51600, sty_a8dbc889)

## [0.0.625] - 2026-10-08

### Added
- **Engaging a story claims it on the hosted project, and other machines can see who holds it.** With a bound project and the server reachable, moving a story into an engaging state places or refreshes its hosted hold before the engagement lease is taken; if another location holds it, the engage is refused naming the holder, label and last-seen time. Two machines engaging the same story at once cannot both succeed. An engage made offline, or on a story the server has not seen yet, records a pending claim that the next `satelle sync workstate push` reconciles: it places the hold, or reports the collision when another location got there first. After a pull, satelle lists each non-terminal story held by another location with its holder and since when (a last-seen time is labelled as last seen), whatever status the hosted copy still shows for it. (sty_52eb8c2f)
- **After pulling work state, satelle says where story status and the git remote disagree.** Each pulled story whose status claims code the remote cannot be shown to hold is named with the reason: in an implementation state with no commit recorded since work started (its code may exist only on the machine that holds it), or done with a recorded head no remote branch contains. The story's own workflow decides which states count, and cancelled states are skipped. A story whose workflow does not resolve is reported as `reachability unavailable (workflow not resolved)`. (sty_78e20d15)

### Changed
- **`satelle sync workstate pull` and `satelle sync rehydrate` merge by id instead of refusing on counts.** A freshly initialised repo, or one holding only stories and ledger rows the hosted copy has never seen, now pulls without `--force`; local-only rows are kept. A pull still refuses real divergence (a story newer locally that differs from hosted, or a ledger row whose content differs) and names each conflicting id; `--force` lets hosted win as before. (sty_78e20d15)
- **The work-state push hold starts where implementation starts, whichever agent performs that step.** On a workflow whose implement step declares `freeze = true`, a story is held from that step on, so a story being coded by a dispatched coder with its work only in an uncommitted tree is no longer published; executor steps before the freeze are not held. Workflows with no freeze step keep holding executor states only. (sty_87f407ef)

## [serve-v0.0.134] - 2026-10-08

### Changed
- The serve path picks up the hosted hold claim, the id-based work-state merge and the freeze-based hold eligibility (sty_52eb8c2f, sty_78e20d15, sty_87f407ef).

## [0.0.624] - 2026-10-07

### Changed
- **Substrate sync works from snapshots, like pushing to a git remote.** Each push of an opted-in config or documents area also publishes a snapshot record (`backups/sync/<area>.snapshot.json` on the hosted copy, never written into a tree) naming every file in the area and the snapshot it was based on. A pull or `satelle sync rehydrate` applies that snapshot, so a file deleted or renamed on one machine disappears on the others; a hosted head the snapshot does not name (such as the retired `workflows/done.md`) is never written into a tree. Each machine records, outside the repo, the snapshot it last synced per area (`satelle sync scopes` shows a `last synced` column; push and pull print `<area>: snapshot N (parent B)` / `<area>: synced to snapshot N`). A push from a machine whose recorded snapshot is older than the hosted one is refused, naming both numbers and telling you to pull first, and uploads nothing; when two machines push from the same snapshot, the first claim wins and the other is refused before uploading. A pull where a file changed on both sides keeps your local file, writes the remote copy to `backups/sync-conflicts/<area>/<path>`, names it as a conflict, and refuses the next push until you merge and delete that copy. The first pull on a machine with no recorded snapshot adopts matching files, treats differing files as conflicts, moves a local file the snapshot does not name and that matches a stale hosted copy to `backups/sync-removed/<area>/<path>`, and reports any other local-only file as `local only — push to publish`. A server with no snapshot yet deploys every head as before, and `--dry-run` uploads nothing. (sty_fe5a8ed4)

## [serve-v0.0.133] - 2026-10-07

### Changed
- The serve path picks up the snapshot-based substrate sync configuration (sty_fe5a8ed4).

## [0.0.623] - 2026-10-07

### Added
- **`satelle sync workstate push` holds back a story's activity until its code is on the git remote.** At push time satelle checks each story that has entered a state its workflow allocates to `agent=executor`: when the story's engagement tree has uncommitted changes, or its `HEAD` is on no remote-tracking branch, neither its item row nor any of its ledger rows is sent, and push prints `held <id>: uncommitted changes in <tree>` or `held <id>: head <sha8> is not on any remote branch` and still exits zero, sending everything else. Stories that have not reached such a state (backlog, plan) are never held. The next push after the code reaches the remote publishes the held rows, with no new transition and no `--full`. When git or the story's engagement tree is not available on this machine, push holds nothing on that basis and prints `reachability unavailable (<reason>): <id> not checked`. `--dry-run` lists what would be held. The hold is configuration: `[sync] hold_unpushed = false` turns it off (missing means on; a non-boolean value is a config error). (sty_7361569a)

## [serve-v0.0.132] - 2026-10-07

### Changed
- The serve path picks up the `[sync] hold_unpushed` configuration key (sty_7361569a).

## [0.0.622] - 2026-10-07

### Fixed
- **A story pushed to the hosted project now arrives on another machine with its whole ledger, including status transitions and change records.** A transition used to stamp its `status_transition` and `change_record` rows, and the story's `updated_at`, with the time the call started, before the gates ran. A `satelle sync workstate push` made during that gate window sent the later-stamped attachment rows and moved the push cursor past them, so the transition rows committed behind the cursor and were never sent without `--full`. Transition rows and the story's `updated_at` now carry the commit time, and the ledger push cursor follows insertion order instead of `created_at`, so every row inserted since the last successful push is sent whatever its timestamp. The cursor is bound to the database it was taken from (a per-database instance id plus the row at its saved position), so after `satelle sync rehydrate` or a `VACUUM INTO` copy the next push resets and sends the full ledger once, printing `ledger cursor reset: store changed; sending full ledger`. The first push after upgrading also sends the full ledger once, which heals rows earlier pushes left behind; the server ingests by id, so nothing is duplicated. (sty_4a31e1ed)

## [serve-v0.0.131] - 2026-10-07

### Changed
- The serve path picks up the ledger store's per-database instance id and insertion-order listing (sty_4a31e1ed).

## [0.0.621] - 2026-10-07

### Added
- **An epic can place its parallel children on a cloud performer while trunk children run locally.** A step in `step.toml` may declare `remote_agent` (a cloud binding, `interface = "cloud"`) and `local_tags`. A child runs that step remotely only when it is a child of a parallel epic, carries none of the local tags (for example `lane:trunk`), and the session is signed in; otherwise the step's own agent runs it, and a ledger note says why. Container steps cannot declare `remote_agent`, so the epic merge stays local; `satelle story rework` is refused for a remotely placed child; a child whose branch is not pushed is refused with the `git push -u` command to fix it. The shipped default workflows declare no `remote_agent`, so nothing changes until a repo opts in. (sty_dde8b6a4)
- **A cloud-performed step prints its session URL as soon as the session starts,** so the operator can follow it while the dispatch waits for the pushed branch. (sty_9eb7eac5)

## [serve-v0.0.130] - 2026-10-07

### Changed
- The serve path picks up remote placement of epic children and the cloud session URL progress line (sty_dde8b6a4, sty_9eb7eac5).

## [0.0.620] - 2026-10-06

### Fixed
- **A gate's functional check now sees the change set of a story engaged from a linked worktree.** A functional check runs in the main tree (the process of record), and `satelle story diff` refuses to run from a tree other than the one the story was engaged from. A check judging a worktree-engaged story (for example `satelle-substrate-only-check`) used to swallow that refusal and reject with "no change set found". The engine now exports `SATELLE_GATE_STORY=<id>` into the check's environment, and `story diff <id>` honours it by reading that story's own engagement tree. It is honoured only for the story it names, only while that tree still exists, and only for a tree of the same repository. Any other foreign-tree diff is still refused, and the check's working directory stays the main tree. A story engaged in the main tree is unchanged. See `satelle help reviewer-checks`. (sty_ab93f9a6)

## [serve-v0.0.129] - 2026-10-06

### Changed
- The serve path carries the updated `reviewer-checks` help topic, which documents `SATELLE_GATE_STORY` (sty_ab93f9a6).

## [0.0.619] - 2026-10-06

### Fixed
- **A story engaged from a linked worktree now counts its edits to the main tree's git-ignored substrate as its change set.** A worktree reads the main tree's substrate directory as its process of record, so a substrate-lane story engaged from its own worktree makes its edits there. The substrate walk used to drop every path outside the story's worktree, so `satelle story diff <id> --include-substrate` and the recorded change set came back empty, and `satelle-substrate-only-check` refused the close with "no change set found". Those main-tree substrate files are now listed under their process-root-relative paths (for example `skills/x.md` under the data dir). `story diff --include-substrate` also reports `process_root` (the main tree) and `process_root_files`, and its note says the files live in the main tree. The root comes from the existing process-of-record wiring and is never guessed: with no wiring, out-of-tree paths are still dropped. A story engaged in the main tree is unchanged, and those fields are omitted for it. Deleting main-tree substrate from a worktree is not enumerated yet (follow-up sty_85ba4a00). (sty_92e4cdbe)

## [0.0.618] - 2026-10-06

### Added
- **A step's performer can be a Claude cloud session.** An `agents.toml` performer binding may set `interface = "cloud"` (a fourth value of the dispatch transport, valid only on a `role = "agent"` performer and never a live transport). Entering that step, satelle refuses unless the story worktree's branch is pushed and the step skill declares no `output_*` contract or `attempt_*` policy, then launches the session from the worktree (`claude --cloud` under a pty from `script`) with a prompt of the step rubric, the embedded, repo-overridable `satelle-cloud-performer` skill and a data-only block (payload, branch, nonce). It waits for the adapter-named branch (`claude/satelle-<story>-<nonce>`) to carry a tip commit with the trailer `Satelle-Nonce: <nonce>` (the binding's `timeout`, else 60 minutes), fast-forwards or cleanly merges it into the worktree, and, when the binding sets `collect_doc`, attaches the commit body under that name. Usage is recorded as unavailable with the adapter's name, the ledger carries the session, branch and commit, and the step's local exit gates then judge the work as for any performer. The session needs no hosted access, token, vault or allowlist; the prompt is refused if it would carry the hosted URL or the session-token variable. A non-Claude harness reports cloud launch unavailable (`satelle agent capabilities`, and the dispatch fails by adapter name); `story rework` and consult refuse a cloud binding; a cloud binding needs no context-channel grant. Dogfooded: sty_9eb7eac5's coded step ran in cloud session session_01U66zNWMgWhdqvNDAvQJ1k9 and its local gates accepted it. See `satelle help agent-dispatch`. (sty_82cffd60)

### Changed
- **This repository's committed cloud hook guards by branch, not by a satelle bootstrap.** `scripts/claude-cloud-hook.sh` (wired by the tracked `.claude/settings.local.json`, now PreToolUse only) lets a cloud session edit, commit and push only on a `claude/satelle-*` dispatch branch and denies edits and `git commit`/`git push` (including `git -C …` and `git -c …` forms) on any other branch; it does nothing outside a cloud session. The SessionStart install/init bootstrap and the operator step to allowlist the hosted service are gone. (sty_82cffd60)

## [serve-v0.0.128] - 2026-10-06

### Changed
- The serve path carries the `interface = "cloud"` binding config, the cloud-launch capability column, the `satelle-cloud-performer` embedded skill and the updated `agent-dispatch` help topic (sty_82cffd60).

## [0.0.617] - 2026-10-06

### Added
- **A Claude cloud session of this repository runs satelle's edit and commit gates.** `.claude/settings.local.json` is now tracked (`.gitignore` ignores `.claude/*` except that file) and holds only a `hooks` block: SessionStart and PreToolUse run `scripts/claude-cloud-hook.sh`, which does nothing unless `CLAUDE_CODE_REMOTE=true`. In a cloud session it installs the satelle CLI from the GitHub release (skipped when `satelle` is on PATH), runs `satelle init --harness claude --no-workspace`, logs to `/tmp/satelle-cloud-bootstrap.log` and emits `satelle hook context`; edits and Bash calls are then judged by satelle's own gate and commitgate hooks, and denied when the bootstrap did not produce them. A real `claude --cloud` probe (CLI 2.1.291; there is no `--remote` flag, and `--cloud` needs a terminal) confirmed that `CLAUDE_CODE_REMOTE=true` is set, that hooks the clone carries fire, and that a Write and a `git commit` with no engaged story are refused by satelle. Hooks that `init` writes mid-session are not loaded, so a cloud session has no Stop or UserPromptSubmit hook. Operator steps, listed in the script header: allowlist `satelle.dev` in the cloud environment's network policy (the default proxy answers 403); until then the session has only a fresh local store and the embedded-default substrate, so every edit is refused. Optionally register an environment setup script that pre-installs satelle. Personal keys that lived in `.claude/settings.local.json` belong in `.claude/settings.json` or `~/.claude/settings.json`; a containment test fails if the tracked file gains anything but `hooks`. (sty_3b112554)

## [0.0.616] - 2026-10-06

### Added
- **A session with no browser (a cloud session, a CI job, a headless box) can act as the signed-in user with a session token.** Mint a token on the hosted server (Account → Sessions, or `POST /api/v1/me/tokens` with `session_projects` and an `expires_in` of at most 30 days) and set it in `SATELLE_TOKEN`. While it is set, every hosted client the CLI builds sends it as the bearer instead of any stored login, with no browser and no credentials file. It reaches story holds, location registration and workstate sync (REST and gRPC Sync.Apply/Snapshot) on its granted projects only; UI snapshot push is not covered. The token is never refreshed or written to disk: a 401 / Unauthenticated fails at once with "session token refused (expired, revoked, or a route the token does not cover)", and a 404 / NotFound says the project is not in the token's scope. The session learns its user id from the location-registration response, never `/api/v1/me`, and caches only that id, keyed by server and a SHA-256 fingerprint of the token, in `session-identity.toml` beside `credentials.toml`. Once it is known the assignee stamp, ledger attribution, web footer, `satelle project status` and `satelle whoami` all name that user and the wrong-holder guard applies. Until then, an engage is refused with `run "satelle whoami"`, and the footer and status say "session token, user not yet resolved". With `SATELLE_TOKEN` unset nothing changes. See `satelle help session-token`. (sty_6ed6318d)

## [serve-v0.0.127] - 2026-10-06

### Changed
- The serve path embeds the new `session-token` help topic (sty_6ed6318d).

## [0.0.615] - 2026-10-06

### Fixed
- **satelle has one user, and every surface that names the user agrees on it.** Local-only (no credential for the configured hosted server) the user is the repo's git user. Signed in (`satelle login`), the online account is the user and the git user is never shown in its place. The web header shows the account email, else its display name, else `signed in — run "satelle login" to refresh identity`; local-only it shows `git config user.email`. `satelle project status` decides its sign-in line by the same rule, and a `definition_edited` ledger row now names the git user local-only instead of the `executor` role. The engagement holder and seat owner are unchanged. The decision reads only the local credential file and git, never the network. (sty_e698d914)

## [0.0.614] - 2026-10-06

### Added
- **A session principle against drifting and orphan documentation ships with the binary.** `satelle-no-drifting-docs` tells agents that the code and configuration are the documentation: do not write prose that restates them, do not create a document nothing reads, and treat a document that has drifted as a defect to delete or replace with a pointer. It is injected every session; a repo overrides it with a same-named file under `.satelle/principles/`. (sty_9fa1023d)

### Changed
- **A Bash command that writes into locked substrate is held by the substrate lock, as an Edit is.** `hook gate` and `hook commitgate` refuse redirects, `tee`, `rm`/`mv`/`cp`/`sed -i` and `git -C` targets in locked `.satelle/` paths, and interpreter commands (`python`, `node`, `perl`, `ruby`, `sh`/`bash`/`zsh -c`) whose arguments or heredoc name `.satelle`, with the same refusal text and `substrate_lock_deny` ledger row. A script whose path is not in the command text, or a path assembled from fragments, is not visible and is not refused; `satelle hook gate --help` says so. Heredoc bodies and the commands that follow them are now classified by every Bash check. (sty_dc77e118)

### Fixed
- **The Makefile credentials guard fails a run only when the tests changed which credentials the host file holds.** `scripts/credguard.sh` compares the file's credential identity (servers, display name, email, principal) instead of its bytes, through the same check the integration suite uses, so an outside token refresh during a run no longer fails it; a test that adds, removes or changes a credential, or creates, deletes or corrupts the file, still does. (sty_5ba68e2c)

## [serve-v0.0.126] - 2026-10-06

### Changed
- The serve path picks up the new session principle, the Bash substrate-lock classification and the shared credential identity check (sty_9fa1023d, sty_dc77e118, sty_5ba68e2c).

## [0.0.613] - 2026-10-06

### Fixed
- **The epic-driving principle's no-branch case now names the dependent child as the one cut from trunk.** It reads "A child whose done target has no branch of its own ... is cut from trunk", where 0.0.612 made the done target the subject. (sty_92e73724)

## [serve-v0.0.125] - 2026-10-06

### Changed
- The embedded `satelle-agent-goals` principle words its no-branch worktree base case with the dependent child as subject (sty_92e73724).

## [0.0.612] - 2026-10-06

### Fixed
- **The epic-driving principle no longer tells the driver to cut a dependent child from a branch that holds nothing new or does not exist.** `satelle-agent-goals` ("Driving an epic") now says: cut an independent child from the epic base; cut a child whose done `depends-on` target is already on trunk, or has no branch of its own (its change was live without a commit), from trunk; cut from the target's branch only when the done target is not yet on trunk. `satelle help worktree` ("The base") states the same rule, and the `story worktree` help, the missing `--base` error and the engage refusal name trunk as a base for a dependent and point to `satelle help worktree`. (sty_92e73724)

## [serve-v0.0.124] - 2026-10-06

### Changed
- The embedded `satelle-agent-goals` principle and the `worktree` help topic carry the trunk-aware worktree base rule for a dependent child (sty_92e73724).

## [0.0.611] - 2026-10-06

### Added
- **`satelle logout --prune-loopback` removes the stale localhost test credentials earlier test runs left in the host credentials file.** It removes only loopback (`127.0.0.1` / `localhost`) entries that carry no `created_at` and no email, and leaves every real hosted credential untouched. Run it once after upgrading to clear the entries that accumulated before this release. (sty_18403814)

### Changed
- **The web cost view shows no dollar column.** The Driver sessions table keeps its token, call, cache and agent-time columns; each adapter's note still says, by adapter name, whether usage was measured. (sty_5c4e703e)
- **The story list's status cell shows the status alone.** A pending outgoing gate's rounds sit on the current PROGRESS chip, with the edge in its tooltip. (sty_5f717362)
- **Gate outcome counts are shown by number and colour, without pass and fail glyphs.** Accept counts use the accent colour and reject counts the fail colour; each chip's title still says what the numbers mean. (sty_82e203f4)
- **A blocked episode shows on a progress chip as a count in the warn colour.** The chip's title names the episodes by the route's own park-state name (e.g. "blocked once") instead of a fixed "parked" word. (sty_027a90f0)

### Fixed
- **No test writes a credential into the operator's host credentials file.** Under `go test` the credentials path is fenced to the test's temp or `SATELLE_HOME` dir; the `internal/cli` and shared test helpers isolate `XDG_CONFIG_HOME`; every Makefile `go test` target runs under `scripts/credguard.sh`, which fails the run when the host file changes. (sty_18403814)
- **The integration suite's host check tolerates a credential token refresh made outside the suite,** and still fails, naming the file, when the suite itself writes the host credentials file. (sty_d9677380)
- **A linked worktree of the session's own repository is not refused as a foreign tree.** The foreign-tree fence lets an Edit/Write or Bash mutation in a worktree that shares the session repository's git common dir through to the ordinary edit gate; a different repository is still refused, and an unresolvable common dir fails closed. A target under the process temp dir or `/tmp` is not fenced even when it is itself a git root. (sty_bcf837ff)

## [serve-v0.0.123] - 2026-10-06

### Changed
- The web story list drops the status-cell gate badge and the chip glyphs, shows blocked episodes as a warn-coloured count and a pending gate on the current chip, and the cost view drops its dollar column (sty_5f717362, sty_82e203f4, sty_027a90f0, sty_5c4e703e).

## [0.0.610] - 2026-10-06

### Fixed
- **A git commit or push run from inside one of a session's worktrees is judged against that worktree's seat when the session holds several.** The commit gate derives the command's effective directory (the tree the hook runs in, moved by each preceding `cd`, then by the git segment's own `-C`) and attributes the commit to the seat whose worktree contains it, whatever order the store lists the seats in; it is still refused when that seat is not engaged or its status is not listed in an authored `[gate.command_allow]`. A commit nothing moved, or one whose directory is in no seat's worktree, is attributed exactly as before. The foreign-tree fence still refuses `git -C <other tree>` unless `[gate] allow_outside_tree_edits` is true. (sty_3a9b06fe)

## [0.0.609] - 2026-10-06

### Added
- **`satelle story worktree <id> --base <ref>` opens a story's worktree and carries the gitignored files the repository declares it needs.** `[worktree]` in `satelle.toml` declares `include` paths plus `branch` and `path` templates (`{id}` placeholder); each path is linked from the main tree, anchored in `info/exclude`, and proven ignored. `--existing <path>` brings an existing worktree up to the declaration without creating or deleting anything. A malformed declaration is refused at load, naming the entry. (sty_804c566b)
- **A missing or divergent authored process is reported, never silently replaced by the embedded default.** An unreadable workflows dir refuses engage, transition, create, amend, restamp, `story route`, doctor and validate by name; an absent one keeps the embedded backstop and says so, naming the binary-shipped halves; a worktree whose own data dir diverges from the process of record is reported at engage, in refusals and in the route document. (sty_d6e209aa)
- **The story list shows completed stages and gate outcomes.** The PROGRESS column has one chip per completed stage plus the current one, named by the stage, with a per-edge badge counting accepts and rejects; park, sink and terminal come from the route. (sty_a7d6e0d0)

### Changed
- **A performer dispatched into a linked worktree without its harness gate wiring is refused before it starts.** The refusal names the harness and each missing path (for claude, `.claude/settings.json` and the `satelle-hook.sh` wrapper it calls). To heal a worktree: declare the gitignored paths a worktree needs in `satelle.toml` `[worktree] include` (e.g. `include = [".claude", ".grok", ".pi"]`) and run `satelle story worktree <id> --existing <path>` to link them in; or, only if you accept ungated runs, declare `[worktree] absent_wiring = "fail-open"`. An unknown key under `[worktree]` or `[harness.<name>]` is now refused at load, naming the key. (sty_f141c77f)
- **A story driven in a linked worktree follows the main tree's authored process.** Workflows, agents, skills, principles, documents, the constitution and the gate, engagement and vars decisions read the main tree's data dir; a worktree's own data dir is not the process of record. (sty_ddbe2669)
- **A reviewer on a re-presented edge judges what changed since its last verdict** instead of starting over, citing the words its earlier verdict rested on. (sty_3d279f8b)

### Fixed
- **A reviewer records a verdict on a story whose code, plan and history exceed the per-argument limit.** Oversized story material is passed by reference instead of failing with `argument list too long`. (sty_f3a127cd)
- **A read-only shell command and a temp-dir write pass the edit gate whoever holds the seat.** `sed` without an in-place flag (`-i`, `-i<suffix>`, `--in-place`, or a short cluster containing `i` such as `-ni`) is a read and contributes no mutation target; its script (the `-e`/`-f` value, or the first positional) was never a file. A leading `~`, `$VAR` or braced `$VAR` that the hook's own environment expands to an absolute path is expanded before classification, so `mkdir -p $TMPDIR/x` or a scratch path under `/tmp` is recognised as outside the tree through the existing temp-dir exemption. A word the environment cannot resolve stays conservatively in-tree, and `sed -i`, `rm`, a redirect and `mkdir` into the tree are still refused under a foreign seat. A `w <file>` command inside a non-in-place sed script is not tracked. (sty_bc78617c)
- **An edit is judged against the seat of the worktree it is made in when one session holds several seats.** The edit gate, commitgate and substrate lock pick the session's seat whose worktree contains the target, never store order, and name every seat when none matches. (sty_42231b74)
- **The unit suite passes in a clean checkout with no gitignored data dir.** The reviewer-skill quotation test judges the embedded skills unconditionally and skips the operator's authored skills only when they are absent. (sty_700b772f)
- **The integration suite runs in a story worktree against the repository's process of record.** (sty_a0140039)

## [serve-v0.0.122] - 2026-10-06

### Changed
- The web story list renders completed stages and per-edge gate badges, and the serve path picks up the worktree process-of-record, unreadable-process and session-seat changes (sty_a7d6e0d0, sty_ddbe2669, sty_d6e209aa, sty_42231b74).

## [0.0.608] - 2026-10-05

### Added
- **A grok in-loop session receives the session principle set.** grok ignores SessionStart hook stdout, so the set is delivered on the hook event harness configuration names (`[harness.<name>] session_context_event`; grok: `PostToolUse`), once per session, budgeted by `tool_context_limit_chars` (grok: 10000) and degrading to the compact index. A harness with no declared event emits nothing when an event is present; `satelle hook context` with no event still emits. Existing grok installs get the PostToolUse context hook by re-running `satelle init`. (sty_507d3d9c)
- **A gate can be judged by a panel of agent seats.** A reviewer reference may declare `panel` (seats) and `combine` (a functional-check skill); each seat runs cold and one-shot, the combine check's exit code is the skill's verdict, and seat records ride the ledger row. `satelle-panel-all-accept` and `satelle-panel-majority` ship as default combine skills; a repo authors another rule as a check skill. A panel on a bundled edge is refused by validate. (sty_74e52e16)

### Changed
- **Story create no longer requires acceptance criteria.** The compiled numbered-criterion rule is gone from the structure check (title, goal body and category remain). The shipped create review does not reject empty or unnumbered criteria; numbered criteria that are present must still verify the goal. Help for create-story, create-review, reviewer-checks and workflows updated. (sty_9d623118)
- **Create review pushes back on a story that prescribes its solution.** A draft whose body or criteria require a particular implementation is rejected with the prescriptive parts named; naming a file, symbol or mechanism only as evidence of what exists or is missing is not a prescription. (sty_dea5e414)
- **The session principle tests cover every harness channel.** A repo principle is proven on claude SessionStart, the pi synthetic SessionStart, grok PostToolUse and the dispatched system prompt. (sty_9e33976f)

## [serve-v0.0.121] - 2026-10-05

### Changed
- Route parsing carries reviewer `panel` and `combine` declarations (sty_74e52e16).

## [0.0.607] - 2026-10-04

### Breaking
- **`satelle story estimate` no longer accepts `--usd`.** The flag, the `estimate-usd` write, and the `actual-usd` write are gone. A stored dollar tag is left as stored and is not an estimate. Token, cache, and time estimates and actuals are unchanged.
- **Migration:** replace `satelle story estimate <id> --usd <n>` with `satelle story estimate <id> --fresh-input <n> --output <n>`, or with the legacy `--tokens <n>` / `--time <dur>`. `satelle init` does not rewrite scripts or stored tags.

### Changed
- **A story's cost is a band from its actual tokens, not a dollar figure.** When actual tokens are recorded, the story is tagged `actual-cost:low` (under 200000), `actual-cost:medium` (200000 up to but not including 2000000), or `actual-cost:high` (2000000 and above), from fresh input, unsplit input, output, cache read, and cache write only. No measured tokens means no band. The story and epic cost views show that band instead of a dollar estimate or dollar actual. Driver-session dollars and gate-value cost-per-reject are unchanged. (sty_2f6c7df5)

## [serve-v0.0.120] - 2026-10-04

### Changed
- **The published serve binary includes the token cost band.** The cost view, the band function, and the estimate help sit on the serve dependency path, so this release bumps satelled.version. (sty_2f6c7df5)

## [0.0.606] - 2026-10-03

### Fixed
- **A finished gate's verdict reaches the session that started it, exactly once, on claude, grok, and pi.** This closes epic:gate-wake. (sty_fbf45a3f)
  - Each harness records its gate-wake budget and catch-up channel in its adapter's facts row. (sty_3a2d3b7e)
  - **grok:** past the eight-continuation Stop cap, a verdict reaches the same session by resuming it with the verdict as the prompt. It no longer waits for a ninth Stop-block that grok never consults. (sty_eac9b28d)
  - **claude:** the Stop hook delivers a gate as soon as it finishes, not held until every other running gate is done. Past claude's Stop block cap (CLAUDE_CODE_STOP_HOOK_BLOCK_CAP, default 8), the verdict arrives as the prompt of a resumed turn in the same session (`claude -p --resume`). StopFailure and SessionEnd close the turn, so a cut-off turn still gets its verdict. (sty_7e4393fc, supersedes sty_c35690d3)
  - **pi:** a pi session is told a gate's verdict once and is no longer re-prompted while the gate runs. The stop waits once, up to its bound. A gate still running is named as pending, and the extension sends the verdict as one user message when it finishes. A non-interactive pi run that exits on settle records a pi-named limitation on each undelivered gate, leaving it for the next session's prompt. (sty_7ebeda10)
  - **Cross-repo:** a gate started by a session in one repo, acting on another repo, is now delivered by the session's own repo hooks, not left unclaimed in the other repo's store. (sty_8f10499d)

### Changed
- Run `satelle init` in each repo to deploy the new claude StopFailure/SessionEnd hooks and the updated pi extension. Until then, those repos keep the previous delivery behaviour. (sty_7e4393fc, sty_7ebeda10)

## [serve-v0.0.119] - 2026-10-03

### Changed
- **The published serve binary includes the gate-wake delivery changes.** The gate handle store, session resolution, and help topic sit on the serve dependency path, so this release bumps satelled.version. (sty_fbf45a3f)

## [0.0.605] - 2026-10-03

### Changed
- **epic:substrate-tight closed.** Embedded session principles and two on-demand principles state each rule once and point at the help topic or toml that owns the rest. Reviewer skills drop the repeated read-only posture paragraph. An epic child's close gates accept the branch-only release when the parent route owns the merge. (sty_1c91175b, sty_f61c09ec, sty_984d0717, sty_f10218e6, sty_2e9d0440)

## [serve-v0.0.118] - 2026-10-03

### Changed
- **The published serve binary includes the shortened embedded principles and reviewer skills.** Those files sit on the serve dependency path, so this release bumps satelled.version. The web UI does not merge branches. (sty_1c91175b)

## [0.0.604] - 2026-10-03

### Fixed
- **A dispatched reviewer's Stop hook no longer waits on the gate that is waiting for it.** The hook still wakes a driving session. A dispatched process, recognised by the markers satelle already sets, skips that wait, so the verdict is not held until idle_timeout. (sty_aeb8a138, sty_79894535)

## [0.0.603] - 2026-10-03

### Changed
- **epic:epic-drive closed.** The mechanism shipped in 0.0.597 through 0.0.602. This container release records the close. Children of an epic whose parent owns the merge now stop on `epic/<id>` instead of pushing main; this epic's children had already landed on main before that rule existed, so there was nothing left to merge. (sty_53d52623)

## [0.0.602] - 2026-10-03

### Added
- **A container step may wait on a named child obligation, then perform.** `after_children` is refused until every child has discharged that obligation, or is done or cancelled. The refusal names the children that have not. Entering the step takes a parent seat. A discharged child can still advance from another worktree. Absent the key, the waiting-container lease rule is unchanged. The binary does not merge, push, or bump a version. (sty_fdad98b0)

## [serve-v0.0.117] - 2026-10-03

### Added
- **The published serve binary includes the after_children gate, because the route parser and help topic sit on its dependency path.** The web UI does not merge branches. (sty_fdad98b0)

## [0.0.601] - 2026-10-03

### Changed
- **The driving session is told how to drive an epic to ready and to done.** Ready walks only the container. Done follows `satelle story wave` and stops when that command exits non-zero. One story at a time remains the rule only when no schedule is declared. A parallel inject names one worktree per child. An independent child is cut from the branch the container was engaged on; a dependent child is cut from the done dependency's branch. A cancelled dependency is a stop. (sty_4aefd2a9)

## [serve-v0.0.116] - 2026-10-03

### Changed
- **The published serve binary ships the same drive-an-epic principle the CLI injects.** The web UI does not drive epics. (sty_4aefd2a9)

## [0.0.600] - 2026-10-03

### Added
- **Engaging a child a scheduled epic's wave would omit is refused.** The refusal quotes the wave's reason. A child the wave returns may still engage, one worktree per lease. A container with no schedule is unchanged, so an existing epic does not lock. A story that is not a child of a scheduled epic is unaffected. (sty_54004fbd)

## [serve-v0.0.115] - 2026-10-03

### Added
- **The published serve binary includes the engagement-wave refusal, because the help topic that states it sits on the serve dependency path.** The web UI does not enforce engagement. (sty_54004fbd)

## [0.0.599] - 2026-10-03

### Added
- **`satelle story wave` returns the children of an epic that may start now.** Membership is the epic tag set. A dependency is satisfied only when that story is done; cancelled does not count, and the omission names it. Parallel returns every eligible child. Sequential returns that set only when it is exactly one id, otherwise it refuses and names them. No schedule, two epic-parents, or a missing epic tag refuses with no runnable ids. The command writes nothing. (sty_a0563b3e)

## [serve-v0.0.114] - 2026-10-03

### Added
- **The published serve binary includes the epic-wave assessment, because the CLI help topic and verb sit on its dependency path.** The web UI does not render a wave. The command remains read-only. (sty_a0563b3e)

## [0.0.598] - 2026-10-03

### Fixed
- **A container waiting on open children does not hold the engagement lease.** Entering a `waits_on_children` status releases any story-seat row for the container, so one child can engage from that worktree. A second child in the same tree is still refused. When every child is terminal, the container's next performing step acquires a lease again. Ordinary stories are unchanged. (sty_56648ae5)

## [serve-v0.0.113] - 2026-10-03

### Fixed
- **The service uses the same waiting-container rule as the CLI.** A container idling on open children is not treated as holding the story seat. (sty_56648ae5)

## [0.0.597] - 2026-10-03

### Added
- **A container step may declare `schedule` as `parallel` or `sequential`, and a story may declare `depends-on:`.** An absent schedule stays absent. A value outside the closed set is refused and the message names the key and both legal values. `schedule` is refused on a step that is not waiting on children, so it cannot be confused with reviewer fan-out `parallel`. Create and set refuse a `depends-on:` that does not name a story, a self-edge, a cycle, or an edge that leaves the epic set, and the refusal names the ids. The binary does not compute a wave and does not change engagement. (sty_839938cc)

## [serve-v0.0.112] - 2026-10-03

### Added
- **The service reads the same container `schedule` field the CLI validates.** A waits-on-children step may carry `parallel` or `sequential`; anything else is refused. Reviewer fan-out is unchanged. (sty_839938cc)

## [0.0.596] - 2026-10-02

### Added
- **`satelle story driver-backfill` attributes historic pi driver spend by the engage..close window.** Stories whose driver rows are still `pi: no driver-usage reader` can be filled from the session file that still holds the messages. The row is marked backfilled and derived from timestamps, never as a live delta. An overlapping window in the same session is named unavailable and is not split. Zero cost beside real tokens stays an unpriced-model unavailable, never a measured zero. A later backfilled available row is what session reconciliation counts, so the old unavailable row no longer hides those tokens. (sty_8c0e7e8c)

## [serve-v0.0.111] - 2026-10-02

### Added
- **The service shows backfilled driver rows as derived, not as a live measurement.** (sty_8c0e7e8c)

## [0.0.595] - 2026-10-02

### Fixed
- **Pi's in-flight turn is in the session file when the tool runs.** A timestamped probe on a real pi session recorded the calling assistant row already present at each of three tool executions, so `MayUndercountInFlightTurn` for pi is false, measured, not inferred. A first available driver row after unavailable rows is marked a fresh baseline and is not reported as a measured zero-token delta. (sty_89768625)

## [serve-v0.0.110] - 2026-10-02

### Fixed
- **The service uses the measured pi flush result.** Cost coverage does not treat a fresh baseline after unavailable rows as a measured zero. (sty_89768625)

## [0.0.594] - 2026-10-01

### Changed
- **An epic close re-reads the `epic:<theme>` set at commit and does not trust the parent list.** The set is every story carrying the tag. The parent is the one `category: epic-parent` in that set. Every other tagged story is a child and keeps its work category. `parent_id` is not membership. A child that is not done or cancelled refuses the close, named by id and status. The reviewer list is a snapshot. A plain parent container still uses `parent_id`. A proposal whose tag names a container that is already done or cancelled is filed without that tag and with a body note. (sty_9f4f8e12)

## [serve-v0.0.109] - 2026-10-01

### Changed
- **The service reports the same epic set the close uses.** Web story view, route members, and the workspace aggregate read the `epic:<theme>` set, not `parent_id`, for an epic-parent. (sty_9f4f8e12)

## [0.0.593] - 2026-09-30

### Added
- **A pi driving session now reports what it spent.** Every run driven on pi recorded its
  driver rows as `pi: no driver-usage reader for this harness`, so the figure satelle published as
  a story's cost covered dispatched agents and gates and excluded the session doing the driving —
  silently, because an honest unavailable looks like a measured total. epic:token-accountability
  built the instrument for dispatched agents; the in-loop driver was never in scope, and moving
  this repo's driving harness to pi turned a working column into a missing one. (sty_ca1ca935,
  epic:earned-complexity)
  - **The premise was checked before anything was built, and the answer was yes.** A pi session is
    JSONL at `~/.pi/agent/sessions/<repo-dir>/<timestamp>_<id>.jsonl`: a `session` header, then
    `model_change` and `message` rows, and each assistant message carries that request's own
    `usage` — `input`, `output`, `cacheRead`, `cacheWrite`, `totalTokens` and a per-component
    `cost`, written once when the message completes. The split is disjoint and maps straight onto
    the snapshot fields, so one assistant row is one model call.
  - **The dollars are pi's own, never a price table.** A model pi holds no price for records cost
    0 beside real token counts, and that zero means *unpriced*, not *free*: it is reported as an
    unavailable cost with its reason, never as a measured $0. A silent zero is the one thing the
    adapter-agnostic rule forbids, so it is named.
  - **The summary says which is which.** The cost summary now states, in words, when the total
    covers gated-and-dispatched agents only and not the driving session, and the aggregate names
    every adapter's reader or its named unavailable.
  - **Captured real output is the fixture.** `internal/agentcli/testdata/driver/pi_session.jsonl` is
    a real capture, checked in beside the reader that parses it, and the reader is exercised per
    adapter in the cost-coverage tests.

### Fixed
- **The `serve-v0.0.107` changelog entry claimed the service surfaces an unfinished dispatch in the story view. It does not.** Commit `6130151` touched no serve or web source, and the retrospective on sty_f0ed2473 caught the overreach. The verb is real; the claim that the service view shows it was not backed by the diff that shipped. Surfacing it in the web view is `sty_737f4d87`. The line is corrected here rather than left standing.

## [serve-v0.0.108] - 2026-09-30

### Added
- **The service carries the pi driver-usage reader above**, so a story driven on pi reports its driving session's tokens in the cost view. (sty_ca1ca935)

## [0.0.592] - 2026-09-30

### Added
- **`satelle story recover <id>`: a dispatch that died without a completion is now visible, with what it left.** A dispatched performer that dies mid-turn — killed by a stall, an OOM, a closed pipe — leaves no completion row, and nothing in the product said so. The next driver either re-dispatches blind or assumes the work is lost. Observed on 2026-09-30: a planner dispatch from 2026-09-24 had died without completion and nothing had surfaced it in the six days since. (sty_f0ed2473, epic:earned-complexity)
  - **It reports, it does not vouch.** The report opens `dispatch <name> ended WITHOUT a completion; the transition did NOT commit; the work is UNVERIFIED`, names the log, the last recorded event and the wall time since, and lists the files present since the engagement anchor. It then says in words that it does not say whether the slice is coherent. A half-written slice must never read as finished.
  - **Detection uses evidence that already exists.** A dispatch is unfinished when its newest log has no terminal `completion` line. No new telemetry is written on the dispatch path, and the verb stays quiet after a genuine successful completion — including when an older dispatch died and a later one succeeded, which is exactly the shape this repo hit on sty_992cffc6.
  - **The recovery decision leaves a row.** `--choice redispatch|finish|park` records what was decided and on what evidence, so the cost of a recovery is visible in the story's own record rather than only in a session transcript.
  - **A missing baseline is reported, not hidden.** A story with no engagement baseline says so in the report and still gives the dispatch facts.
  - **The watchdog stall is included, not just the orphan.** A dispatch reaped by the stall detector has no completion row either, and is reported the same way.

## [serve-v0.0.107] - 2026-09-30

### Added
- **The service carries `satelle story recover` above.** The verb ships in the binary the service runs; surfacing an unfinished dispatch in the web story view is not part of it (see the 0.0.593 correction). (sty_f0ed2473)

## [0.0.591] - 2026-09-30

### Changed
- **The substrate lock shipped in 0.0.590 is smaller: dead configuration surface removed, one config read per edit instead of two, and a four-parameter gate.** The lock refused a `.satelle/` edit under a performing seat, but it carried plumbing that no posture a repo can reach makes work, and read the config twice on every edit. Six premises were checked against the code first; every one of them held. (sty_1ba4dbc7, epic:earned-complexity)
  - **The dead field is gone.** `GateConfig.LockSubstratePaths` was decoded and read at exactly one place — a `satelle settings` display row — and since the scaffold ships the key COMMENTED, that row could only ever print empty. The field, the display case, the settings schema row and its test are removed. A repo that pins the key still loads and is now answered by the gate, not by a display.
  - **One read, not two.** `substrateLockGate` called `config.Load` (a read plus a full decode) and then `ResolveLockSubstratePaths` (a second read plus a second decode) on every edit, and re-absolutised the footprint list twice. It now reads the file once and parses both the lock list and the exempt globs from the same bytes.
  - **The gate takes four parameters, not six.** `engaged`, `info` and `live` were three views of one seat resolution, collapsed inside the gate AFTER the config had already been read. Holders are resolved at the call site, so with no seat the gate reads no configuration at all.
  - **`ResolveLockSubstratePaths` is deleted.** Its only consumer was the doctor finding, which used half its result; the doctor now makes one call into the config parser with no entry point of its own.
  - **Fail-closed now covers the lock and not the carve-outs.** An unreadable or garbled config previously deferred to the ordinary gate; it now fails closed to the default lock. The story-reference globs are the exception: a damaged file falls back to the seeded managed defaults rather than to none, so a garbled `satelle.toml` cannot turn a story dump into a refusal.
  - **Net: mechanism −4 lines, tests +34.** Every added test line and every added mechanism line is named in the story's `line-count` evidence with the alternative it rejected. The regression suite and the three-harness live probe are unchanged in behaviour.

## [serve-v0.0.106] - 2026-09-30

### Changed
- **The service carries the substrate-lock simplification above**: one config read per edit, the four-parameter gate, and the removed settings display surface. (sty_1ba4dbc7)

## [0.0.590] - 2026-09-30

### Added
- **The edit gate refuses a substrate change while a story is performing, on every harness.** `.satelle/` was unconditionally edit-exempt, so the workflows, skills and agent bindings that judge a story could be rewritten in the middle of that story — observed in petitio, where a story replaced the `[reviewer]` binding at `in_progress` and every later gate of that same story dispatched on the new one. `satelle-workflow-change-review` caught it at integration, which is detection after the fact. The refusal is now made in the gate itself, before the exemption early-return. (sty_992cffc6)
  - **One harness-neutral predicate.** `substrateLocked` takes an already-resolved path and a list of roots — never a tool name, envelope or harness — so a claude-shaped and a grok-shaped edit of the same file classify identically and produce the same denial text. `TestSubstrateLockIsIdenticalAcrossHarnesses` drives both shapes and asserts one reason string.
  - **The lock is the default posture, not an opt-in.** `config.ParseLockSubstratePaths` reads the RAW committed `satelle.toml` and keys off key PRESENCE, because the decoded config cannot tell an absent key from `= []`: absent locks the data dir, present-with-entries takes the operator's list, and present-and-empty is the documented opt-out. A file it cannot read with confidence — a TOML error, a non-string list — fails CLOSED to the lock, never to permissive. Migrate is not on this path: the gate resolves the list on every edit, so an upgraded repo that has never run migrate is locked from its first edit.
  - **There is a lane out, or the product deadlocks itself.** A story in the `substrate` category may make the change under its own live seat; without one, satelle could never ship a skill, a gate rubric or a workflow. The deny names the holder, the locked path, the lane out and the opt-out key, and a store that cannot be read fails closed rather than allowing the edit.
  - **Carve-outs are evaluated first and win.** The process temp dir and `/tmp`, the `[gate] edit_exempt_globs` story-reference dumps, and the footprint satelle deploys itself (`.claude/`, `.grok/`, `.pi/`, the managed `.gitignore` block) stay writable, so the binary's own writes are never refused.
  - **Every refusal is a row.** `substrate_lock_deny` on the ledger, attributed to the gate rather than the agent, carrying the path, the holder's status and the lane — so a refusal is auditable after the fact and reads the same whichever harness was refused.
  - **`satelle doctor` reports a deliberate unlock.** `IDSubstrateUnlocked` is a Warn when the key is present and empty, and nothing otherwise, so an unlocked posture is visible rather than silent.
  - **Scope, stated rather than implied:** the lock covers Edit/Write. A substrate write that arrives as a shell command still short-circuits through the existing bash path; that is a separate story, not a silent part of this one.

## [serve-v0.0.105] - 2026-09-30

### Added
- **The service carries the substrate lock above**, its `[gate] lock_substrate_paths` key, its `substrate_lock_deny` ledger kind and the `IDSubstrateUnlocked` doctor finding. (sty_992cffc6)

## [0.0.589] - 2026-09-30

### Changed
- **This repo's agent seats now run on sonnet 5.5, and the edge-gate reviewer runs on grok.** Operator direction (sty_a07f69ef): `[reviewer]` moves from an inline `claude` command on opus to the `grok-reviewer` profile (grok `acp`, `read_file`/`grep`/`list_dir`), and `[reviewer-consult]`, `[ready-reviewer]` and `[planner]` are pinned to `model = "sonnet"`, which resolves to `claude-sonnet-5-5`. No seat in `.satelle/workflows/agents.toml` resolves to opus any more, and `satelle agent validate` resolves every seat with no missing profile and no fallback.
  - **The cheaper edge-gate seat is a directed change, not a measured improvement.** The dispatch record shows opus as the largest line item (759 dispatches, $266) and grok having run the same gate class 149 times for a small fraction of that, but the store holds no comparison of the two harnesses' ACCEPT/REJECT verdicts on the same edge. Whether grok judges as well is NOT asserted here; read its effect from the gate-value ledger (epic:token-accountability).
  - **The machine catalog is untouched.** `~/.satelle/agents.toml` is fleet-wide and still pins `claude-consult` and `claude-planner` to opus; this repo moves those seats with a pin in its own binding, which `satelle agent validate` attributes to `(repo)`, so no other repo on the machine is rebound.
  - **`[model_order]` and the agent roster agree with the bindings.** `claude` drops opus so an unset binding cannot silently resolve to it, and `grok` is `grok-4.5`, the `grok-reviewer` profile's actual pin, replacing the stale `grok-4.7`. The roster's reviewer, reviewer-consult, ready-reviewer and planner rows are rewritten to match.

## [0.0.588] - 2026-09-30

### Fixed
- **A session-start hook verb no longer fails as "unparseable output" when it has something to report.** The pi extension binds `SessionStart` to a direct binding that runs `satelle reindex` and parses its stdout as one JSON document. `reindex` printed the doc-sync JSON and then appended its progress lines to the SAME stream, so any repo with a backlog-reference delta — this one has 61 open stories — got `Warning: satelle reindex unavailable (unparseable output) — continuing without it` on every pi startup, and nothing else. The child coders and the four child dispatches of the run that reported it were unaffected, which is why it read as a repo-local artifact. (sty_929c7959, epic:pi-harness, supersedes:sty_6b8b9010)
  - **stdout is the JSON document; stderr is the operator.** The task-sync report, the backlog-reference count and the doc-validation `FAIL` lines now go to `cmd.ErrOrStderr()`, beside the warnings already there. Nothing is removed and nothing changes shape: a terminal shows both streams, and a failing leg still exits non-zero.
  - **The contract is pinned by a test, not by a comment.** `TestReindexStdoutIsSingleJSONDocument` runs the verb through the reproduced backlog-delta case, a task-sync report and a validation `FAIL` finding, and each decodes stdout with `json.Decoder`, requires a second `Decode` to hit `io.EOF`, and requires the moved line to be present on stderr and absent from stdout.

## [0.0.587] - 2026-09-30

### Fixed
- **A dispatched performer is attributed to its own dispatch, so the edit gate no longer refuses it while its story status still reads as the source status.** The coder dispatched for an epic's `in_progress` step was refused every source edit ("story status is still plan with the dispatch in flight, so the hook saw no performing story"), while the four child coders committed normally. The gate already allowed the marked agent during an in-flight transition; the defect was upstream, in seat selection. A performer inherits the driver's `SATELLE_SESSION`, and with several sibling seats live — or a seatless performing sibling — `pickSessionSeat` returned a sibling's seat or none at all, so the permission check never saw the performer's own lease. (sty_8d7d1c45, epic:exit-remediation)
  - **The dispatch names its item, so the pick uses it.** `resolveSeats` now selects the live seat whose item equals `SATELLE_DISPATCH_ITEM` before it consults session or worktree, through `dispatchSeat`. Nothing about the choice is ambiguous once a marker names the item, so the `unstampedAmbiguous` report is untouched for sessions without a marker.
  - **Selection grants nothing.** Permission still needs that item's lease in flight and a route allocation to the marked agent (`editPermitted`, read from the route, never compiled in); the foreign-tree fence runs before the seat is consulted. A wrong agent, or a marker whose item holds no live lease, is still refused.
  - **A dispatch with no lease says so.** The deny names the dispatch (item, step, agent) and "no live in-flight lease" rather than attributing the refusal to whichever other story the session resolved.
  - **Proof is store-backed.** `TestDispatchedPerformerAttributedToItsOwnLease` runs an epic committed at `ready` with a transition in flight: alone in the store with a performer whose session id differs from the one on the seat (the reported failure), and beside sibling seats acquired before and after it (store order is `seat_key, acquired_at`, and each case asserts where the epic actually lists), with a shared or differing performer session, and with an unstamped session made ambiguous by a seatless sibling. Without `dispatchSeat` the differing-session cases and the ambiguous case fail. `TestDispatchedPerformerRefusedOutsideItsDispatch` and `TestDispatchedPerformerStillFencedOutsideTheRepo` cover the refusals.

## [0.0.586] - 2026-09-30

### Fixed
- **The edit gate resolves the story that holds the seat, not the first performing story in store order.** With an epic left seatless at `ready` — the state the epic choreography requires while its children walk — alongside a child correctly engaged at `in_progress` with a live seat, the PreToolUse gate named the EPIC, refused an edit the caller was entitled to make, and told the agent to re-acquire a seat it did not need. `pickSessionSeat` walked the store, skipped only live leases, and returned the first performing story it found, so the answer depended on store order rather than on seats. (sty_fbbb4aee, epic:exit-remediation)
  - **A seat for THIS worktree decides attribution.** An unstamped session matches by worktree; a session stamped with a different id gets no pick, and `attributedDenyReason` then checks this worktree's live seat ahead of any seatless story and names it through `seatMismatchEditReason`. This is attribution only — permission is not widened anywhere.
  - **An unstamped session with several performing stories is told so, instead of being attributed to whichever seat sorted first.** `pickSessionSeat` now RECEIVES the seatless performing set and delegates to `unstampedAmbiguous`: no session id, no live seat for this worktree, and `len(live)+len(dropped) > 1` returns no pick, so the gate reports the ambiguity and lists every candidate. The `live[0]` fallback now fires only when there is genuinely one performing story. The same rule applies to commitgate. Permission is unchanged for a stamped session, a worktree match, or a single performing story.
  - **Every denial now names the seat.** `seatToken` renders `stale`, `none`, or `live (session …, worktree …)`, and it appears in the dropped-seat, seat-mismatch, ambiguity and both allocation texts — so a refusal says which story the edit would be attributed to, in what state, and under whose seat, instead of only saying a seat "was dropped".
  - **The advice no longer contradicts the situation.** In the seat's own worktree the mismatch text no longer says "run from that seat's worktree".
  - **Proof is store-backed, not asserted on the pure chooser.** `TestEditGateUnstampedLiveSeatPlusSeatlessStory` drives `resolveSeats` and `hookDenyReason` with one live lease in `/w/a`, a second story performing with no lease, no session stamp and the hook running from `/w/other`, and asserts both stories are listed, that "re-acquire" is absent, and that a worktree or session match still resolves. `TestEditGateEpicReadySeatlessChildSeated` runs both store orders. On the baseline these fail.
  - **The unintended reach of the rule is stated, not buried.** `stopcheckSeat` and `pruneSeat` inherit the same chooser, so an unstamped session outside every seat's worktree with two or more performing stories is no longer treated as engaged there. That is the same defect this change removes, in those callers.

## [0.0.585] - 2026-09-29

### Added
- **A scoped in-loop fix lane: a typed `in-loop-fix` claim with an absolute bound, and a ledger row per claim.** When an agent hit a small, self-evident fix mid-step, satelle offered two wrong answers: engage a full story for a two-line change, or make an ungated edit that is a rule breach and invisible afterwards. `satelle fix claim <path> --reason … --lines N --test <named test>` records the claim BEFORE the edit, and the edit gate then allows that ONE edit. `satelle fix report` analyses the rows over any window. (sty_4b694872, epic:pi-harness)
  - **The lane changes WHO MAY EDIT, never WHAT IS JUDGED.** It joins the existing adapter-neutral PreToolUse predicate as a second look, taken only after the ordinary rule has already refused, so claude, grok and pi all reach it through the same harness matchers with no provider-specific code. `TestFixLaneNeverChangesWhatAGateJudges` compares every gate skill body and every edge's reviewer set byte-for-byte with the lane off, on, and after a claim is used.
  - **The bound is absolute, and it fails closed.** A claim is refused — and the refusal is itself a ledger row carrying the class — when it touches product surface, a gate skill, a reviewer rubric, a workflow, a principle, the constitution or the repo config, or names no proving test. **The lane does not exist unless the repo declares `[fix_lane] product_surface`**: with none declared every claim is refused with class `undeclared-bound`, because a lane with no declared product surface would be anything-goes. The substrate classes are DERIVED from where the repo actually keeps its substrate, so relocating it keeps every refusal.
  - **No bound number lives in Go.** `max_lines` is read from the repo's `[fix_lane]`, and the default ships in the embedded `substrate/config/fix_lane.toml` as template text that the help topic derives from. A repo that sets no bound falls to that default, never to "anything goes". `TestNoBoundLiteralInGo` scans the lane's sources, including comments and the seed.
  - **One claim licenses ONE edit and dies at the story's next transition**, so it is never a standing licence. It is refused outright under a dispatch or relay marker, and while a transition is in flight — a lane is for the driving session, and a dispatched performer or a reviewer still edits nothing.
  - **Every claim and every refusal is a row, before the edit.** `fix_claim` precedes `fix_claim_used`, both durable in SQLite and readable from another process, so the timeline proves the claim came first. Without a row per exception the exception rate, its size distribution and its test coverage are unmeasured — and an unbounded lane is indistinguishable from no lane. `satelle fix report` yields, over any window: claim count, declared and edited size distribution, refused-claim count by class, and per-gate reject count.
  - **The rule lives in substrate, not only in Go.** `satelle-edits-require-a-story` now names the lane as the one exception to the executor-state rule — explicitly not to the engaged-story requirement — and points at `satelle help fix-lane` for the refused classes.

### Changed
- **`satelle agents install claude|grok|pi|all` in the README.** The documented target list predated 0.0.584's pi scaffold and understated the thing it described. Fixed through the new lane itself, as its dogfood: a recorded claim, one line, rows `evt_d66c6780` and `evt_83ae07e7`. (sty_4b694872)

## [serve-v0.0.104] - 2026-09-29

### Added
- **The service carries the in-loop fix lane above**, its ledger kinds, and the amended `satelle-edits-require-a-story` principle and `fix-lane` help topic. (sty_4b694872)

## [0.0.584] - 2026-09-29

### Added
- **`satelle agents install pi` holds a pi session to the edit gate and the commit gate.** pi had gate DELIVERY — a measured `HarnessFacts` row, so a verdict started by a pi session is claimed and lands — but no gate ENFORCEMENT: there is no path outside a harness hook, so a pi session got the benefit of the gates without their cost, and product edits to `internal/agentcli`, `internal/cli`, `internal/help` and `tests/` were made in this repo on 2026-09-29 with no engaged story and nothing objecting. `satelle agents install pi` now writes `.pi/extensions/satelle.ts`, which wires all four `fullHookEvents` onto pi's extension events and calls the same `satelle hook` verbs the claude and grok scaffolds wire. `satelle agents remove pi` removes it, leaving no residue: a foreign extension in the directory survives, and an unowned `satelle.ts` is skipped and reported. (sty_b3c7b37d, epic:pi-harness)
  - **The event map, and what each event can actually refuse.** a pi `tool_call` on `edit`/`write` → `satelle hook gate`; on `bash` → `satelle hook commitgate`; `session_start` → `satelle hook context`; `before_agent_start` → `satelle hook prompt`; the settle event → `satelle hook stopcheck`. The edit gate honours `edit_exempt_paths` and `edit_exempt_globs` exactly as the claude and grok scaffolds do, and an engaged story's edit is allowed.
  - **A deny is enforced, not logged.** pi blocks a `tool_call` whose handler returns `{ block: true, reason }`, and blocks on a throw. The refusal carries the edit-gate rule verbatim.
  - **One owner for the infra-failure policy.** The PreToolUse handlers exec the SAME wrapper the claude and grok scaffolds use (`.satelle/hooks/satelle-hook.sh gate|commitgate pi`), so gate and `git commit`/`git push` fail CLOSED with the infra reason when satelle is unusable — the wrapper's rule, not a rule restated inside a harness adapter, which is what would otherwise leave the gate inert whenever satelle is missing from pi's PATH. Session start, prompt and stop call `satelle hook` DIRECTLY and fail open, exactly as claude's direct commands do. `maybeRemoveSharedHookScript` now counts `.pi/extensions/satelle.ts` as a scaffold that references the wrapper, so removing claude or grok no longer deletes a script pi still calls.
  - **The one event pi cannot veto.** `agent_settled` is notification-only: a refused stop is delivered as a follow-up user message that starts another turn, so the agent cannot settle silently on an ungated edit — but a user who kills the session, or a non-interactive run that exits on settle, is not held. The re-prompt carries `stop_hook_active`, so it is one per stop chain rather than an unbounded loop. This limitation is stated in the extension header itself, not only in a changelog.
  - **Parity is a test, not a convention.** The pi scaffold's event set is asserted equal to `fullHookEvents`, and each event's verb AND invocation form (wrapper vs direct) is asserted equal to what `buildClaudeHookSettings` and `buildGrokHookSettings` write, so a fifth event or a changed claude form fails the build.
  - **`satelle agents install all` now installs pi**, and its help says so: `all` will create `.pi/` in repositories that do not use pi. Scaffold drift detection covers the extension and `satelle init` heals it, so the builder/healer/checker table stays one table.
  - **`[harness.pi]` gets its own `context_limit_bytes` row** (equal to `unknown`, not claude's), so a pi session's injected principles:session context is bounded on its own terms. (sty_b3c7b37d)

### Changed
- **The `hook` verbs accept `--harness pi`.** `hook gate`, `commitgate`, `context`, `prompt` and `stopcheck` route pi through the same claude-shaped envelope the non-grok path already emits, so there is no new envelope type and no pi sniffing in the CLI. (sty_b3c7b37d)

## [serve-v0.0.103] - 2026-09-29

### Added
- **The service carries the pi harness enforcement hook events above.** (sty_b3c7b37d)

## [0.0.583] - 2026-09-29

### Added
- **A gate rejection is now a loop on every edge in this repo, not operator work.** The reject-budget mechanism was already generic — `reject_budget` is read from the destination step and enforced with no keying on a step name — but only `[readied]` declared one, so a rejection on any other edge failed the command outright and left the story sitting in its source status with the reason to be re-read by hand. Every gated step in this repo's step catalogue now declares `reject_budget = 3`: the verdict and the reviewer's reasoning come back to the session that requested the edge, the story holds its source status, the retry is admitted until the budget is spent, and the presentation after that is refused with the last objection quoted for the orchestrator's park. A budget on the TERMINAL `[closed]` step holds the story in `release` and admits the retry into the same edge rather than re-entering `done`; the shared `done` status resolves against the category's own route, so one category's budget cannot leak onto another's close. The budget is a repo's opinion, not the binary's: the embedded default declares none, and a step that declares none behaves exactly as before. (sty_4cf2c585, epic:pi-harness)

### Changed
- **`satelle workflow show <category>` reports each step's declared reject budget.** The derived route view named the entry gates and the terminal flag but not the budget that bounds them, so the route and the catalogue could disagree with nothing on screen to show it. A step that declares no budget prints no line, unchanged. (sty_4cf2c585)

## [serve-v0.0.102] - 2026-09-29

### Changed
- **The service carries the 0.0.583 review-loop work.** (sty_4cf2c585)

## [0.0.582] - 2026-09-29

### Fixed
- **A git worktree is the repository, not a project of its own.** A linked worktree registered itself in the workspace registry and relabelled its parent's mirror partition after its own directory name, so `http://localhost:8787/r/satelle/` returned 404 on every worktree dispatch and a `satelle workspace add` from the main tree was needed to restore it — a cycle that recurred three times in one session. `git rev-parse --git-common-dir` already resolves from any worktree to the main repository's `.git`, the same fact the runtime repo key collapses worktrees with, so identity now canonicalises through it and a worktree keeps its own `.satelle` for authored substrate. Both the registry path and the partition slug, path and project name are canonical; the data dir, substrate and doc reads stay per worktree. (sty_cd219594, sty_dfc9b100)
  - **The runtime path marker was a data-loss path, and is fixed.** The runtime dir is keyed by repo key, which collapses worktrees, but the marker recorded the INVOKING root — so it flipped to whichever tree opened last. After `git worktree remove` the main repository's whole plane then read as stale and `runtime reap` could offer to delete it. The marker is an identity sink, so it now records the canonical root.

## [0.0.581] - 2026-09-29

### Fixed
- **The gate-value view groups a seat's spend under its role, not under `unknown`.** `normalizeSkill` folded every empty skill into `unknown`, so one seat appeared under two labels — the 30-day dogfood showed `coder / coder@sonnet` at 57 rows / $181.61 and `unknown / coder@sonnet` at 24 rows / $54.20, with orchestrator, reviewer-consult and planner rows under `unknown` too. A reader could not separate gate spend from executor and orchestrator spend, and per-seat totals split across rows. The label now resolves the recorded skill, then the recorded role, and reaches `unknown` only when neither is recorded. The view still only reports. (sty_95b6b869)

### Fixed (test)
- **The instruction-change gate and its trigger ship with their fence and wiring fixtures.** Both halves landed in 0.0.580 without the integration fixtures the substrate checks require, so `make integration` — and `satelle-integration-check` on the way to release — failed. Both checks are integration-tagged, so `go test ./...` never ran them. (sty_012394c0)

## [0.0.580] - 2026-09-29

### Added
- **`pi` is a governed harness, and a gate verdict reaches a pi session.** pi joins claude and grok in the harness vocabulary, is detected from `PI_CODING_AGENT` / `PI_SESSION_*`, and publishes `executable = pi` on the ledger. A pi driver can now land a gated write: story create, story amend and every transition gate a pi session issues are claimed and their verdict delivered, instead of detaching and waiting forever. (sty_f3dc2a97, epic:pi-harness)
  - **A measured row, not the conservative floor.** pi carries a 10m background cutoff with its basis naming the measurement — a live pi 0.87.1 foreground call held 25m under an explicit timeout and 45s with none, so pi does not auto-background a long command the way grok does at 15s. A pi-only session therefore does not need a hand-off, and the gate completes in the foreground.
  - **What this fixes:** an unrecognised harness took the `HarnessUnknown` floor (10s) and the pending line said so. Every gate then detached at 10s, and the finished handle was never claimed because the claim is delivered by the harness's own hooks. Twelve gates on 2026-09-29 stranded this way, each with a correct reviewer verdict that never landed.

### Fixed
- **The hand-off decision follows the session's harness, not every scaffold a repo keeps.** `[gate] handoff = auto` decided from the union of the harnesses a repo has installed, so a repo carrying both `.claude/` and `.grok/` forced a pi session to hand off on grok's 15s cutoff — and then waited on a Stop hook the pi session never fires. The set is now the session's own harnesses when the environment names one, and falls back to the installed scaffolds only when no session marker is present. A grok or claude session in the same repo is unchanged. (sty_f3dc2a97)

### Changed
- **The create-review gate no longer researches the repo to validate a story.** `satelle-story-create-review` kept a premise section that cited `satelle-story-plan-review` for its discipline; that skill bounds falsification to existence claims on a presented plan, and the create gate had widened it to behaviour claims on the story body and ACs. The section is now a dead-premise check — reject only a story asking for something this repo does not have or has removed, cited by file/symbol — with counts, sibling-story existence, whether referenced work has landed, and dependency ordering each named as NOT grounds at create. The content & alignment and classification sections are unchanged. Observed on this repo, the section produced cross-story dependency checks and a repository audit, and cost 4-8 minutes per create; the same creates now take about 90 seconds. (sty_012394c0)
- **`when` is a gate key, and a trigger is an enqueue precondition.** A gate may name `when = "<check-skill>"`: the check exits 0 to run the gate, 1 to skip it (a whitespace- or typo-only change), and any other outcome runs it. An enqueue precondition, never a verdict. The embedded shipped route carries `satelle-instruction-change-review` behind `satelle-instruction-change-trigger` on every editable lane, and containers carry none because they perform no work. (sty_012394c0)
- **`satelle validate` reports injected context size.** Each kind's bytes and estimated tokens per seat, warning — never refusing — when a repo-configured budget is exceeded. No budget number is hard-coded in Go. (sty_012394c0)
- **`agentcli.SessionMarkerEnvNames` publishes the marker env names.** A caller that must neutralise harness detection clears exactly what the code reads, instead of a hardcoded list that silently drifts every time a harness is added. (sty_f3dc2a97)

## [serve-v0.0.101] - 2026-09-29

### Changed
- **The service embeds the pi harness row, the session-scoped hand-off decision, and the `when` gate key above.** (sty_f3dc2a97, sty_012394c0)

## [0.0.579] - 2026-09-29

### Fixed
- **`satelle update` installs the highest published satelled, not the first one GitHub lists.** GitHub's release list is not ordered by version (it returned serve-v0.0.99 ahead of serve-v0.0.100), and update took the first `serve-v*` tag it found. Every install stopped at satelled 0.0.99 once the serve patch number reached three digits. update now reads every release page up to the existing cap and installs the numerically highest `serve-v` tag. It ignores drafts and tags that do not parse as a version. The "no published release" and "cap exhausted" errors are unchanged. Run `satelle update` again to pick up satelled 0.0.100. (sty_47cf8289)

## [0.0.578] - 2026-09-29

### Added
- **Spend budgets, set by the repo: `context_budget` and `turn_budget`.** A binding in `agents.toml`, its `[defaults]`, or a workflow step can declare a context budget (input tokens) and a coder turn budget (model turns for one dispatched run). Resolution is step, then binding, then `[defaults]`. `0` or absent means unset, and a negative value is refused at load. The binary ships no number.
  - **Turn budget reaches the harness where it can:** a command template takes `{max_turns}` (claude `--max-turns`, grok command). Grok ACP and unrecognised harnesses record an adapter-named "turn budget unavailable". `satelle help agent-dispatch` shows it in a new `turn budget` capability column.
  - **No budget set:** satelle measures, warns and records on the `agent_invocation` row. A story is never parked for spend.
  - **Budget set and overrun:** a `budget_overrun` row is recorded with its reason. If the step has a `rework` key, the output directs the orchestrator to `satelle story rework <id>`. Otherwise the story is blocked with the reason kept on the ledger. That consequence exists only because the repo configured the budget.
- **Engage warns the driver to start a fresh session.** When the driving session's driver-usage rows already cover another story, engaging a story prints a warning to start a fresh session and records a `session_advisory` row. A repo-set context budget is a further trigger. It is a warning only, and engage is never refused.

### Changed
- **The coder-step edit refusal names the right next step.** During a step the route allocates to a dispatched coder, the edit gate's refusal of the driver's own edit now names `satelle story rework` only when that step has a `rework` key, and the one-shot coder dispatch otherwise. Steps allocated to the in-loop executor still accept the driver's edits. All four rules are documented in `satelle help agent-dispatch` ("Spend budgets") and `satelle help workflows`. (sty_a7914904)

## [serve-v0.0.100] - 2026-09-29

### Changed
- **The service embeds the spend budgets and engage advisory above.** (sty_a7914904)

## [0.0.577] - 2026-09-29

### Changed
- **A reviewer configuration satelle cannot hold to its tool grant now runs with a warning instead of being refused.** 0.0.575 refused four configurations before they started; each now runs, and the gap is shown, recorded and documented. satelle's default is to let the operator configure what they choose and to warn about what is unsupported or below standard, not to block it.
  - **What runs with a warning:** a grok ACP reviewer (`grok agent stdio`); a harness no adapter recognises, with no `isolation` key; grok with `--always-approve` and no `--tools` allow-list equal to the grant; claude skipping permissions while its grant scopes a tool. This covers gate reviewers, `[reviewer-summary]` and `[reviewer-consult]`.
  - **The warning is never silent.** One line on stderr names the binding, the adapter, the gap (for grok ACP: "tools not held to the grant" and "usage accounting not to standard") and the fix. The gap is recorded as `isolation_limitation` and `offered_tools_source` on the `agent_invocation` row and as a `reviewer-isolation-warned` ledger event. `satelle doctor` reports each binding as a WARN naming the gap and the fix. `satelle help agent-dispatch` lists every warned configuration.
  - **`isolation = "operator-attested"` is now the operator's acknowledgement of any of these gaps.** It suppresses the dispatch warning and turns the doctor WARN into an INFO; the ledger still records the limitation.
  - **Mid-session ACP checks no longer abort the run.** A peer that offers no ask mode, or a tool that ran outside the grant without a permission request, is recorded (`reviewer-isolation-gap`, `reviewer-isolation-breach`) and warned; the session continues and the verdict stands. An observed breach is warned even for an attested binding.
  - **Unchanged:** the default claude and grok-command trims (`--tools`, `--strict-mcp-config`, `--permission-mode default`), because they change nothing the operator configured. A stock claude or grok-command reviewer reports nothing.
  - **New principle `satelle-configure-freely`:** configurations are warned, not blocked, and no rule is undocumented. (sty_2d5e583a)

## [serve-v0.0.99] - 2026-09-29

### Changed
- **The service embeds the warn-not-block reviewer isolation above.** (sty_2d5e583a)

## [0.0.576] - 2026-09-29

### Added
- **Reviewer gates on one edge can run bundled: one session judges every rubric and returns one verdict per rubric.** A step opts in with `bundle = true` in `step.toml`; without it, gates run as separate sessions exactly as before.
  - Each rubric's verdict is recorded under its own skill, any reject rejects the edge, and a rubric the bundled reply leaves out fails closed.
  - Gates bundle only when they share binding, model, effort and tool grant. A rubric whose front matter says `independent: true` keeps its own session, and a gate backed by a command check never enters the bundle.
  - A bundle writes one usage row. `satelle story cost --gate-value` shows each rubric's share labelled as an allocation of that row, and an unavailable usage stays unavailable in every share.

  **Bundling is opt-in, and the embedded default does not enable it.** The parity run over the frozen reviewer corpus (3 runs per case per mode, claude and grok command) found that the bundle missed no defect the separate sessions caught, but rejected known-valid cases the separate sessions accepted (claude: 8 of 9 valid runs; grok: 6 of 9). The corpus also has no cases yet for the intent, architecture, scope and workflow-change rubrics. (sty_23e10d92)

## [serve-v0.0.98] - 2026-09-29

### Changed
- **The service embeds reviewer bundling above.** (sty_23e10d92)

## [0.0.575] - 2026-09-28

### Breaking
- **A reviewer that cannot be held to its tool grant is refused before it starts.** Every reviewer dispatch — edge gates, the step summariser and the rework consultant — now goes through one isolation check:
  - **claude:** reviewer spawns add `--tools <grant>`, `--strict-mcp-config` and `--permission-mode default`, so only granted built-in tools are offered, no MCP server loads, and a settings-level bypass mode cannot switch off a scoped grant such as `Bash(satelle:*)`. A claude reviewer that skips permissions while its grant scopes a tool is refused.
  - **grok command:** admitted when `--tools` lists only granted tools. `--always-approve` with no `--tools`, a blank `--tools`, or a wider one is refused.
  - **grok ACP (`grok agent stdio`):** refused as a reviewer. The peer reports no permission mode and cannot be forced to ask, and `~/.grok/config.toml` can set always-approve, so an out-of-grant tool could run without a permission request. Use the grok command transport.
  - **Any other command** (a script, or a harness satelle has no adapter for) is refused unless its binding declares `isolation = "operator-attested"`, the operator's statement that it offers nothing outside the grant.

  The refusal names the adapter and the tool classes, and no process is started. `satelle doctor` warns about any binding that will be refused, and names it. Each reviewer invocation now records its system prompt bytes and the number of tools actually offered, with the source (`flag` or `harness`), or an adapter-named "unavailable". It is never the grant length and never a silent zero. (sty_ef3efb51)
- **Migration:** a repo whose `.satelle/workflows/agents.toml` or `~/.satelle/agents.toml` binds a reviewer, reviewer-summary or reviewer-consult seat to `grok agent stdio`, to a grok command without `--tools`, or to a custom script must change it:
  - move grok reviewers to the grok command template with `--tools read_file,grep,list_dir`;
  - add `isolation = "operator-attested"` to a custom reviewer command you vouch for.

  Run `satelle doctor` to list the affected bindings.

## [serve-v0.0.97] - 2026-09-28

### Changed
- **The service embeds the reviewer isolation above.** (sty_ef3efb51)

## [0.0.574] - 2026-09-28

### Fixed
- **A readiness performer proposing from backlog now receives the orchestrator's messages.** Under a `propose = true` step (0.0.571) the planner runs while the story is still in backlog, before any engagement baseline exists, and the message window returned nothing there, so a `satelle story message --to planner` sent after a rejected round never reached the next plan. The engagement window now has a pre-engagement period: while a story sits at its route's start state and has re-entered it since its last baseline (or has none), messages are stamped and read against that period, by one function for both sides, so messages from an earlier engagement still stay out. A decision message is no longer cut at 2 KiB: bodies up to 8 KiB reach the performer whole, and a longer one is marked truncated with the `satelle story messages <id> --to <addr> --since <time>` command that fetches it. (sty_36ac4319)

## [serve-v0.0.96] - 2026-09-28

### Changed
- **The service embeds the message-window fix above.** (sty_36ac4319)

## [0.0.573] - 2026-09-28

### Changed
- **Migration: a claude-only repo runs gates in the foreground again, as before 0.0.563 (claude's 120s cutoff is above the 1-minute line; a repo whose edges run longer sets `handoff = "on"`).** Whether a gate-running verb hands off (returns a handle, delivers the verdict by notification) is now a per-repo choice, `[gate] handoff = "auto" | "on" | "off"`, and the default is `auto`: hand off only when a configured driver harness has a background cutoff under 1 minute — the shortest a gate-running command was measured to hold its caller (measured driver windows in this repo's ledger: 53s to 6m55s; grok's cutoff is 15s, claude's 120s). Under a minute a hand-off is unavoidable; above it `auto` is not a guarantee, since a multi-gate edge can outlast claude's 120s — such a repo sets `handoff = "on"`. Reason: a hand-off exists to beat a harness's background cutoff; where none applies it only adds a delayed notification that a caller may check too early (this repo's own rework run did), and it changed behaviour for every script with no terminal. A harness with no facts row takes the conservative floor, so an unrecognised harness still hands off. Set `handoff = "on"` to restore the 0.0.563 behaviour, or `"off"` to never hand off. `SATELLE_GATE_MODE` still overrides the setting. (sty_8ee31f26)

## [serve-v0.0.95] - 2026-09-28

### Changed
- **The service embeds the per-repo gate handoff setting above.** (sty_8ee31f26)

## [0.0.572] - 2026-09-28

### Fixed
- **A story's seat is no longer dropped while its holder or a dispatch it started is alive.** One liveness predicate in the lease package decides whether a seat is held: its holder process is dead, or its lease has expired with no live in-flight or activity process. Acquire's steal path, reap and the edit gate all call it, so a second session can no longer take a seat the gate still treats as held. A keep-alive now holds the seat through long gates and rework relay runs. (sty_7f3e6fd3)
- **An epic or parent waiting on its children no longer counts as performing.** A step can declare `waits_on_children = true` in `step.toml`. While a container sits at that step with non-terminal children, the edit gate, the Stop hook and the deny message ignore it, so its children can be engaged without releasing its seat first. A story at a dispatched-performer step that has child tasks still counts as performing. (sty_7f3e6fd3)
- **A dispatched coder can edit on a correct seat.** The edit gate now allows a coder dispatched on entry to its step while the story's seat is settled. A coder working longer than 30 minutes also no longer loses its in-flight status, because the keep-alive refreshes it. (sty_7f3e6fd3)

## [serve-v0.0.94] - 2026-09-28

### Changed
- **The service embeds the seat and edit-gate fixes above.** (sty_7f3e6fd3)

## [0.0.571] - 2026-09-28

### Added
- **A story's readiness can be one step that plans while the story is still in backlog, and its definition freezes only when implementation starts.** Three step keys make this possible in `step.toml`:
  - `propose = true` runs the step's performer before the entry gates, so the reviewers judge the plan it just produced;
  - `freeze = true` marks the step whose entry freezes title, body and acceptance criteria;
  - `reject_budget = N` refuses a presentation after N rejected rounds, quoting the last objection so the orchestrator can park the story and ask the developer.

  A rejected readiness edge leaves the story in backlog, where its definition stays editable. A round counts once, however many reviewers reject in it. Every title, body or acceptance edit made before the freeze is recorded as a `definition_edited` ledger row with actor, old and new value, and edits ride in the gate payload so the intent review can judge them against the story's purpose. `recover` in `done.toml` now also accepts an array, which declares more than one way back.

  `satelle story definition-edits <id> [--since-edge from:to]` enumerates the edits. The embedded plan-review and intent-review rubrics require every blocking finding in the first pass, and later rounds only verify earlier findings unless there is new evidence. Routes that do not use the new keys behave as before. (sty_5262592e)

## [serve-v0.0.93] - 2026-09-28

### Changed
- **The service embeds the readiness-step mechanism above.** (sty_5262592e)

## [0.0.570] - 2026-09-28

### Changed
- **A Breaking release blocks a repo's commands only when `satelle init` heals it.** A `### Breaking` entry refuses ordinary commands in a repo stamped before it only when the entry carries an `init-heals:` bullet — the release's own declaration that `satelle init` performs every migration it names. A Breaking entry without it (manual migrations such as hand edits or a `jq` command) now lets commands run and prints the entry's remediation once per session on stderr; `satelle init` still re-stamps, and `satelle doctor` reports the unacknowledged Breaking release as a finding. The session-start advisory says which of the two applies. 0.0.568's entry is unmarked on purpose: its migrations are manual. (sty_6e143870)

## [0.0.569] - 2026-09-28

### Fixed
- **A repo whose satelle scaffolding is older than the binary keeps working and is told to heal, instead of having its commands refused.** When a release changed satelle's hook wrapper or harness hook commands, every store-backed command in a repo still on the older scaffolding failed with "deployed harness scaffolding is stale", and `satelle doctor --all` showed every registered repo unhealthy. Those files are satelle-owned and `satelle init` rewrites them idempotently, so stale scaffolding now prints one line on stderr naming the stale files and the heal command, and the command runs. Hook handlers are unaffected and stdout stays clean for JSON output. A release whose CHANGELOG entry declares `### Breaking` still refuses until `satelle init` is run, and `satelle doctor` still reports `scaffold.stale`. (sty_e56ea643)

## [0.0.568] - 2026-09-28

### Breaking
- **Codex and Antigravity (agy) support is removed; satelle drives claude and grok only.** Neither can be controlled headless from its command line: agy has no system-prompt flag and denies tools only through its own hooks file, and codex's read-only mode is an OS sandbox that also breaks satelle's read verbs. Everything specific to them is deleted: the adapters, launchers, hook scaffolds and wrapper-script arms, session markers and payload fingerprints, capability and harness-facts rows, usage and model parsers, model-order entries, reviewer sandbox rules and help text. `satelle init --harness codex`, `satelle init --harness antigravity` (and `agy`), and `satelle agents install codex|antigravity` are now unknown-harness errors. (sty_941e60cb)
- **Migration: remove stale bindings by hand.** In `.satelle/workflows/agents.toml` and in every `~/.satelle/agents.toml` profile, delete any binding whose `command` starts with `codex`, `npx -y @agentclientprotocol/codex-acp` or `agy`, and delete any `codex` entry under `[model_order]`. satelle does not reject them for you: a bare `codex` or `agy` token is refused as a removed preset, but a full command template still runs as a generic operator command with its usage recorded as unavailable, so nothing flags it as stale.
- **Migration: remove satelle's hook entries from a repo that was initialised for them.** `satelle init` no longer knows those hook file shapes, so it does not heal them. Run this once from the repo root (needs `jq`). It removes only satelle's entries (commands containing `satelle-hook.sh`, `satelle hook ` or `satelle reindex`) from `.codex/hooks.json` and `.agents/hooks.json`, and deletes a file only when nothing else is left in it. User hooks (including `{"enabled":true}`, `{}` and empty arrays) and non-array keys are untouched; on invalid JSON it removes its temp file and leaves the original as it was.

```bash
own='def own: (type=="object") and ((.command? // "")|type=="string" and test("satelle-hook\\.sh|satelle hook |satelle reindex")); def grp: type=="object" and (.hooks|type)=="array"; def had: any(.[]; own or (grp and any(.hooks[]; own))); def prune: map(if grp then (if any(.hooks[]; own) then (.hooks |= map(select(own|not))) | select((.hooks|length)>0) else . end) else select(own|not) end); def pobj: with_entries(if (.value|type)=="array" and (.value|had) then (.value |= prune) | select((.value|length)>0) else . end); def touched: [.[]|select(type=="array")|had]|any;'
heal() { f=$1; [ -f "$f" ] || return 0; { jq "$own $2" "$f" > "$f.tmp" && if [ "$(jq -c . "$f.tmp")" = '{}' ]; then rm -f "$f" "$f.tmp"; else mv "$f.tmp" "$f"; fi; } || { rm -f "$f.tmp"; echo "satelle heal: $f left unchanged (invalid JSON?)" >&2; }; }
heal .codex/hooks.json '(if (.hooks|type)=="object" and (.hooks|touched) then (.hooks |= pobj) | (if .hooks=={} then del(.hooks) else . end) else . end) | if ((.description? // "")|type=="string" and startswith("satelle-owned")) then del(.description) else . end'
heal .agents/hooks.json 'with_entries(if (.value|type)=="object" and (.value|touched) then (.value |= pobj) | select([.value[]|select(type=="array")]|length>0) else . end)'
```

## [serve-v0.0.92] - 2026-09-28

### Changed
- **The service embeds the codex and Antigravity removal above; it drives claude and grok only.** (sty_941e60cb)

## [0.0.567] - 2026-09-28

### Fixed
- **Two timing-flaky tests from the Stop-wait change no longer fail on slow runners.** The killed-child lifecycle test polls to a deadline instead of probing fifty times back to back, and the unverified-liveness Stop test proves "did not wait" against a 5-second bound instead of a 40ms one. The flake failed CI for 0.0.566, so that version was never tagged; its changes ship in this release. (sty_154a7b88)

## [0.0.566] - 2026-09-28 (not tagged; shipped in 0.0.567)

### Added
- **satelle recognises Antigravity (agy) sessions.** A command run by an agy driver carries `ANTIGRAVITY_AGENT=1`, which satelle now reads as an in-loop agy session; agy hook payloads (`conversationId`, `toolCall`) classify as antigravity without matching claude, grok or codex payloads; the caller's model is taken from the payload's `modelName` as reported (agy may report `auto`). The capability table carries an `antigravity command` row with an agy-named reason for every capability agy does not report (token usage, cache split, resolved model, live session). (sty_68a24884)

## [serve-v0.0.91] - 2026-09-28

### Changed
- **The service embeds the Antigravity detection and capability row above.** (sty_68a24884)

## [0.0.565] - 2026-09-28

### Added
- **Antigravity (agy) is a supported harness.** `satelle init --harness antigravity` (alias `agy`) and `satelle agents install antigravity` write a per-repo `.agents/hooks.json` in agy's own format: PreToolUse gates file writes (`write_to_file|replace_file_content`) and commits (`run_command`), PreInvocation injects satelle's context as an agy `injectSteps` ephemeral message, and Stop blocks with agy's `decision: "continue"` so a handed-off gate's verdict is delivered. Hook payloads are read from agy's `toolCall.args` (`TargetFile`, `CommandLine`, including JSON-encoded values) and `conversationId`. Denials use agy's top-level `{"decision":"deny"}`, and the fail-visible wrapper emits the same shape when satelle is missing. Heal is idempotent, user hooks are preserved, and `satelle agents remove antigravity` removes only satelle's entries. A `satelle-antigravity` launcher runs `agy`. Nothing is installed outside the repo. (sty_9e88b82f)

## [0.0.564] - 2026-09-28

### Fixed
- **A driving session waiting on a handed-off gate is never released to idle while the gate is still running.** The Stop hook used to wait up to 25 minutes for a gate this session handed off and then allow the stop, so a longer gate stranded the driver until someone typed. It now blocks the stop with a one-line still-running note (handle, elapsed time, "end your turn to keep waiting; do not poll"), and the next stop waits again; the verdict is delivered exactly once when the gate finishes. The loop ends when a gate is found dead or unfinishable (process gone, a reused process id, unreadable handle files), which is reported as died, never skipped. Process identity is checked on Linux, macOS and FreeBSD (start time) and Windows (exit code and creation time); where the platform offers neither, the stop is allowed at the bound with that reason stated. `SATELLE_GATE_STOP_WAIT` is clamped below the installed Stop-hook timeout, and prompts are never told about running gates. (sty_5f15f263)

## [0.0.563] - 2026-09-28

### Changed
- **Migration: re-run `satelle init` in each repo** so its Claude and grok Stop hooks carry the new timeout (`satelle init --all --yes` heals every registered repo). Without it, a finished gate's verdict still arrives, but at the next prompt instead of when the turn ends.
- **Migration: a script that calls `satelle story set` without a terminal now gets a handle for a slow gate.** Set `SATELLE_GATE_MODE=interactive` to keep the old foreground run with progress lines.
- **Gate-running verbs return fast to an agent and deliver the verdict as a notification.** Called with no terminal or from a detected agent session, `story set`, `story create` under `gate_create`, `story amend`, `story resummarise`, `story retrospect` and `story rework` return within a bound set below the shortest background cutoff of any configured harness (grok: 15s). They print no reviewer progress and return either the finished verdict or `{"gate":"pending","handle":"gw_…"}`. The gate keeps running detached, and the harness's Stop hook (or the next prompt) delivers only the verdict into the driving session. No command reports a pending handle's result, so polling cannot complete a wait. A terminal user sees the same progress as before. Completion notification per harness: claude yes, grok yes (observed on grok 1.0.41), codex an adapter-named limitation (no Stop hook). (sty_c4b92c9e)
- **Driver-usage rows record model calls, and `story cost` shows each gate wait's driver model calls**, read from rows taken at issue, at delivery and once the turn is recorded. On a grok dogfood run, one 25s gate wait cost the driver 2 model calls. (sty_c4b92c9e)

## [serve-v0.0.90] - 2026-09-28

### Changed
- **The service embeds the gate hand-off and the model-call columns above.** (sty_c4b92c9e)

## [0.0.562] - 2026-09-28

### Added
- **`satelle story cost --gate-value`** reports, per skill and per seat, invocations, dollars (priced subtotal plus the unpriced count), fresh tokens, accepts, rejects and dollars per reject. Filter with `--story`, `--epic` (the item and every descendant) or `--since`/`--until`; `--json` gives the same rows, with `cost_usd` null when nothing was priced. It reports only; it recommends nothing and writes no configuration. (sty_b8542a3a)

### Changed
- **`satelle story cost` leads with dollars and fresh tokens.** The summary headlines dollars (priced rows only, with the unpriced count — never `$0`), fresh input and output, with cache read and cache write as separate columns. Unreported usage reads `unavailable (N unreported)`, never `0`. Elapsed wall time (the story clock) and agent time (dispatch plus driver) are separate lines; an estimate is shown in the unit it was written in. Each driver session is its own row with the same columns, and an item with children shows each child and the family total. Existing `--json` fields are kept; the new figures are additive. (sty_b8542a3a)
- **One computation for cost figures.** `internal/costview` now owns the figures behind `story cost`, `story actual`, the `actual-*` tags, `measured_actual` in gate payloads and the web story page, so they cannot drift apart. (sty_b8542a3a)

## [serve-v0.0.89] - 2026-09-28

### Changed
- **The story page shows the same cost figures as `satelle story cost`**: dollars and fresh tokens first, cache columns, elapsed and agent time, estimate, driver rows and the family roll-up. (sty_b8542a3a)

## [0.0.561] - 2026-09-28

### Added
- **A frozen reviewer corpus for judging review changes.** `tests/reviewcorpus` holds, for each proof rubric (ready, start, integration, implementation, coverage, done), at least one frozen change a gate rejected — citing the recorded reject by story, skill and ledger id — and one it accepted. A coverage test fails if any rubric lacks a defect or a valid case. Later reviewer-cost changes (isolation, bundling, audit, warm resume) are judged against it. (sty_51a04b93)

## [0.0.560] - 2026-09-28

### Changed
- **Migration: `satelle story actual` no longer takes figures.** `--tokens` and `--time` are refused; run `satelle story actual <id>` to compute and record the actual from the ledger. Scripts or skills that pass figures must drop them.
- **Migration: the shipped estimate/actual check no longer requires hand-typed `actual-*` tags at the terminal edge.** A repo that keeps its own copy of `satelle-estimate-actual-review` under `.satelle/skills/` still demands them: run `satelle restore` (or delete the local copy) to take the shipped check, or run `satelle story actual <id>` before the terminal transition.
- **A story's actual is computed from the ledger.** Dispatch rows plus driver-usage rows give dollars (summed only over priced rows, with the count of unpriced rows), fresh input, unsplit input, output, cache read and cache write as separate figures, elapsed wall time (engage to terminal state, found by the workflow's shape) and agent time. It is written as `actual-*` tags and an `actual_recorded` payload right after a transition into a terminal state commits. A missing cost is `actual-usd:unavailable`, never `$0`. An item with children rolls them up by parent id. (sty_8eae81ac)
- **Tool-permission events are their own ledger kind (`tool_permission`)** and no longer count as agent invocations in `story cost`, `--by-skill` or the web timeline; older rows are filtered on read. (sty_8eae81ac)
- **`satelle story estimate` takes `--usd` and `--fresh-input` / `--output`.** `--tokens` stays as a legacy unit and is shown in its own unit, never compared to dollars or fresh input. (sty_8eae81ac)

## [serve-v0.0.88] - 2026-09-28

### Changed
- **The service embeds the computed actual and filters tool-permission rows out of the timeline's model view.** (sty_8eae81ac)

## [0.0.558] - 2026-09-28

### Fixed
- **A grok session still in its first turn reads as "nothing yet", not unreadable.** grok writes `usage.json` when a turn ends, so a session directory without it is a session inside its first turn; its driver row is now available with zero usage so far instead of an unreadable-record row. (sty_81caa41b)

## [serve-v0.0.86] - 2026-09-28

### Fixed
- **The service embeds the grok first-turn fix above.** (sty_81caa41b)

## [0.0.557] - 2026-09-28

### Fixed
- **Driver usage is read from the right harness when agents are nested.** A grok session started from a Claude Code shell inherits `CLAUDECODE=1`, so the recorder named it claude and looked for a Claude transcript. The session's own published in-loop row, written by hooks that name their harness, now decides; the environment is only the fallback. (sty_81caa41b)
- **A move between two engaged states is recorded as a transition, not an engage.** (sty_81caa41b)

## [serve-v0.0.85] - 2026-09-28

### Fixed
- **The service embeds the driver-usage harness fix above.** (sty_81caa41b)

## [0.0.556] - 2026-09-28

### Added
- **The driving session's own usage is recorded against the story.** On every status transition, satelle reads the in-loop session's usage from its harness's own record — the Claude transcript, grok's `usage.json`, the codex rollout — and appends a `driver_usage` ledger row with the delta since that session's last snapshot: session id, executable, model, fresh input, cache read, cache write, output and cost (or an adapter-named unavailable reason). A session's usage is split between the stories it drives by a per-session high-water mark, and each harness turn is credited to at most one story. (sty_81caa41b)
- **Killed sessions, late flushes and resumes are accounted for.** Reaping or stealing a dead seat records the dead session's usage up to its last record (or an adapter-named "no partial-turn usage" row with its wall time); usage flushed after a story closed is written onto that story as a late row; a resumed session is attributed only its post-resume usage. (sty_81caa41b)
- **`satelle story cost` shows the driver line and a session reconciliation**, and `satelle story cost --session <id>` lists every story a session drove plus the unattributed remainder. Unavailable amounts stay unavailable, never zero. (sty_81caa41b)

## [serve-v0.0.84] - 2026-09-28

### Changed
- **The service embeds the driver-usage recording above; its seat list reaps dead seats with a kill row.** (sty_81caa41b)

## [0.0.555] - 2026-09-27

### Fixed
- **Grok ACP invocations record their dollar cost.** Grok's ACP usage reports `costUsdTicks` (1e-10 USD), which was dropped, so every grok ACP review row had no dollars. It is now converted to `cost_usd`. (sty_c4df7376)

### Changed
- **Every agent invocation row carries a dollar cost or says why not.** Each adapter records the provider's own figure — Claude and grok command/stream `total_cost_usd` or the model `costUSD` sum, grok ACP ticks — or `cost_unavailable_reason` naming the adapter (codex reports none). No price table is used. (sty_c4df7376)
- **`satelle story cost` and `--by-skill` total dollars.** Each row shows `$` or `unknown`, and totals sum only rows with a known cost and count the rest (`$0.1095 (1 rows unknown)`); a missing cost is never shown as `$0`. Rows recorded before this take their cost from the per-model figures when present. (sty_c4df7376)

## [serve-v0.0.83] - 2026-09-27

### Changed
- **The service embeds the cost-recording changes above.** (sty_c4df7376)

## [0.0.554] - 2026-09-27

### Fixed
- **Every hook names its own harness, so a grok driver is no longer recorded as Claude.** The grok hook scaffold now passes `--harness grok` on SessionStart, UserPromptSubmit and Stop, as PreToolUse already did, and Stop now publishes the session like the other hooks. Harness sniffing no longer treats `permission_mode` or snake_case keys as proof of Claude: grok sends Claude-compatible aliases alongside its own camelCase keys, so grok is recognised by those keys or a `.grok/` transcript, and an envelope with nothing to go on is unknown. Tests use real captured grok hook payloads. (sty_719c4a7b)
- **A dispatched agent never records itself as the driving session.** Every satelle-spawned process (one-shot, reviewer, rework relay, step summary) carries a spawn marker, and its hooks skip the in-loop publish, so a `claude -p` or ACP dispatch can no longer overwrite the driver's model file. (sty_719c4a7b)
- **`session_model` rows carry the session id, and an unknown model says why.** The row records `session_id`, and a model the harness did not report is `unknown` with an adapter-named reason. Rows written before this still parse. (sty_719c4a7b)

### Changed
- **`satelle doctor` flags a grok hooks file missing `--harness grok`.** Heal: run `satelle init` in the repo, which rewrites `.grok/hooks/satelle.json`. (sty_719c4a7b)

## [serve-v0.0.82] - 2026-09-27

### Changed
- **The service embeds the session-identity changes above.** Its behaviour is otherwise unchanged. (sty_719c4a7b)

## [0.0.553] - 2026-09-25

### Fixed
- **A fresh repo's doctor check no longer expects an orchestrator live-seat warning.** `story chat` no longer opens that seat, so the warning is gone. (sty_6f9ba7ca)

## [0.0.552] - 2026-09-25

### Removed
- **`satelle story chat` is gone.** The command, its `[orchestrator]` chat console and the inherited-orchestrator model tier are removed. `satelle story <unknown>` refuses an unknown subcommand. `satelle story rework` and the live sessions it opens are unchanged. An unset model still resolves by binding, step or `--model`, the in-loop session, `[model_order]`, then `cli-default`. A `session_model` row left under the old orchestrator role is ignored. Model inheritance comes from the in-loop tier alone, so the capability table lists it unavailable for grok and for codex acp, each with an adapter-named reason. (sty_6f9ba7ca)

## [serve-v0.0.81] - 2026-09-25

### Removed
- **The service no longer has a story-chat path.** It embeds the help, model-selection, and ledger changes above. (sty_6f9ba7ca)

## [0.0.551] - 2026-09-25

### Fixed
- **The init integration test matches the seed that no longer shows a live orchestrator.** `TestInitSeedsCommentedReworkBindingsAndDocs` was still requiring `# [orchestrator]` in a fresh agents.toml. (sty_462603f0)

## [0.0.550] - 2026-09-25

### Changed
- **Init and help no longer teach story chat as the way a story is driven.** The agents scaffold does not show a live `[orchestrator]`. A commented coder example, when present, is a one-shot `claude -p` command a repo may point `[coded]` at. `satelle help agent-dispatch` says the driving session is the in-loop executor, a one-shot coder runs only when the route names one, and `satelle story rework` is the only live relay and is off unless the step sets `rework`. The shipped `*` lane is unchanged: `in_progress` is still performed by `executor`, with no rework key. (sty_462603f0)

## [serve-v0.0.80] - 2026-09-25

### Changed
- **The service embeds the updated agent-dispatch help topic.** Its behaviour is otherwise unchanged. (sty_462603f0)

## [0.0.549] - 2026-09-25

### Added
- **A binding with no model follows its own CLI's model order.** agents.toml has a new `[model_order]` table with one list per executable: `claude`, `grok` and `codex`. When a binding leaves `model =` empty and no same-executable session model (orchestrator, in-loop or story creator) applies, the dispatch takes the first entry of its own executable's list. The ledger records the source as `order`. A binding's `model =` pin still wins. A name from one executable's list is never applied to another executable. An executable with no list falls through to `cli-default`, which is recorded as such. An entry can be a group of names that count as one rank, such as an alias and its resolved id; the first name is the one applied. `satelle init` writes the three lists (claude: opus, sonnet, haiku; grok: grok-4.7; codex: gpt-5-codex) into a fresh agents.toml, and appends them to an existing one that has no `[model_order]`. An operator's own table is never changed. The Grok and Codex ids come from captured CLI fixtures. `satelle help agent-dispatch` lists the same three orders. The table survives encode, redact, rehydrate and the repo/workspace/baseline layering: per executable, the highest layer that names it wins. (sty_4fde0a50)

## [serve-v0.0.79] - 2026-09-25

### Changed
- **The service reads the `[model_order]` table in agents.toml and embeds the updated agent-dispatch help topic.** Its behaviour is otherwise unchanged. (sty_4fde0a50)

## [0.0.548] - 2026-09-25

### Added
- **`satelle help agent-dispatch` has a per-adapter capability table.** One row per adapter (claude command, claude stream, grok command, grok acp, codex command, codex acp) says whether usage, cache split, resolved model, model inheritance and live session are available. Each unavailable cell gives an adapter-named reason. The table comes from `internal/agentcli/capabilities.go`. Tests check every cell against the code that produces it, check the help topic against the matrix, and fail when an unavailable is a silent zero rather than an explicit reason. The "What each harness reports" prose now matches the code: Codex hooks carry a model, and ACP live sessions record the model they opened with. (sty_52a8cb4a)

## [serve-v0.0.78] - 2026-09-25

### Changed
- **The service embeds the updated agent-dispatch help topic.** Its behaviour is unchanged. (sty_52a8cb4a)

## [0.0.547] - 2026-09-25

### Fixed
- **In-loop sessions now get the constitution and session principles at SessionStart.** Before, `satelle hook context` capped its output at a compiled 16384 bytes. Claude Code keeps only a ~2 KB preview of hook context over about 10 KB, so in-loop Claude sessions saw a preview and not the rules. The injection limit is now set per harness in `[harness.<name>] context_limit_bytes`. Embedded defaults: claude 9500, grok 16384, codex 16384, unknown 9500. A repo can override any of them in `satelle.toml`. When the constitution and principles do not fit, the ones left out are listed under an `OMITTED FOR THE <HARNESS> CONTEXT LIMIT` heading, each with the command to read it (`satelle doc get principles <name>`). Nothing is cut silently. Advisory lines and the seat block count toward the limit. `satelle hook context` takes `--harness`. Without it, the harness is detected from the hook event and then the session environment; anything unrecognised is `unknown`. The placement diet check compares against the largest configured limit. Grok ignores SessionStart hook output entirely, so an in-loop Grok session gets none of this injection. That is recorded on the story and left for a follow-up. (sty_ce1a2733)

## [serve-v0.0.77] - 2026-09-25

### Changed
- **The service picks up the per-harness `[harness.<name>]` config table.** The table comes with the config changes above; the service's behaviour is unchanged. (sty_ce1a2733)

## [0.0.546] - 2026-09-25

### Fixed
- **Grok now gets the no-user answer instead of a tool error.** When Grok asks the user (`_x.ai/ask_user_question`) in an ACP session, satelle now replies in the shape Grok expects: `{"outcome":"accepted","answers":{"<question>":"no user is available; decide from the payload and state your assumption"}}`. Before, Grok logged "Client returned an invalid response" and lost the instruction. The reply shape is chosen by request method in the ACP adapter. Any other ask or elicitation method still gets the generic reply. A test replays a captured Grok request. For capturing an agent's requests, `SATELLE_ACP_TRACE=<file>` appends each request the agent sends (method, id, redacted params) as a JSONL line. (sty_068fce04)

## [serve-v0.0.76] - 2026-09-25

### Fixed
- **The service's ACP sessions answer Grok's ask-the-user request in Grok's shape.** It shares the ACP adapter change above. (sty_068fce04)

## [0.0.545] - 2026-09-25

### Fixed
- **`satelle story chat` and the rework relay now write the denied-ask row.** 0.0.544 added the row, but the engine these two verbs open sessions with had no ledger sink wired, so on a real chat the row was dropped. It is now wired, and an end-to-end chat test covers it. (sty_ff50f788)

## [0.0.544] - 2026-09-25

### Fixed
- **Live sessions ledger a denied ask-the-user request.** A `satelle story chat` or rework-relay session (stream or ACP) whose agent asks the user now writes an `agent-interactive-denied` ledger row with the tool, question and response when it happens, as a one-shot dispatch already did. A stream-json agent's ask tool (for example `AskUserQuestion`) is now always denied with the no-user message, whatever the permission policy allows, and the `questions[].question` input shape is read as the question text. (sty_ff50f788)

## [serve-v0.0.75] - 2026-09-25

### Fixed
- **The service denies and reports ask-the-user requests the same way.** It shares the stream and ACP transport changes above. (sty_ff50f788)

## [0.0.543] - 2026-09-25

### Changed
- **A live binding with no command no longer becomes Claude.** A live seat that authors no `command=` and no `profile=`, such as the baseline `[orchestrator]`, used to resolve to the Claude stream-json command. It now resolves to `live use: no command`. `satelle agent validate` and `satelle doctor` warn about it, and opening the session refuses with a message that names both live options: `stream` (a stream-json CLI spawn line) and `acp` (an ACP agent spawn line). To heal, set `command=` or `profile=` on the seat in `.satelle/workflows/agents.toml`. satelle compiles no live default command. `DefaultClaudeStreamCommand` is still available as an opt-in Claude preset. (sty_a762f3bd)

## [serve-v0.0.74] - 2026-09-25

### Changed
- **The service uses the same live-binding resolution.** A live binding with no command is refused and never resolved to Claude. (sty_a762f3bd)

## [0.0.542] - 2026-09-25

### Fixed
- **Agent and harness detection no longer assumes Claude.** An in-loop Grok or Codex session now counts as an agent caller, so `compact_for_agents` applies to it. It is detected by the harness's own session marker or satelle's `SATELLE_SESSION`, not only `CLAUDECODE=1`. A hook envelope that carries Codex's `turn_id` is recorded as `codex`, and an unrecognised envelope is recorded as `unknown` instead of `claude`. Denies keep the strict shape for every non-Grok harness. Provider env-var names and envelope fingerprints now live in the `agentcli` adapter package. (sty_37fd5470)

## [serve-v0.0.73] - 2026-09-25

### Fixed
- **The service uses the same provider-neutral detection.** It shares the agent-caller and hook-harness detection above. (sty_37fd5470)

## [0.0.541] - 2026-09-25

### Fixed
- **ACP bindings receive the selected or inherited model.** An ACP binding with no `model=` and no `{model}` slot now gets the orchestrator, in-loop, or creator model when its executable matches, and applies it through ACP session configuration (`session/set_config_option`), not argv. An ACP live session records the model it started with at open, instead of `unknown`. (sty_bb92973f)

## [serve-v0.0.72] - 2026-09-25

### Fixed
- **The service uses the same ACP model selection.** An ACP binding can inherit a model without a `{model}` slot, and an ACP session reports its starting model. (sty_bb92973f)

## [0.0.540] - 2026-09-25

### Fixed
- **Grok and Codex runs record the model that actually ran.** A Grok ACP session reads the model id the peer reports on its prompt result (falling back to its modelUsage, then its session reply). Grok command and streaming output keep reading modelUsage, and a Codex run reads a model named on its exec events and keeps it across summed turns. When a provider reports no model, `model_resolved` names the adapter, for example `unavailable: grok acp reports no model` or `unavailable: codex command reports no model`, instead of the bare `unavailable`. Model labels never show such a reason as a model id, and a later reason never overwrites a measured id. (sty_8e422d47)

## [serve-v0.0.71] - 2026-09-25

### Fixed
- **The service shows the same resolved-model record.** It does not display an adapter-named no-model reason as a model id. (sty_8e422d47)

## [0.0.539] - 2026-09-25

### Changed
- **`[models]` ranking is no longer an agents.toml section.** A leftover table is ignored and is not a seat. Model selection no longer compares opus, sonnet, and haiku. When both the orchestrator and the in-loop session are eligible, the orchestrator wins. The init scaffold and `satelle help agent-dispatch` do not tell a repo to add a ranking. (sty_10a1303a)

## [serve-v0.0.70] - 2026-09-25

### Changed
- **The service uses the same model selection.** It does not read a `[models]` ranking. When both sessions are eligible, the orchestrator model wins. (sty_10a1303a)

## [0.0.538] - 2026-09-25

### Changed
- **A repo agents file overrides named seats and keeps the baseline seats.** The logical seats ship embedded. A repo `agents.toml` names only the seats it changes, and an unnamed seat stays on the baseline. A missing repo agents file no longer refuses: the baseline seats run. Command, tools and model come from a `profile=` the seat names, or from the catalog's `[roles]` when the repo sets `use_global_roles`. A catalog does not retarget a repo that did not ask. The starter machine catalog carries an agent profile beside the reviewer profile for each supported CLI. (sty_6602bb44)

## [serve-v0.0.69] - 2026-09-25

### Changed
- **The service resolves the same by-name seat overlay.** An absent or partial repo agents file runs the shipped seats, and a machine catalog reaches them only through an explicit `profile=` or `use_global_roles`. (sty_6602bb44)

## [0.0.537] - 2026-09-25

### Changed
- **A repo workflow file overrides by name.** A category table or a step key in the repo's done.toml or step.toml replaces that unit. Names the repo does not mention stay on the shipped route. A repo that declares any `[[gate]]` owns the gate list; a repo that declares none keeps the baseline gates. A repo with no workflow file still runs the shipped route. (sty_a4603ea2)

## [serve-v0.0.68] - 2026-09-25

### Changed
- **The service resolves the same by-name route overlay.** An authored category or step replaces that baseline unit and leaves the rest. (sty_a4603ea2)

## [0.0.536] - 2026-09-25

### Changed
- **A wrong premise at ready parks the story blocked.** Configuration is repaired on that same pass. The incorrect content is written on the timeline. Any other dispatch failure leaves the status unchanged. The start state can park, and the resume origin of that park is backlog. (sty_b8a0d062)

## [serve-v0.0.67] - 2026-09-25

### Changed
- **The service ships the same park edge.** A story still in backlog can be set to blocked, so a ready-step premise reject shows the blocked pill instead of staying in backlog. (sty_b8a0d062)

## [0.0.535] - 2026-09-25

### Changed
- **The ready step is performed by `ready-reviewer`, not the read-only reviewer.** The binding may run `satelle` (order tags, a child for an acceptance criterion the story already states, a vocabulary tag) and may not edit product code. A premise reject omits the required review artifact, so the dispatch fails and the story stays in backlog. The route pin expects that performer. (sty_e1d93a2c)

## [0.0.534] - 2026-09-24

### Fixed
- **Token usage and the cache split are recorded for Grok and Codex, not only Claude.** When a provider gives no usage, it is recorded as unavailable with a reason, never as zeros that look like measurements.
  - Each provider's usage mapping now lives in `internal/agentcli/usage_adapters.go`, and each mapper reads that provider's own field names:
    - Grok `-p --output-format json` reads the snake_case envelope `usage`, where the counts are separate. `streaming-json` reads the `usage` and `end` lines, and the last line wins.
    - Grok ACP reads `session/prompt` `result._meta.usage`. That is camelCase, and `inputTokens` already includes the cache read. Codex reads `turn.completed` usage.
  - Fixtures captured from grok 1.0.41 pin the real numbers. The Codex fixture is synthetic and labelled as such, because codex is not authenticated here.
  - A `usage` object with none of the provider's token fields is recorded as unavailable, never as available zeros.
  - The reason for missing usage (`usage_unavailable_reason`, which names the adapter) and the `cache_split_unavailable` flag are stored on gate, verdict, dispatch, summary, attempt and live-session rows, and `satelle story cost --json` returns them.
  - Dogfood: a Grok `[reviewer]` ACP gate recorded 572661 in / 5521 out, where it used to show `—/—`. (sty_c8d45201)

## [serve-v0.0.66] - 2026-09-24

### Fixed
- **The serve channel picks up per-provider usage mapping and the recorded reason when usage is unavailable.** (sty_c8d45201)

## [0.0.533] - 2026-09-24

### Fixed
- **A dispatch scratch directory is short enough for Chromium's Unix socket.** The dispatch id is 8 hex with no timestamp, and the leaf directory is created exclusively, with a retry on collision. Under `/tmp` the worst-case chromedp path (`chromedp-runner` plus 10 digits plus `SingletonSocket`) is at most 91 bytes for a story id and for an empty story id, 16 bytes under the Linux `sun_path` limit of 107. (sty_b666bc6e)
- **Route pins follow the ready stage.** Container lanes close backlog → ready → done, and the working lane's intent review gates ready → plan. (sty_b666bc6e)

## [0.0.532] - 2026-09-24

### Fixed
- **The process-tree CPU tests no longer leave busy-loop shells spinning after `go test`.** Their cleanup killed only the outer `sh`, so the forked `(while :; do :; done)` subshell (and a `sleep 30`) was orphaned to the user's systemd and ran forever. Every test run leaked one more, and 23 of them overloaded the owner's machine.
  - The tests now start their shell in its own process group and SIGKILL the whole group on cleanup. A new test proves no group member survives.
  - The busy-cap stall tests were checked: their scripts do the work in the script's own process, so the stall kill leaves nothing behind. The invariant is now documented on `busyScript`. (sty_2d2c4c03)

## [0.0.531] - 2026-09-24

### Fixed
- **A one-shot command binding that prints nothing until it exits is no longer killed as a stall while its process is working.** `claude -p --output-format json` and similar headless CLIs are silent while they think, so the idle watchdog cancelled any healthy run longer than `idle_timeout` (5m by default).
  - The command transport now samples the CPU time of the child's process tree (Linux `/proc`) and treats a rising total as a sign of life. A process with no CPU progress still stalls at `idle_timeout`.
  - The exemption is capped by the new `busy_timeout` (binding, then `[defaults]`, then a 60m shipped default; `"0"` or `"off"` restores strict behaviour), measured from the last real output. A stall past the cap says `(busy cap exceeded)`.
  - `busy_timeout` carries across machine-wide profiles like every other binding field.
  - Stream and ACP transports are unchanged. Platforms other than Linux report the probe as unavailable and stay strict.
  - Dogfood: a real planner dispatch with `idle_timeout = "5s"` was killed after 6 s on 0.0.530 and ran 18 s to completion on this build. (sty_db62a3b9)

## [serve-v0.0.65] - 2026-09-24

### Fixed
- **The serve channel picks up the command-transport CPU-liveness probe and `busy_timeout`.** (sty_db62a3b9)

## [0.0.530] - 2026-09-24

### Fixed
- **A repo binding that names a machine-wide profile keeps its own `idle_timeout`.** Profile resolution (`config.overlayBinding`) merged every execution field except `idle_timeout`, so a per-binding idle bound on a profile-backed binding was silently dropped.
  - It now carries across profile, global-role and workspace layering, and `satelle agent validate` shows its source.
  - A completeness test reflects over every TOML-tagged `AgentBinding` field and fails if one is not merged, so a future key cannot be dropped the same way. (sty_407158e4)

## [serve-v0.0.64] - 2026-09-24

### Fixed
- **The serve channel picks up the idle_timeout profile-merge fix.** (sty_407158e4)

## [0.0.529] - 2026-09-24

### Fixed
- **An ACP agent's interactive "ask the user" tool can no longer hang a satelle dispatch.** No human is attached to a satelle ACP session, so the ACP client now handles these itself:
  - a permission request for an Ask-shaped tool (`Ask`, `ask_user_question`, `AskUserQuestion`) is denied;
  - an elicitation or ask request is auto-answered with "no user is available; decide from the payload and state your assumption";
  - **any** other unanswered request from the agent gets JSON-RPC "method not found" rather than silence.

  Each denial emits an `interactive_denied` event, and one-shot dispatches record an `agent-interactive-denied` ledger row with the tool and question. This was observed on the Grok `[reviewer]`: two create-reviews stalled for 5m01s on `ask_user_question`, and a provoked Ask now ends in about 10 ms. Grok's ACP agent cannot exclude the tool at session setup: `--tools` and `--disallowed-tools` are headless-only. (sty_32795645)

## [serve-v0.0.63] - 2026-09-24

### Fixed
- **The serve channel picks up the ACP interactive-ask handling.** (sty_32795645)

## [0.0.528] - 2026-09-24

### Fixed
- **The reviewer-model dogfood tests resolve through the machine-wide profile catalog.**
  - `TestRepoReviewerModelIsActive` checks the reviewer's EFFECTIVE model through `ResolveAgents` against the operator's `~/.satelle/agents.toml`, not just the repo layer. A missing profile is named rather than reported as an empty model.
  - `TestReviewerModelActorsBoots` installs a read-only copy of that catalog into its isolated `SATELLE_HOME`, so a repo whose bindings use `profile = "…"` still boots under `make integration`. (sty_4fdd9815)

## [0.0.527] - 2026-09-24

### Added
- **A JSON array crusher, the lossy last resort for oversized list output.** When `[output.crush]` in satelle.toml enables it, a `compact_commands` list whose lossless table form is still over `size_threshold_bytes` is sampled down to a budget of `max_kept` rows.
  - **Selection.** It keeps the first and last fractions of the budget and fills the rest by stride sampling, with duplicate rows removed.
  - **Always kept, outside the budget:** rows whose string values contain a configured error keyword, rows beyond `variance_sigma` on length or any numeric field, rows carrying a rare key (`structural_outlier_fraction`), and rows with a rare `status_fields` value (`rare_status_fraction`).
  - **Output.** Kept rows are emitted unchanged, in original order. One trailing `{"_crushed": "<<ccr:HASH,rows,SIZE>>", "dropped": N, "total": T}` element follows, and `satelle retrieve HASH` returns the full original array.
  - **Configuration.** The binary ships no keywords or thresholds. An absent or disabled table does nothing, `--json` and `--full` always bypass the crusher, and nonsensical values are refused at load. K is a configured value; adaptive K is not shipped.
  - **Example.** A real 200-row `ledger list` goes from 99 KB (lossless) to 70 KB, with every `review_reject` row kept. (sty_aa34491d)

## [serve-v0.0.62] - 2026-09-24

### Changed
- **The serve channel picks up the `[output.crush]` config.** (sty_aa34491d)

## [0.0.526] - 2026-09-24

### Added
- **Binding env values in `agents.toml` can reference `${SATELLE_SCRATCH}`.** It is substituted per dispatch with that agent's own scratch directory, for one-shot dispatches, live sessions (orchestrator and relay) and the summariser alike.
  - `[vars]` references still expand as before, and an unknown `${NAME}` still fails fast.
  - **Example:** point Go installs into scratch so a dispatched agent's `go install` cannot shadow your installed tools: `env = { GOBIN = "${SATELLE_SCRATCH}/bin" }`.
  - **Requires satelle ≥ 0.0.526.** An older binary rejects the whole `agents.toml` as an unknown `[vars]` reference, so upgrade before adding it. (sty_87ffce38)

## [serve-v0.0.61] - 2026-09-24

### Changed
- **The serve channel picks up the reserved `${SATELLE_SCRATCH}` handling in `[vars]` expansion.** (sty_87ffce38)

## [0.0.525] - 2026-09-24

### Added
- **`satelle story tidy <id> <path>...` clears stray files a story created without deleting them.** A dispatched coder can create files but not delete them, so this breaks the scope-gate deadlock over untracked debris.
  - **Reversible.** Each path moves into a story-level tidy area under the satelle scratch directory, with one `tidy` ledger row per file. `satelle story untidy <id> [<path>...|--all]` moves it back and never overwrites.
  - **Safe by construction.** A path is refused, and nothing moves, when it is tracked, exists in HEAD or at the engagement baseline, predates the engagement, or lies outside the worktree.
  - **Usable at any performing step**, from the driver or a dispatched session. The edit gate does not block it.
  - **Scope-review hint.** A scope-review rejection for untracked debris ends with a ready-to-run `Fix: satelle story tidy …` line, so the relay coder can clear it without help. (sty_d74e9b1b)

## [serve-v0.0.60] - 2026-09-24

### Changed
- **The serve channel picks up the `tidy` / `tidy_restore` ledger kinds and the shared scratch-path helper.** (sty_d74e9b1b)

## [0.0.524] - 2026-09-24

### Changed
- **A failing functional check's reject notes keep what matters from anywhere in the log.** They no longer keep only the last 40 lines.
  - **With `[output.check_log]` configured**, the notes keep every line matching the configured keep patterns, with context. Configured stack traces are collapsed to their first frames plus app frames, and repeated warnings are deduplicated.
  - **With nothing configured**, the notes are the head and tail of the log.
  - **Either way**, the notes end with an omission footer and a `<<ccr:…,log,N>>` marker that `satelle retrieve` resolves to the full raw output.
  - **Configuration.** The binary ships no language patterns. This repo configures its Go values (`--- FAIL`, `panic:` with goroutine traces, `file.go:N:M:`).
  - **Verdict unchanged.** Accept/reject still depends only on the check's exit code.
  - **Build-unit check.** `satelle-build-unit-check` no longer tails build or test output, so the compressor sees the full log. (sty_ef930f81)

## [serve-v0.0.59] - 2026-09-24

### Changed
- **The serve channel picks up the `[output.check_log]` config.** (sty_ef930f81)

## [0.0.523] - 2026-09-23

### Changed
- **The gate payload's diff is now ranked rather than cut blindly at 64KiB.** When `[output.diff_rank]` in satelle.toml enables it (`passthrough_lines`, `max_files`, `max_hunks_per_file`, `context_lines`, `priority_patterns`), the patch gets noise stripping followed by ranked selection:
  - the files with the most changes are kept;
  - within each file, the first hunk, the last hunk, and the highest-scored hunks (by change count plus configured priority patterns) are kept;
  - unchanged context is trimmed, with hunk headers rewritten.

  Every dropped file, hunk or trimmed context carries a marker that `satelle retrieve` resolves byte-exactly. The file list and stat caps are unchanged. The 64KiB ceiling remains as a last-resort backstop, and its overflow is now offloaded behind a marker instead of lost. The binary ships ranking off, with no thresholds or patterns. (sty_918e2086)

### Added
- **`satelle story diff --full`** prints the raw patch, skipping noise stripping and ranking. (sty_918e2086)

## [serve-v0.0.58] - 2026-09-23

### Changed
- **The serve channel picks up the `[output.diff_rank]` config.** (sty_918e2086)

## [0.0.522] - 2026-09-23

### Added
- **Compact, lossless CLI output for what agents pull.** When `[output]` in satelle.toml enables it (`compact_commands`, `compact_for_agents`, `long_cell_bytes`, `repeat_min`, `noise_patterns`), `ledger list`, `story list`, `story docs`, `story messages` and `story diff` render compactly.
  - **Lists** become a `[N]{col:type,…}` header plus CSV rows that decode back to exactly the original records. Long cells are offloaded to the retrieval store behind `<<ccr:HASH,str,SIZE>>`.
  - **`story diff --patch`** drops `index` lines and offloads whitespace-only hunks, and files matching `noise_patterns`, behind markers that `satelle retrieve` resolves byte-exactly.
  - **Round-trip guard.** A fold applies only when it round-trips and is smaller.
  - **Opt-in.** The binary ships compact off and no filename patterns. Compact applies for agent callers (dispatched, or an in-loop Claude session) when enabled, and `--json` always forces plain JSON.
  - **Help.** `satelle help compact-output` documents the format. (sty_75b76691)

### Fixed
- **`story diff --patch` always emits `a/` / `b/` path prefixes.** Git config such as `diff.mnemonicPrefix` no longer changes the headers. (sty_75b76691)

## [serve-v0.0.57] - 2026-09-23

### Changed
- **Serve channel picks up the `[output]` config and the pinned diff prefixes.** (sty_75b76691)

## [0.0.521] - 2026-09-23

### Added
- **Retrieval store and `satelle retrieve <hash>`.** Content that a later compressor condenses can be stored by content hash (sha256, first 24 hex characters) in the repo's SQLite store. It is deduplicated across stories and linked to each story that stored it.
  - `satelle retrieve <hash>` prints the exact original bytes, and exits non-zero on an unknown or malformed hash.
  - The marker grammar (`<<ccr:HASH>>` and `[N lines compressed to M. Retrieve more: satelle retrieve HASH]`) has one exported detection regex.
  - Retention is `retrieve_keep_days` in satelle.toml. The default 0 keeps everything. When it is set, an original is pruned only once every story referencing it is terminal and older than the setting.
  - `satelle help retrieve` documents which reviewer seats can reach the verb. Configured command-transport reviewers with `Bash(satelle:*)` can. The embedded default reviewer template, which denies Bash, and read-only live sessions cannot. (sty_b0577532)

## [serve-v0.0.56] - 2026-09-23

### Changed
- **The serve channel picks up the retrieval store migration and the retention setting.** (sty_b0577532)

## [0.0.520] - 2026-09-23

### Added
- **The ledger now separates fresh, cache-write and cache-read input tokens.** They are recorded on every gate verdict, dispatch, summariser run, artifact attempt and live session, as new `tokens_in_fresh`, `tokens_cache_write` and `tokens_cache_read` fields, and in each per-model `model_usage` entry. `tokens_in` stays their sum, so route, cost and web views are unchanged; older rows read as unsplit. (sty_363eaf55)
- **Gate and dispatch rows record the size of what satelle sent.** They carry `system_prompt_bytes` and `payload_bytes`, lengths only, never content. (sty_363eaf55)
- **`satelle story cost --by-skill`** rolls up input tokens by gate skill (fresh, cache-write, cache-read and unsplit) across one story or all stories (`--all`). It pages through the whole ledger rather than stopping at a row cap. (sty_363eaf55)

### Fixed
- **A live relay or chat session now records the sum of all its turns, not only the last.** The close row carries the summed tokens (including the cache split) and a `turns` count. (sty_363eaf55)

## [serve-v0.0.55] - 2026-09-23

### Changed
- **The serve channel picks up the cache-split ledger fields and the paged ledger scan.** (sty_363eaf55)

## [0.0.519] - 2026-09-23

### Changed
- **`satelle-agent-goals` now states that an epic closes on its children.** An epic or parent container is complete only when every child story is terminal. The agent keeps driving the children, one at a time, until the container can close. Intermediate stages are waypoints, never a point to hand back control. This is part of the embedded principle every session receives. A repo whose deployed copy is stamped from an older binary shows it as drifted until `satelle init` / `satelle rebase` runs there. (sty_930d404a)
- **This repo's AGENTS.md is a short pointer.** It no longer restates rules the session-injected principles and the release skill already carry. (sty_930d404a)

## [serve-v0.0.54] - 2026-09-23

### Changed
- **Serve channel picks up the updated embedded `satelle-agent-goals` principle.** (sty_930d404a)

## [0.0.518] - 2026-09-23

### Added
- **Every dispatched agent gets its own scratch directory, and the leftovers it strands in the repo are cleared before a gate runs.**
  - **Scratch directory.** Every one-shot dispatch (reviewer, executor, planner, summariser) and every live session (`story chat`, rework coder and consult) runs with `TMPDIR` and `SATELLE_SCRATCH` set to a per-dispatch directory under the system temp root (mode 0700). The instructions satelle generates for every agent name that directory, so no skill or principle has to say it. The directory is removed on success and kept, with its path ledgered as `scratch_kept`, on failure.
  - **Attaching without a file.** Agents attach text with `satelle story attach --body "…"`, or with `--file $SATELLE_SCRATCH/…`, so no file is needed in the repo.
  - **Leftovers.** When a coder or driving session ends, files it created in the tree that match `[dispatch.leftovers]` in satelle.toml (`patterns`, optional `content_regex` within `max_bytes`, `action = move|flag`) are moved into scratch or flagged, and ledgered, before the next gate. A failed sweep never deletes what it already moved. The binary ships no patterns. (sty_e7aaf8b1)

## [serve-v0.0.53] - 2026-09-23

### Changed
- **Serve channel picks up the `[dispatch.leftovers]` config and the scratch help text.** (sty_e7aaf8b1)

## [0.0.517] - 2026-09-23

### Added
- **A binding that leaves `model` unset gets a deliberately chosen model, and every dispatch records why.** Precedence, first match wins:
  1. the binding's `model`;
  2. a per-dispatch choice, where `--model` on `story rework` / `story chat` / `story retrospect` beats a step's `model =` in step.toml;
  3. the higher-ranked of the orchestrator's and in-loop session's models;
  4. the model of the session that created the story;
  5. the CLI default.

  "Higher-ranked" is configuration: `[models] ranking = [...]` in agents.toml, strongest first; an unranked model loses to a ranked one. An inherited model is only passed to a command with the same executable, so a Claude model never reaches a Codex or Grok binding. Session models are captured from the harness (hook payload, transcript, stream init) and recorded as `session_model` ledger events, or as `unknown`. Every dispatch, gate verdict, summariser run, attempt/escalation and live session open/close records `model_source` next to `model` and `model_resolved`. `satelle agent validate` reports a step-level model as the effective model. The init scaffold ships a commented `[models]` example. (sty_7069bced)

## [serve-v0.0.52] - 2026-09-23

### Changed
- **Serve channel picks up the model-selection config, the session-model ledger kind and the `model_source` telemetry field.** (sty_7069bced)

## [0.0.516] - 2026-09-23

### Changed
- **A dispatched agent now runs for as long as it makes progress.** The wall-clock agent timeout is gone as a default. A dispatch is stopped only when it **stalls**: no real event (tool start/end, message, usage) for `idle_timeout`. Heartbeats do not count. `idle_timeout` is set per binding or in `[defaults]` in agents.toml, and ships as 5m. `timeout` remains as an optional hard ceiling, unset by default. This applies to command, stream and ACP transports, one-shot dispatches, the retrospective, and live `story rework` / `story chat` sessions. A stall is recorded as outcome `stalled` with `agent-stalled` telemetry (idle duration and last event), and the refusal says the agent stalled rather than "context deadline exceeded". (sty_752c4ef2)

### Added
- **In-flight dispatches are visible.** The seat's activity record now carries the agent, model, pid, last event, last-event time and event count. It refreshes on real events and is pushed to the web mirror while the dispatch runs, not only on status changes. The story row shows e.g. `coder (sonnet) running · 18m · last activity 10s ago: tool: Bash`, and turns to a warning once idle passes half of `idle_timeout`. A pip for an earlier refused attempt is drawn differently from the step currently running. `satelle story seat` prints the same fields. (sty_752c4ef2)

## [serve-v0.0.51] - 2026-09-23

### Changed
- **Serve channel picks up the dispatch running indicator, the warning token, and the extended seat activity.** `internal/web`, `internal/lease`, `internal/config` and `internal/help` are on the satelled watch set. (sty_752c4ef2)

## [0.0.515] - 2026-09-23

### Added
- **An unset `interface` now resolves by how the binding is used.** A binding with no `interface` that is opened as a live session (the rework relay's coder seat, a `rework.consult` binding, or `satelle story chat`) resolves to the first transport in `[defaults] live_interfaces` (shipped `["stream", "acp"]`) that its **authored** command can actually open. A stream-json Claude line gets `stream`, and an ACP spawn line gets `acp`. A binding with no command gets the default stream command line. An authored one-shot command (e.g. `claude -p --output-format json … {system}`) is not treated as live: it resolves to `command` with the reason `live use: not live-capable`, so the existing warning and refusal still fire. One-shot dispatches (gate reviewers, planners, edge advisors) always resolve to `command`. An explicit `interface` always wins. Live opens of `[reviewer]` and `[executor]` now go through their per-role resolvers, so their default tool grants apply. `satelle agent validate` prints each binding's resolved interface and the reason (`explicit`, `live use`, `one-shot default`, `live use: in-loop`, `live use: not live-capable`). (sty_119f6fda)

## [serve-v0.0.50] - 2026-09-23

### Changed
- **Serve channel picks up the use-aware interface resolution and its help text.** `internal/config` and `internal/help` are on the satelled watch set. (sty_119f6fda)

## [0.0.514] - 2026-09-23

### Changed
- **The default bound on one nested agent invocation is now 20 minutes, up from 10.** A binding with no `timeout` now gets 20m. The coded step dispatches a whole implementation to a coder under this bound, and 10m cut a real implementation off partway through. An explicit per-binding `timeout` still wins. The gate check timeout (20m) is unchanged. (sty_a7089cb6)

## [0.0.513] - 2026-09-23

### Added
- **The ledger records the model that actually ran, not only the configured alias.** Every dispatch, verdict row (`review_accept` / `review_reject`), summariser run and attempt/escalation now stores `model_resolved` (the canonical id, e.g. `claude-opus-5-5`) and `model_usage` (every model the CLI reported, with tokens and cost) beside `model`, which stays the configured alias (e.g. `opus`). When a result reports several models, the primary is the one with the most output tokens, independent of map order. A transport that reports no model (ACP, the Grok envelope, or a result without `modelUsage`) records `"unavailable"`, never an empty value. (sty_87b86044)

### Changed
- **`story route`, `story cost` and the web timeline chip show the resolved model.** They render the canonical id when one was recorded and `<alias> (unknown)` when not. Rows written before this release load unchanged and show as unknown; nothing is back-filled. The web chip no longer falls back to the agent name when a row has no model. (sty_87b86044)

### Fixed
- **Stream-transport dispatches now record their usage.** A `stream` binding's result usage (tokens and model) arrived only on an event that nothing consumed, so it was recorded as unavailable. The stream runner now reports it to the engine directly. (sty_87b86044)

## [serve-v0.0.49] - 2026-09-23

### Changed
- **Serve channel picks up the resolved-model ledger fields and the model chip rendering.** `internal/agentcli`, `internal/ledger` and `internal/web` are on the satelled watch set. (sty_87b86044)

## [0.0.512] - 2026-09-22

### Fixed
- **The PreToolUse hook no longer denies the rework relay's coder.** `satelle story rework` exports the lease's stamped session id to its children, so the coder's hook and the relay's own permission policy resolve the same live seat. Before, a differing `SATELLE_SESSION` made `pickSessionSeat` drop it. The verb marks only the coder spawn with `SATELLE_RELAY_BINDING` / `SATELLE_RELAY_ITEM`. `hook gate` and `hook commitgate` then apply the relay's allocated-binding rule (`dispatchedPerformerPermitted`) at a committed status, bound to that exact story. The consultant and every unmarked session, including the driving orchestrator, keep today's `editPermitted` branches and deny text. A refused relay coder is now told why: the seat is not bound (naming both session ids), the seat is stale or missing, a transition is in flight, or the binding is not allocated. It no longer sees "without a performing story" or "allocated to agent=executor". (sty_7567f047)

## [serve-v0.0.48] - 2026-09-22

### Changed
- **Serve channel picks up the relay marker env keys and help text.** `internal/config` and `internal/help` are on the satelled watch set. (sty_7567f047)

## [0.0.511] - 2026-09-19

### Removed
- **Optional TypeSafe/Jev reviewer prototype is gone from the product surface.** `interface=typesafe` is an unknown interface. The HTTP runner, `satelle-plan-typesafe-review` skill, commented `[reviewer-typesafe]` init scaffold, and `jev:prototype` gate are deleted. Default `[reviewer]` stays an LLM. The prototype did not pass as a working-lane gate (sty_5f69cd89; sty_38915987 dogfood). Historical 0.0.505–0.0.508 entries stay as the record that it was tried. (sty_e3eca0b7)

## [serve-v0.0.47] - 2026-09-19

### Changed
- **Serve channel drops the typesafe transport and the retired Jev skill.** `internal/agentcli` and `internal/config` are on the satelled watch set. (sty_e3eca0b7)

## [0.0.510] - 2026-09-18

### Fixed
- **A now-stamped stale snapshot can no longer revert story status after a transition is ledgered.** Follow-on to sty_2c71eff6. `Upsert` of an existing row keeps stored status (`UpsertForce` still overwrites). Field writes that carry a snapshot status stay body-only. `story estimate` / `actual` / restamp compare-and-set on status and never set it. (sty_38915987)

## [serve-v0.0.46] - 2026-09-18

### Changed
- **Serve channel picks up the workitem Upsert status-preserve guard.** `internal/workitem` is on the satelled watch set. (sty_38915987)

## [0.0.509] - 2026-09-18

### Fixed
- **Mirror ingest no longer collides on duplicate doc names; workstate snapshot/pull push the local UI.** Docs are keyed by path (fallback `<kind>/<name>`). A genuine duplicate returns `CollisionError` naming kind and id instead of a SQLite UNIQUE 500. `satelle sync workstate snapshot` and `pull` POST a drain snapshot after materialising so pulled stories appear on `/r/<slug>/` without a fake mutation. (sty_e4e1a008)

## [serve-v0.0.45] - 2026-09-18

### Changed
- **Serve channel picks up path-keyed mirror ingest.** `internal/mirror` is on the satelled watch set. (sty_e4e1a008)

## [0.0.508] - 2026-09-18

### Fixed
- Same slice as 0.0.507: `check-serve-version` refuses a stale local serve-v* baseline. Re-cut the CLI tag so HEAD has a successful release run after the serve-v0.0.44 bump. (sty_da6c3874)

## [0.0.507] - 2026-09-18

### Fixed
- **`make check-serve-version` refuses a stale local serve-v* baseline.** The gate compares the local newest `serve-v*` tag to origin via `git ls-remote` (never fetches). If origin is ahead, it names both tags and tells you to `git fetch --tags origin`. If origin cannot be queried, it refuses rather than answering ok from the local list. `--paths` / `--check-path` stay offline. (sty_da6c3874)

## [serve-v0.0.44] - 2026-09-18

### Changed
- **Serve channel catches typesafe reviewer substrate on the watch set.** CLI 0.0.505–0.0.507 shipped `interface=typesafe` packages that `cmd/satelled` compiles; bump so `satelle update` refreshes the running service. (sty_da6c3874)

## [0.0.506] - 2026-09-18

### Fixed
- **`interface=typesafe` dispatch no longer LookPaths the System One HTTPS URL.** `lookupRunner` and `satelle agent validate` skip PATH resolution when the binding interface is typesafe (HTTP transport; nothing is spawned). command/acp/stream still LookPath. Default `[reviewer]` unchanged. (sty_6b6a2f98)

## [0.0.505] - 2026-09-18

### Added
- **Optional `interface = "typesafe"` reviewer transport (prototype).** A `role=reviewer` binding may set `interface = "typesafe"` with a single `https://` System One endpoint, pinned model (e.g. `jev-1.13.0`), and `TYPESAFE_API_KEY` via binding env. The runner POSTs the satelle-built payload plus skill-authored ```typesafe questions, maps Choice accept/reject onto the existing `{"decision","notes"}` bytes, and reuses `parseDecision` / enact unchanged. Not live-capable (`story chat` / rework stay off this path). The default `[reviewer]` is unchanged — this is an opt-in surface behind a tag-scoped `[[gate]]` (`jev:prototype` + `satelle-plan-typesafe-review`), not a default reviewer replacement. Repo substrate ships the binding/gate **commented** so an older installed binary can still load `agents.toml` until `satelle update` after release. (sty_5f69cd89)

## [0.0.504] - 2026-09-16

### Changed
- **`satelle help workflows` teaches the TOML route source.** Operator help (and README, reviewer-checks, create-review, agent-dispatch) now describe `done.toml` / `step.toml` with category tables, obligation-keyed steps and `[[gate]]` entries. The conversion guide keeps its md-to-toml mapping. A help-test fence fails if the live-form topics regress to `done.md`/`step.md`. (sty_a69d7090)

## [serve-v0.0.43] - 2026-09-16

### Changed
- **Serve help topics match the TOML route.** `internal/help` is on the satelled watch set; bump so `satelle update` refreshes the running service's help pages. (sty_a69d7090)

## [0.0.503] - 2026-09-16

### Added
- **Story timeline: attached documents are named, clickable line items.** `story_doc_attached` carries `{name,type}` (legacy body-parse still names pre-existing rows). The timeline link opens `/r/{slug}/story/{id}#doc-<name>` with that document expanded. A `←` on the standalone story page returns to the project stories tab with that row expanded (`?expand=<id>`). (sty_49666ca9)

## [serve-v0.0.42] - 2026-09-16

### Added
- **Timeline document links and back-to-expanded-row.** Serve-path (`internal/web` detail VM, templates, `app.js`/`app.css`); `satelle update` refreshes satelled so operators can review agent-attached docs from the timeline. (sty_49666ca9)

## [0.0.502] - 2026-09-16

### Added
- **Workspace landing: the whole project row opens the project page.** The name stays a real `<a href="/r/{slug}/">`; a CSS `::after` stretch (scoped to `tr.row[data-slug]`) extends the hit target across Path, counts, and Updated so the pointer cursor matches the click. Project-page story/task rows are untouched — they still expand inline via `data-expand-url`. (sty_be23ae50)

## [serve-v0.0.41] - 2026-09-16

### Added
- **Landing rows stretch the project-name link across the row.** Serve-path (`internal/web` template + `app.css`); `satelle update` refreshes the running satelled so the workspace landing matches the pointer affordance. (sty_be23ae50)

## [0.0.501] - 2026-09-10

### Added
- **A fresh repo can find converge-then-gate in what `satelle init` seeds.** The seeded `.satelle/workflows/agents.toml` now ends with commented `[orchestrator]` and `[coder]` example tables: live-capable (`interface = "stream"`; `acp` works the same way), a read-oriented orchestrator grant beside a coder grant that includes `Edit,Write`, and a note that these bindings are consumed by verbs (`satelle story chat`, `satelle story rework`), not allocated by a workflow node. Every line is a comment, so a fresh repo's binding set and behaviour are unchanged. (sty_cec967b5)
- **The seeded workflows README and `step.toml` document `rework = { consult, rounds }`.** One paragraph each: the key opens a bounded coder/consultant relay through `satelle story rework`; absent means off; the gate reviewer still runs cold and one-shot, and the consultant's `READY` is a signal, never a verdict. The shipped `step.toml` stays comment-only and declares no `rework` key. (sty_cec967b5)
- **`satelle help agent-dispatch` has a "Converge then gate" section.** It sets the expensive loop (present, reject, re-present) against the cheap one (warm convergence, then one cold gate), points at the `READY` contract, and states the blocked fallback: after the round budget without `READY`, or a declared number of rejections on the same edge, the orchestrator parks to `blocked` quoting the last objection. That rule lives in the orchestrator skill; the binary does not compile it. (sty_cec967b5)

### Changed
- **`satelle agent validate` and `satelle doctor` report the rework round budget.** Both existing rework-consult WARNs, for a missing binding and for one that cannot hold a live session, now include `rounds=N` in their detail. They are still WARNs and never block, and no new finding ID was added. (sty_cec967b5)

### Fixed
- **`satelle help agent-dispatch` no longer says this repo's route declares no `rework`.** That was true until order:4 (sty_ae16cd44) opted this repo's `coded` step in. The sentence now gives the rule instead: an in-loop performer cannot be relayed, so a step that opts in must allocate a live-capable one. (sty_cec967b5)

## [0.0.500] - 2026-09-10

### Fixed
- **A story id cited in another commit's body no longer hands that commit's files to the cited story.** `satelle-substrate-only-check` and `satelle-docs-only-check` built their commit channel from `git log --grep="$sid"`, which matches the WHOLE message. So d7bf17d — subject `… (sty_0496c77c)`, body citing sty_ae16cd44 as the reason that story exists — was attributed to sty_ae16cd44, and its close was rejected for `.version`, both CHANGELOGs and a `_test.go` file that were never its slice. The repo's ownership signal is the trailing `(sty_…)` in the **subject**, so each candidate is now tested with `git show -s --format=%s` and only subject matches accumulate; `--grep` is retained as a cheap candidate narrowing and the fence comment says so, because dropping the subject test reopens the defect. Deliberately **not** a channel-1 short-circuit: channel 3 still runs unconditionally and unions with the recorded and live channels, so a later subject-owned commit that adds product files still rejects. Both ````check` fences carry the fix and both gained a golden case reproducing the d7bf17d shape — a story's in-lane commit plus a later commit whose subject names a different story and whose body cites this one while touching `cmd/foo.go` — which exits 1 against the pre-fix fence. (sty_8221c090)

## [serve-v0.0.40] - 2026-09-10

### Fixed
- **Serve path carries the corrected commit-attribution rule.** The two check skills under `internal/config/substrate/skills/` are on the satelled watch set; bump so `satelle update` refreshes the running service's embedded substrate. (sty_8221c090)

## [0.0.499] - 2026-09-10

### Fixed
- **`TestProjectWorkflowReviewerFirst` no longer forbids this repo's own converge-then-gate opt-in.** The reviewer-first integration test pinned `in_progress` to in-loop `agent=executor @skill:code`, a wall sty_db003275 built when it reverted a brief, *unbounded* `agent=coder` experiment. epic:converge-then-gate order:4 (sty_ae16cd44) then allocated the `coded` step to the dispatched `[coder]` binding **with** a bounded rework relay, so the live route emits `agent="coder" skill="coder"` and the test failed the very substrate it governs. `in_progress` now accepts either sanctioned allocation — in-loop `executor@code` for repos that have not opted in, dispatched `coder@coder` for those that have — and nothing else; `integration` and `release` stay pinned in-loop, and the doc comment records both decisions so a later reader does not restore the wall. The relay's round budget is deliberately **not** asserted: `rework` is an orchestrator instruction, not topology, it is absent from the emitted Spec by design, and `satelle story route` already prints it — compiling it into Go would be another process-in-Go pin. (sty_0496c77c)

## [0.0.498] - 2026-09-10

### Added
- **`satelle story rework <id>` — a bounded conversation between a coder session and a consulting reviewer session.** The reject → re-present cycle is the expensive loop; the cheaper shape is warm convergence then a cold gate. The relay opens the current step's performer binding and its declared consult binding as two live sessions on the story, seeds both with the story payload (acceptance criteria, plan, messages), and relays turns — consultant objection → coder, coder's reply → consultant re-check — until the consultant says ready or the round budget is spent. It prints and ledgers `{converged, rounds, last_objection}` and **never sets status**: a consultant's "ready" is a signal to the orchestrator, never a verdict, and the reviewer that gates the edge still runs cold and one-shot afterwards. (sty_8e0b29a0)
- **`rework = { consult = "<binding>", rounds = N }` on a performing step.** The loop is configuration, read and never decided: the route parser accepts the key, `satelle story route` and `satelle workflow show` print it, and the web route panel shows it. A step without the key has no loop and behaves byte-identically to before. Three mis-authorings are refused by name rather than degrading to a silent no-op — no `consult`, `rounds <= 0`, or a step that allocates no performer to code with. `satelle agent validate` **warns** (never refuses) when `consult` names a missing binding, or one that exists but cannot be opened as a live session, using the same predicates the relay itself uses. (sty_8e0b29a0)
- **The termination contract is a rule, not a decision.** The consultant's reply must end with a final line that is exactly `READY` or `NOT READY: <reason>`. Anything else counts as not ready with the whole reply as the objection and **consumes a round**, so a consultant that cannot follow the contract cannot hang the loop. `--rounds N` may only *lower* the authored budget. `satelle help agent-dispatch` documents the relay, the contract, the marker and the two permission policies. (sty_8e0b29a0)
- **`cc` on an agent message — a directed turn can also be readable by all.** `to` stays who a message is *for*; the new optional `cc` is an additional address it is readable *under*. The relay ledgers every turn with its real `coder`/`consult` direction **and** `cc = "*"`, so `satelle story messages <id>` reads as the conversation it was while the transcript still reaches whoever later judges the edge through the payload's `messages[]`, within the existing message budget. A message without `cc` is filtered exactly as before. (sty_8e0b29a0)

### Changed
- **Permission policy is per side of the relay, and neither side is the other.** The coder may mutate the tree only when its **own** binding's tool grant admits mutators **and** the seat is live, not mid-transition, and its committed status is a performing state the route allocates to *that binding* — a new predicate beside the PreToolUse one, because the existing driving-session branch only ever admits `agent = "executor"` and a relay coder is always a named live binding. The consultant's mutator asks are **denied by policy** whatever the seat says: a consultant that can edit is a reviewer marking its own work. (sty_8e0b29a0)
- **A live session's charter role is stated, not inferred from its binding name.** `Engine.OpenSessionAs` takes the role explicitly, so a repo may name its coder binding anything — including `orchestrator` — without being handed the consultant's charter. `satelle story chat` is unchanged: it delegates with today's name-based selection. (sty_8e0b29a0)
- **The relay reads a turn's verbatim result, not the render channel.** Both transports pass assistant text through `SafeText`, which flattens whitespace and caps at 240 runes — right for a terminal, fatal for a marker on a reply's final line. The relay reads the transport's per-turn `Captured()` result and falls back to the rendered text only when a transport captures nothing. (sty_8e0b29a0)
- **`wfgovern.RouteFor`** returns the resolved `DerivedRoute` — the graph plus the annotations that are not topology (advisors, and now rework loops) off one parse. `SpecFor` is a thin wrapper over it, so the three route renderers gained the annotation and no gate call site changed. (sty_8e0b29a0)

## [serve-v0.0.39] - 2026-09-10

### Changed
- **Serve path carries the rework step key and its route rendering.** `internal/web` (route panel), `internal/wfdot`, `internal/wfroute`, `internal/wfgovern`, `internal/verb` (the `cc` message field) and `internal/help` are on the satelled watch set; bump so `satelle update` refreshes the running service's route view and `agent-dispatch` topic. (sty_8e0b29a0)

## [0.0.497] - 2026-09-10

### Added
- **`satelle story chat --agent <binding>` — chat is a consultation mechanism, not an orchestrator console.** The session binding is now a choice: `--agent reviewer` opens the `[reviewer]` binding, `--agent <name>` any live-capable binding (`interface = "acp"` or `"stream"`), and no flag opens `[orchestrator]` exactly as before. A `command`/in-loop or missing binding refuses with the same message shape as today, naming the binding it was asked for. The session runs on that binding's own tool grant, so a consulted reviewer keeps its read-only `Read,Grep,Glob`. This is how the in-repo agent interrogates a rejection with the rejecting reviewer, or takes a second opinion to a block. (sty_a0372443)
- **`satelle story chat --from <role>` — the ledger says which agent spoke.** Defaults to `developer-agent` when `SATELLE_SESSION` is set (an agent is driving) and `human` otherwise. The driving turn is ledgered `<from> → <binding>` and the reply `<binding> → <from>`, so `satelle story messages <id>` shows both directions with the real roles; `agent_invocation` rows for tool boundaries and permission decisions are actored by the binding rather than a hardcoded `orchestrator`. (sty_a0372443)
- **The first turn's inbox is the chosen role's.** The payload's `messages[]` are those addressed to the opened binding's role or to `*`; a message addressed to another role is not delivered, and the inbox watermark keys on the chosen `--from` rather than the literal `human`. (sty_a0372443)

### Changed
- **A consulted binding is told it is consulting, not judging.** Charter selection is by binding name: `[orchestrator]` is the scheduler and is *driving*, so it keeps the executor charter; any other binding opened by hand gets a consulting charter stating that its reply is context and not a verdict, that the gate judging the edge still runs cold and one-shot over the payload satelle builds, and that it does not run `satelle story set`. Consultation never advances a stage — only a gate verdict does. See `satelle doc get principles satelle-agent-consultation`. (sty_a0372443)
- **`satelle help agent-dispatch`** — the orchestrator-binding section is now **Consultation sessions**, documenting `--agent` / `--from`, the live-capability requirement and its refusals, the address set, and the consulting charter. Permission policy is unchanged: mutators stay denied outside an executor-owned performing state, and are denied outright when a session is driven non-interactively. (sty_a0372443)

## [serve-v0.0.38] - 2026-09-10

### Changed
- **Embedded help carries the consultation-sessions topic.** `internal/help` is on the satelled watch set; bump so `satelle update` refreshes the running service's `agent-dispatch` topic. (sty_a0372443)

## [0.0.496] - 2026-09-09

### Fixed
- **Stop check no longer blocks a read-only session for a sibling session's engaged edits.** The Stop hook compared a repo-wide dirty tree against a session-scoped seat, so a second session in the same repo was told it had made ungated edits it could neither claim nor revert. The seat question is now repo-wide to match: a live seat held by a sibling session attributes the dirty tree to that holder, and stopcheck allows the stop with a `systemMessage` naming the holding story and session. The block still fires when no live seat exists anywhere in the repo; this session's own seat, a clean tree, exempt-only changes and `stop_hook_active` stay silent allows. The PreToolUse edit gate is unchanged and stays session-scoped. (sty_211d8419)

## [0.0.495] - 2026-09-09

### Added
- **`satelle sync bindings push` / `pull` — agent bindings on the workspace sync path.** `push` publishes the repo's `.satelle/workflows/agents.toml` into the bound team workspace's catalog (kind `agents`); `pull` applies the catalog entry as `.satelle/workflows/agents.workspace.toml`, a **layer under** the repo's own file in the precedence ladder (`repo → workspace → profile / global-role → embedded`): a repo field wins, a workspace field fills a blank, a workspace-only table applies. The bare `satelle sync` pulls it when the `agents` area is opted in. `satelle agent validate` and `doctor` report the layer and name the source of every effective field (`(workspace)` / `(repo)`). An unbound repo does nothing and contacts nothing. (sty_01949949)
- **Redaction is a property of the agents kind, not of a verb.** One function (`config.RedactAgentsTransport`) now guards every agents-layer transport: `sync bindings push`, `publish push` of the agents path (whatever `--kind`), the `agents` area of `sync config push`, and `sync bindings pull` on ingest. Literal env values are blanked with keys kept, pure `${VAR}` references survive (a name is not a secret), secret-shaped settings and `settings.env` are blanked, absolute paths in `command` or `settings` become base names, `profile=` is dropped; an unparseable body is an error, never shipped raw. (sty_01949949)
- **Local resolution.** A blank a redacted layer left is "declared, unsatisfied" and is dropped from the merge, so a pulled layer can never lay `KEY=` over the machine's environment. `${VAR}` expands from the local `[vars]`. A named binding whose program is not on PATH is a hard refusal at dispatch (never an in-loop fallback) and a `WARN` in `agent validate`. (sty_01949949)

### Changed
- **Writing a redacted agents layer back over the authored file rehydrates it.** `sync config deploy`, `publish adopt` and `publish check --update` either leave `agents.toml` byte-for-byte alone (the store copy matches it after redaction) or deploy the store's layout with this machine's env values, command paths and `profile=` re-applied from the local file. The agents-layer classifier is now one function shared by the loader, the workspace layer, transport redaction and migration. (sty_01949949)

## [serve-v0.0.37] - 2026-09-09

### Added
- **Serve path carries the workspace bindings layer.** `internal/config` (workspace tier, transport redaction), `internal/docindex` and `internal/help` are on the satelled watch set; bump so `satelle update` refreshes the running service. (sty_01949949)

## [0.0.494] - 2026-09-09

### Added
- **`satelle story chat <id>` and the `[orchestrator]` binding.** The orchestrator may now be a dispatched live session (`interface = "acp"` or `"stream"`) instead of the hook channel: satelle opens the session with the story payload plus `messages[]`, forwards human lines as turns, streams replies, and answers permission requests through the same predicate as the PreToolUse edit gate — a mutator outside an executor-owned performing state is denied without asking; otherwise the human is asked. Every human turn, orchestrator reply, tool boundary and permission decision is a ledger row (`agent_message` / `agent_invocation`), written from the synchronous transport event sink, never the lossy render channel. `command = "in-loop"` (or no binding) keeps today's hook channel; chat refuses rather than guessing a spawn. Steps dispatched from inside the session run the unchanged synchronous path and inherit the chat seat via `SATELLE_SESSION`. (sty_1de7494c)
- **Stream transport reports pre-allowed tools.** `tool_use` / `tool_result` content blocks — the only boundary for tools the binding allowed without a `can_use_tool` request — surface as `EventToolStart` / `EventToolEnd`. (sty_1de7494c)

## [serve-v0.0.36] - 2026-09-09

### Added
- **Serve path carries the orchestrator session opener and stream tool events.** `internal/agentcli` and `internal/help` are on the satelled watch set; bump so `satelle update` refreshes the running service. (sty_1de7494c)

## [0.0.493] - 2026-09-09

### Added
- **Live-session abstraction and `interface = "stream"`.** `internal/agentcli` now has a `Session` (open / send / events / permission policy / cancel / close). ACP is expressed on it; Claude stream-json is a third transport. `{system}`/`{payload}` ride the first user message; reviewers stay `command`. (sty_d244fe1b)

## [serve-v0.0.35] - 2026-09-09

### Added
- **Serve path carries the stream interface and session abstraction.** `internal/agentcli`, `internal/config`, and `internal/help` are on the satelled watch set; bump so `satelle update` refreshes the running service. (sty_d244fe1b)

## [0.0.492] - 2026-09-09

### Added
- **`satelle story message` / `satelle story messages` and `messages[]` on the transition payload.** Directed agent messages are ledger kind `agent_message` and are injected into the next gate or named-performer dispatch (oldest-first, engagement-scoped, 20×2KiB budget). `--to reviewer` reaches default gates; `--to executor` reaches named performers. Message-free payloads are unchanged. (sty_2db624d0)

## [serve-v0.0.34] - 2026-09-09

### Added
- **Serve channel carries `agent_message` ledger kind and the dispatch help contract.** `internal/ledger` and `internal/help` are on the satelled watch set; bump so `satelle update` refreshes the running service. (sty_2db624d0)

## [0.0.491] - 2026-09-09

### Changed
- **Workstate push is hold-and-reconcile.** Stories this location holds Apply in place (same sty_ id). Stories held elsewhere are skipped (holder named; items cursor not advanced so they are not dropped). Unbound/local-scope push is unchanged. (sty_f6cff549)

## [0.0.490] - 2026-09-09

### Added
- **`satelle story hold checkout|release|takeover`** against the hosted REST hold surface. Distinct from `satelle story seat release`. Engaging a story held elsewhere or unheld on the server is refused (takeover / checkout named); list/get still work. Unbound repos unchanged. REST workstate ingest/list stay unpublished. (sty_dec88606)

## [0.0.489] - 2026-09-09

### Added
- **Bound checkouts send a stable location identity on hosted sync.** A bound repo derives `loc_` + sha256(machine-id + repo path), persists it per-user beside credentials, registers once via `POST /api/v1/locations`, and stamps `x-satelle-location` on authenticated REST and gRPC Sync calls (including the 401-refresh retry). Login, project, and publish stay unstamped. Unbound repos are unchanged. (sty_e88d77ce)

## [0.0.488] - 2026-09-05

### Changed
- **Workflow advisor flags an implementation exit with no slice-quality reviewer.** Item 3 of embedded `satelle-workflow-advisor` still prefers a successor reviewer, but scope, design-tokens and packaging no longer count as judging the slice. The advice names the missing slot (a slice-quality or proof-honesty judge on the successor `reviewers` list, not a `[[gate]]`), not a skill the binary does not ship. Advisory only — `workflow validate` stays structure-only. (sty_fb934eef, sty_a8315e17)

## [0.0.487] - 2026-09-05

### Added
- **`satelle story proof` enumerates tests since the engagement baseline without judging them.** Same family as `satelle story diff`: JSON only, report-only, never runs tests. Go `_test.go` files include parsed `func Test*` names; other languages are listed with a skipped reason. No-baseline, no new tests, foreign tree and git-unavailable all exit 0 with a `state` field so a functional check can report rather than fail the transition. Not injected into reviewer payloads. (sty_76796b8e)

## [0.0.486] - 2026-09-05

### Added
- **Reviewer payloads carry the engagement slice.** Isolated reviewers (no shell) were told to read `satelle story diff`, but the transition payload injected docs, prior verdicts and route drift — not the slice — so quality review depended on the executor attaching it. The payload now includes a report-only `diff` object (`files`, `stat`, `patch`) whenever an engagement baseline exists, the same shape as `satelle story diff`. Missing baseline is a `no_baseline` marker, never a refused transition. The field is not keyed on this repo's status names. The patch has its own ceiling so it cannot starve the plan. Embedded `satelle-story-scope-review` prefers `payload.diff`. Enumeration, not verdict. (sty_a125b440)

## [0.0.485] - 2026-08-31

### Fixed
- **The diagnosis satelle already raised about a broken document is now put in front of you.** When an authored document fails its structure check, `satelle reindex` files a high-priority system story naming the exact file and fault — and nothing read it back, so it sat in backlog while the symptom was hunted (an hour, in the reported case, against a `done.toml` carrying an unknown key). Two surfaces now name it: a session start (`satelle hook context`) carries a one-line advisory naming the story and its document, and a transition refused because the governing route source will not parse ends with `already diagnosed — see sty_… (satelle story get sty_…)`. Bounded so it cannot become noise: only a non-terminal, high-priority story carrying the indexer's `type:system` marker AND a `doc:<kind>/<name>` relationship qualifies, so a repo with sound substrate gains no output at all. The refusal is unchanged when there is no such story, and still fails closed either way. (sty_88d40a60)

## [0.0.484] - 2026-08-31

### Added
- **The four Go toolchain pins are now asserted to agree.** `go.mod`'s directive, the tracked `mise.toml`, and `go-version` in both CI workflows had nothing tying them together, so a partial bump would drift silently — local gates, CI and the released binary each building on a different toolchain with no signal. A test under `make test` reads all four and requires them to agree on the version components each one declares, so CI's minor-only `'1.27'` floats the patch by design while `go.mod` and `mise.toml` must match exactly. A mismatch names every file and the version it declares, plus each conflicting pair. It is a check, not a generator: nothing rewrites a pin. Deliberately test-only — the paths it knows about exist because of how this repo is built, so they stay out of the binary. (sty_02d79953)

## [0.0.483] - 2026-08-31

### Changed
- **Go toolchain upgraded to 1.27.0.** `go.mod`'s directive, both CI workflows (`go-version: '1.27'`, still `check-latest: true`), and a new repo-local `mise.toml` pinning `go = "1.27.0"` so a local build cannot silently disagree with the toolchain a release is built on — and so a workflow gate's non-interactive check shell resolves the right Go from the repo rather than from whatever the machine's global default happens to be. No `toolchain` line: a gate run must never depend on a network toolchain download. `go test` now runs the `stdversion` vet check by default; it reports nothing on this tree, and nothing was suppressed to keep it quiet. (sty_5515036d)
- **`github.com/google/uuid` dropped as a direct dependency** in favour of Go 1.27's standard-library `uuid` package. Both id generators (`workitem` and `ledger`) call `uuid.NewV4().String()` explicitly rather than `New()`: an id keeps only the first 8 hex characters, and `New` is documented as equivalent to v4 only "at this time" — a v7's leading millisecond timestamp would make ids minted in the same instant collide. Id shape is unchanged (`sty_`/`tsk_`/`exe_`/`evt_` plus 8 lowercase hex) and now regression-tested. The module still carries uuid indirectly via `modernc.org/sqlite`. (sty_5515036d)

## [0.0.482] - 2026-08-31

### Added
- **The amend gate now ships with the binary, so `satelle story amend` works out of the box.** 0.0.481 landed the mechanism fail-closed with no embedded reviewer, which left the feature inert in every repo until an operator hand-authored both the rubric and the hook. `satelle-story-amend-review` now ships as an embedded default skill (`scope: system`) beside the other lifecycle reviewers, and the shipped declaration of done declares `amend_review = "satelle-story-amend-review"` once in its `[meta]` frontmatter alongside `create_review` — a lifecycle hook fires outside the status graph, so it is declared for the workflow, never per category the way park and cancel are. A freshly initialised repo resolves the hook and can correct a wrong definition mid-flight. **Nothing about fail-closed changed:** delete the line from your own `done.toml` (or point it at a rubric that does not resolve) and amend is refused again — that remains the escape for a repo that wants definitions permanently frozen, and an authored `done.toml` must restate the hook because substrate override is per file, not per key. No Go changed: the judgement lives in the skill body. (sty_5c768dd3)

## [0.0.481] - 2026-08-31

### Added
- **`satelle story amend <id> --reason …` — gate-approved correction of a story's frozen definition fields.** `title`, `body`, `acceptance_criteria` and `category` freeze when a story leaves its entry state, and until now a wrong definition mid-flight could only be escaped by cancel-and-re-raise: a new id, a lost ledger, and a full route re-walk. amend keeps the id and pays for it with judgement — `--reason` is mandatory, the repo's new `amend_review` lifecycle hook names the reviewer that accepts or rejects the correction (a reject writes nothing at all), and an accepted amendment records every field's old and new value as a `definition_amended` ledger row. It never moves status, and it is reachable from the parked (`blocked`) state, so a story parked over a wrong definition is corrected and resumed in place; terminal and cancelled states are refused, and a category amendment onto a lane that declares no such state is refused rather than stranding the story off-route. `satelle story set` on those fields stays refused, and now names the amend path. **Fail closed:** a repo that declares no `amend_review` hook — including the embedded defaults — has no amend path at all; with nothing to judge the correction the freeze holds. The route-drift remedy, `satelle help create-story`, and `satelle-route-drift-check` describe the amend path where it applies. (sty_81aa4d8f)

## [0.0.480] - 2026-08-30

### Fixed
- **`satelle story attach --file -` reads the document body from stdin, and an attach with no input flag says so.** `--file` did a bare `os.ReadFile`, so the conventional `-` sentinel failed with `open -: no such file or directory` — an agent piping evidence lost an invocation to it every time. `-` now reads the command's stdin (the idiom `execution record` already uses), documented in the `--file` usage string and the command's help; an attach with none of `--body`, `--file`, `--binary-file` is refused by name instead of silently storing an empty document. (sty_ef8a896b)
- **`story estimate` / `story actual` `--time` accepts a bare number of minutes.** `--time 38` failed with `time: missing unit in duration "38"` even though the tag written is literally `estimate-minutes`. A bare integer or decimal is now read as minutes; `30m`, `2h`, `1h30m` are unchanged; malformed or non-positive input is refused with `use a duration like 30m or 2h, or a bare number of minutes like 38`. The fix sits in the shared verb funnel, so CLI, web and MCP inherit it, and a positive duration under a minute now records as 1 minute rather than 0. (sty_ef8a896b)

## [0.0.479] - 2026-08-30

### Fixed
- **A status transition can no longer be clobbered by a write built on a stale snapshot.** `story set` reads the row, then spends the reviewer gate and the named-agent dispatch (minutes) before enacting the status — and the dispatched planner is handed that PRE-transition row, so a write it derives from it could land afterwards and drag status back to the FROM state while the ledger already recorded the move (seen three times in vire; healed only by `story reconcile --repair`). Every write `story set` derives from its snapshot is now a compare-and-set (`workitem.UpdateInput.ExpectStatus` → `AND status = ?`), so the stale writer is refused by name (expected vs. actual, "re-read and retry") instead of silently winning; a losing CAS inside the transition rolls the ledger append back with it. `Store.Upsert` — a full-row `INSERT OR REPLACE`, the other regression vector — now keeps the stored status when the incoming row is stamped strictly older, with `UpsertForce` as the explicit opt-out used only by `sync workstate pull --force`. Dispatch ordering is unchanged: a dispatch failure still refuses the transition. (sty_2c71eff6)

## [0.0.478] - 2026-08-27

### Fixed
- **`satelle update` no longer signals a stale system-unit MainPID or advises a hand-restart of a healthy service.** On a dual-unit host it used to trust `systemctl show MainPID` even when that pid was dead, print "did not respawn — restart manually", and exit 0 — skipping the kernel-fact path `satelle service status` already uses. Update now short-circuits when the live process (cgroup, then listen port) already matches the installed binary, only signals a system-unit pid that *is* the live service, and returns non-zero on failure phrasing. `satelle service restart` still cycles (the short-circuit is update-only). (sty_062656a5)

## [0.0.477] - 2026-08-27

### Fixed
- **`satelle workflow validate` refuses an obligation that names no step.** It used to parse `done.toml` and `step.toml` independently and PASS both, even when a category listed a step STATUS (`in_progress`) rather than a table KEY (`coded`). Derivation already refused that (`obligation "…" in category "…" has no discharging step`); validate now joins the governing route source and fails in the same words, so the verb an author is told to run after editing actually catches the usual TOML-route mistake. (sty_5d712bc5)

## [serve-v0.0.33] - 2026-08-27

### Changed
- **Serve channel follows the wfdot join check.** `internal/wfdot/route.go` is on the satelled watch set; bump so `satelle update` refreshes the running service with 0.0.477. (sty_5d712bc5)

## [0.0.476] - 2026-08-26

### Fixed
- **internal/cli test suite is hermetic against an inherited `SATELLE_SESSION`.** The engaged `satelle story set` process exports the session id, so the build-unit-check gate's `go test ./...` child inherited it — and the seat fixtures acquire leases with no session stamp, so their seats read as foreign-session and the hook-seat, edit-state and statusline tests failed, making the `in_progress→integration` edge impossible to pass from inside any engaged session. `tempRepo` (the shared fixture root that already isolates `SATELLE_HOME`/`SATELLE_CONFIG`) now scrubs `SATELLE_SESSION`; tests that need a session identity set their own after the fixture. (sty_dad373f0)

## [0.0.475] - 2026-08-25

### Fixed
- **CLI gRPC Sync Snapshot no longer dies at grpc-go's 4 MiB recv default.** Apply, Snapshot and Refresh set `MaxCallRecvMsgSize` / `MaxCallSendMsgSize` to 64 MiB (documented on the published contract). Real project snapshots already exceeded 4 MiB (vire ~7.6 MiB, satelle ~24 MiB). `ResourceExhausted` size failures now name the cap and the RPC. (sty_8c277e1b)

## [serve-v0.0.32] - 2026-08-25

### Changed
- **Serve channel catches up to serve-path commits since serve-v0.0.31.** CLI-only releases 0.0.460–0.0.474 left `satelled.version` at 0.0.31 while embedded substrate, ledger, and route code on the serve watch set moved. Bump so `satelle update` actually refreshes the running service. (sty_8c277e1b)

## [0.0.474] - 2026-08-25

### Changed
- **Skill naming and tag hygiene in the embedded defaults.** `satelle-estimate-actual-review` is a functional check — a `check` fence decides, no agent is dispatched — but carried only `type:reviewer`; it now also carries `type:functional-check`, the machine-readable kind marker audits and route conventions key off. `satelle-skill-naming` defined only `review` and `check` while the shipped corpus also contains advisors, summarisers and audits, so the principle was out of convention with its own corpus; the function list now names all five shipped roles, with the advisor entry written as an explicit carve-out to the bare-verb executor rule it sits above. The two coded checks named `-review` are recorded as known drift with three deprecation options and a forward rule — a skill name is a binding surface, so nothing is renamed. (sty_7a0b27e0)

## [0.0.473] - 2026-08-25

### Changed
- **Leaner always-resident descriptions in the embedded defaults.** A frontmatter description is paid on every discovery and every dispatch, and six ran 46-66 words as compressed copies of their own bodies. All six — `satelle-reviewer-self-contained`, `satelle-agent-model`, `satelle-skill-naming`, `satelle-recognise-blockage`, `satelle-done-is-last`, `satelle-task-validate-after-review` — are now 29-32 words, keeping a trigger clause and a one-line what. Every dropped clause was checked against its own body first; `satelle-done-is-last`'s claim that the binary enforces a "mandatory done gate on the workflow spine" was dropped rather than restated, because no such mechanism exists (`mandatory` is the step-summary flag, and "spine" is retired DOT vocabulary). Non-normative body prose also removed: `satelle-recognise-blockage`'s Motivation section, `satelle-story-blocked-triage`'s Worked-case pointer (it named no locatable artifact), and the anecdote in `satelle-executor-deliverables`' evidence-trap paragraph. `satelle-story-scope-review`'s baseline rule no longer narrates its own rollout. No accept/reject condition or executor obligation was lost. (sty_953cd947)

## [0.0.472] - 2026-08-25

### Changed
- **One owner per doctrine in the embedded defaults.** Three clusters stated the same rules in two or three files, where they would drift. `satelle-story-create-review` no longer re-explains the taxonomy `satelle-story-classification` owns and already links — it keeps the judgments unique to the create edge and both guards against a false reject (882 → 743 words). The stop-request/park path is derived only in `satelle-recognise-blockage`; `satelle-story-cancel-review` and `satelle-story-blocked-review` name the verb and keep their own accept/reject rules. `satelle-story-classification` stated repeated-keys and the sprint index form twice each; each is now stated once. No judging rule was weakened. (sty_f9c1d9b9)

## [0.0.471] - 2026-08-25

### Changed
- **The embedded route reference teaches the TOML form the parser accepts.** `satelle-route-standard`, `satelle-workflow-change-review` and `satelle-workflow-advisor` still described `done.md`/`step.md` with `## <category>` and `## gate <skill>` sections — artifacts the binary cannot read — so the reference other workflow tooling cites, and the gate that judges route edits, both named a form that no longer exists. Rewritten to `done.toml`/`step.toml` with category tables, obligation-keyed step tables and always-on gate entries, doctrine carried over unchanged, and leading with the one rule the format does not make obvious: an obligation names a step by its TABLE KEY, never by the status the step declares. A gate's `on` is the documented exception — it matches statuses. (sty_8c115e7a)

## [0.0.470] - 2026-08-25

### Fixed
- **Substrate that QUOTES TOML array-of-tables syntax no longer fails wikilink validation.** A `[[gate]]` inside a fenced ```toml block was read as a dangling wikilink, so any document showing workflow-authoring TOML failed `satelle principle validate`. Inline code spans were already exempt; fenced blocks (backtick and tilde, info strings, unterminated) now are too, via a shared byte-length-preserving `maskQuotedCode`. `satelle migrate --yes` shares the extractor and so also stops rewriting a retired name that a fence merely quotes. (sty_cc06b5a6)

## [0.0.469] - 2026-08-24

### Added
- **Runtime logs are reachable from the repo.** `.satelle/logs` is a symlink pointer to `~/.satelle/<repo-key>/logs` (storage stays home-keyed). Init plants it; migrate leaves it (Lstat, not Stat). The managed gitignore lists `.satelle/logs` only when `.satelle/` is not already ignored. `satelle logs --story/--role/--tail/--path` reads dispatch logs; `satelle story route` shows a `- log:` clause when a matching dispatch file exists. (sty_52634ccf)

## [0.0.468] - 2026-08-24

### Added
- **The PreToolUse edit hook identifies the calling agent from the payload, not file mtimes.** `[gate] no_implement_models` / `no_implement_message` are the model-role rule (absent = no model check; the binary ships no default glob or message). A separate `[gate] no_implement_exempt_paths` / `no_implement_exempt_globs` is that rule's carve-out, so enabling it cannot weaken `edit_exempt_paths`. Grok/Codex payloads with no resolvable caller model skip the model rule. `satelle hook explain --payload` prints the decision. Init prints a one-line notice when the keys are set and `~/.claude/settings.json` still names a foreign Edit hook. (sty_bdeff052)

## [0.0.467] - 2026-08-24

### Fixed
- **`satelle service status` no longer calls a cgroup-supervised process ephemeral when a sibling unit is inactive.** On a dual-unit host the inactive sibling made the bus reachable and the old `reachable` arm printed the release-skill failure line even though the live pid's cgroup named an installed unit that was supervising it. The verdict now names which unit the cgroup identifies (user vs system) and only mentions a sibling when both units are actually installed. A process found only by listening port still reports ephemeral. (sty_29b2af69)

## [0.0.466] - 2026-08-24

### Fixed
- **A slice that only DELETES git-ignored substrate is now enumerable.** The engagement baseline snapshots a `substrate_manifest` — the sorted, repo-relative paths of every authored substrate file present at engage — and `satelle story diff <id> --include-substrate` unions the manifest paths that have since vanished. Retiring a drifted `.satelle/` override in a repo that git-ignores its substrate no longer fails the substrate close gate with "no change set found": the mtime walk can only see files that still exist, so the manifest is what a deletion is measured against. The git-only default leg is unchanged, and a baseline without a manifest (any engagement predating this) reports no deletions and no error. (sty_7e1e2deb)

## [0.0.465] - 2026-08-24

### Added
- **A re-reviewed gate now sees what it already judged.** The transition payload carries `prior_verdicts` for the same story and the same from→to edge — `{skill, decision, notes, created_at, attempt}`, oldest first, attempts numbered over the full edge history and windowed to the most recent 5, each note excerpted to 2 KiB so it can never starve the `docs` budget. Absent on a first attempt, so a reviewer that ignores it costs nothing. The embedded `satelle-story-plan-review` skill now asks for every blocking finding in one verdict, and on a re-review to close or keep open the previous verdict's findings rather than raising a fresh disjoint set. (sty_0f5e600c)

## [0.0.464] - 2026-08-18

### Fixed
- **A parked story with an empty `park_origin` can resume again.** Workstate pull used to wipe the stamp (the wire omitted the column; Upsert replaced it with blank). Resume now derives the last transition into the park when the column is empty; the wire and Upsert keep a non-empty stamp; route text keys “resumes to origin” on the synthesised park, not on “has outbound edges”. (sty_524f091d)

## [0.0.463] - 2026-08-18

### Fixed
- **A process-temp write is no longer gated as an in-repo product edit.** The hook allows a target under `os.TempDir` / `/tmp` at plan and with no seat, even when a custom `[gate] edit_exempt_paths` omits `/tmp/`; a repo that lives under the temp dir still gates its own tree. (sty_e33f78fe)

## [0.0.462] - 2026-08-18

### Fixed
- **A route source that does not parse no longer reads as ungoverned.** A broken `done.toml` / `step.toml` fails closed (distinct from “no workflow”); refusals name `satelle reindex`; doctor structure-checks the TOML halves; `List(workflows)` refreshes from disk so a revert restores governance without a manual reindex. (sty_5b88aa1b)

## [0.0.461] - 2026-08-18


### Fixed
- **A recorded status_transition can no longer leave the story row on an older status.** Transition writes the row and the ledger event in one transaction; a hosted workstate snapshot will not overwrite a newer local row or a later local transition; `satelle story reconcile [--repair]` repairs existing drift; doctor reports it. (sty_8e01ba7f)

## [0.0.460] - 2026-08-15


### Added
- **Logs in the top nav.** A machine-wide Logs page at `/logs` tails satelled's `serve/server.log` (newest first, last 500 lines), filters by level, and live-updates over the existing `/events` stream. (sty_df4a0785)

## [serve-v0.0.31] - 2026-08-15

### Added
- Top nav Logs item opens `/logs`; live tail of the active server.log. (sty_df4a0785)

## [0.0.459] - 2026-08-15

### Changed
- **Landing Updated is local time plus a Push Failing tag.** The workspace row shows the local ingest stamp only. A standing push failure is a compact tag in that cell, not a log dump. The project page still shows both clocks and the fail log. (sty_a447695e)

## [serve-v0.0.30] - 2026-08-15

### Changed
- Landing Updated cell is the local ingest stamp; a standing fail is a `Push Failing` tag. Fail reason and log path stay on the project page. (sty_a447695e)

## [0.0.458] - 2026-08-14

### Changed
- **Project times name their plane.** Header and landing read `local: updated … · remote: pushed …` so the two clocks are not two unlabeled “just now”s. (sty_8104248a)

## [serve-v0.0.29] - 2026-08-14

### Changed
- Project header and landing Updated cell prefix ingest and hosted-push times as `local:` and `remote:`. (sty_8104248a)

## [0.0.457] - 2026-08-14

### Changed
- **Hosted workstate refresh is gRPC.** `satelle sync workstate push` (the satelled child) rotates expired tokens via Sync.Refresh on the same connection as Apply. A valid or expired session no longer requires POST /oauth/token on that path. Browser login and REST config/documents refresh stay HTTP. (sty_d94eb1a4)

## [0.0.456] - 2026-08-14

### Changed
- **Help is formatted markdown at full content width.** `/r/{slug}/help` renders `satelle help` topics as HTML (not a raw `<pre>`) and fills the content panel. (sty_067dc4b0)

## [serve-v0.0.28] - 2026-08-14

### Changed
- Help page renders markdown and spans the full `.wrap` width. (sty_067dc4b0)

## [0.0.455] - 2026-08-14


### Fixed
- **Project header keeps both time labels.** "updated" is last confirm against the repo; "pushed" is last successful hosted push. The client ticker no longer strips "pushed". (sty_8931ab84)

## [serve-v0.0.27] - 2026-08-14

### Fixed
- "pushed" stays visible next to the last hosted-push time (label is outside the ticker-managed `<time>`). (sty_8931ab84)

## [0.0.454] - 2026-08-14


### Fixed
- **CLI unit tests no longer seed the live satelled landing.** Isolating `SATELLE_HOME` also sets `SATELLE_SERVER_ENDPOINT=none` unless the test already chose a serve URL, so `story create` in `go test ./internal/cli` cannot POST snapshots to :8787. (sty_cb74c03b)

## [0.0.453] - 2026-08-14


### Changed
- **Hosted server URL is machine-scope only.** `config.HostedServerFor` / `ResolveHostedServer` no longer fall through to repo `[sync] server`. Leftover `[sync] server` or `[hosted] server` in a repo file is a stray (doctor/status); `satelle migrate --yes` re-homes an unset machine value and never clobbers a set `[hosted] server`. The init example no longer seeds a repo server line. Write the origin with `satelle settings --global server` or `satelle login`. (sty_34037275)

### Added
- **Satelled serves writable Global settings** at `/settings/global` (loopback-only POST into `~/.satelle/config.toml [hosted] server`). The project settings page links there. (sty_34037275)
- **A standing push failure names its reason** on the project page and landing row (not only a tooltip) and points at satelled's `serve/server.log`. `satelle doctor` still emits `sync.failing` with that exact text. (sty_34037275)

## [serve-v0.0.26] - 2026-08-14

### Changed
- satelled mounts loopback-only GET/POST `/settings/global` for the machine hosted-server URL. (sty_34037275)
- Standing push failures render the recorded reason and the server.log path on the landing and project page. (sty_34037275)

## [0.0.452] - 2026-08-13


### Fixed
- **`satelle migrate --yes` seeds `[sync] stale_after` even when nothing else is pending.** A repo already on current structure used to print "already on current structure" and leave the key absent, so doctor could not judge staleness. (sty_30696eeb)

## [0.0.451] - 2026-08-13

### Added
- **`satelle doctor` reports workstate backup health and no longer PASSes an unbacked repo.** Per-partition last successful push, standing failure, and age vs `[sync] stale_after` are recorded by satelled (and by `satelle sync workstate push`) as readable JSON — no hosted call, no re-attempt. A repo whose work-state areas are all local is reported as such and is not flagged. `stale_after = "24h"` is a live key in the init scaffold; `satelle init` / `satelle migrate --yes` seed it on existing repos without clobbering a set value. The mirror landing and project page show push-failing / last-pushed beside freshness. (sty_30696eeb)

## [serve-v0.0.25] - 2026-08-13

### Added
- satelled records per-partition workstate push/snapshot outcomes; the mirror UI shows standing push failure. (sty_30696eeb)

## [0.0.450] - 2026-08-13

### Changed
- **Hosted workstate delivery moves off the CLI command path onto satelled.** `satelle story create` / `satelle story set` no longer open a connection to satelle.dev. The local ingest (`POST /ingest/snapshot`) is the trigger: satelled execs `satelle sync workstate push` after a short debounce. The push is cursor-driven, so mutations made while the daemon was down ride the next start; a persistently failing hosted target backs off (30s–15m) without growing unbounded work. `satelle sync` remains the manual escape hatch and still exits non-zero on a failed hosted call. The post-mutate Apply hook is gone — that is the 505-on-every-create. (sty_c526753a)

## [serve-v0.0.24] - 2026-08-13

### Added
- satelled forwards local ingest to hosted workstate via `satelle sync workstate push` (catch-up on start, reconcile backstop, per-repo backoff). (sty_c526753a)

## [0.0.449] - 2026-08-13

### Changed
- **Machine-scope listen keys live only in `~/.satelle/config.toml` `[service]`.** `web_port` and `[server] endpoint` are no longer read from a repo's satelle.toml. satelled binds port/addr from `[service]`; the CLI publishes to `[service] endpoint` (or `http://127.0.0.1:<port>`). `[sync]` scopes stay per-repo. A leftover machine-scope key in a repo file is a warning (doctor/status) naming the key, the value satelled used, and `satelle migrate --yes`. That command copies unset machine keys then removes the repo leftovers; already-set machine values are not clobbered. `satelle settings --global port|addr|endpoint` is the write path; `satelle settings web_port` (repo scope) fails with the same warning. No new required config — defaults remain 8787 / 0.0.0.0 / derived endpoint. (sty_21a7d16d)

## [serve-v0.0.23] - 2026-08-13

### Changed
- satelled listen addr/port come only from machine `[service]` config. (sty_21a7d16d)

## [0.0.448] - 2026-08-13

### Changed
- **The local daemon is now `satelled`, not `satelle-serve`.** New installs and `satelle update` place the UI binary at `satelled`. Existing units that still ExecStart `satelle-serve` keep working until you re-point them: run `satelle service install` (it prefers a sibling `satelled`, then a leftover `satelle-serve`). The CLI command is still `satelle`. Hosted satelle-server is unchanged. This is not a repo-scaffold break — it does not require `satelle init`. (sty_bd9de06d)
- **Local-tier naming is satelled everywhere the operator sees it.** `satelle serve` / `satelle service` help, the unit Description, `satelle service status` (`satelled — …`), daemon log prefixes, README, and release assets all say satelled. The systemd unit filename stays `satelle.service`. The serve release tag prefix stays `serve-v*` so older CLIs still find the channel. (sty_bd9de06d)

## [serve-v0.0.22] - 2026-08-13

### Changed
- Daemon artifact renamed satelle-serve → satelled; `.version` key is now `satelled.version`. (sty_bd9de06d)

## [0.0.447] - 2026-08-13

### Fixed
- **`satelle init` and `satelle migrate --yes` fold leftover `[hosted]` into `[sync]` and drop the table.** A repo that still has `[sync] all = "personal"` plus `[hosted] project = …` (the collector-sdk shape) becomes the simple `[sync]` view. Already-set `[sync]` keys are not clobbered. The init scaffold no longer presents a repo `[hosted]` table. Machine-wide login `~/.satelle/config.toml [hosted] server` is unchanged. (sty_5eb1bb8a)

## [0.0.446] - 2026-08-13

### Fixed
- **Same-tree sessions no longer share an engagement seat.** A lease is stamped with the harness session id (published from the hook, resolved at `story set`); a sibling session with a different id is not IN FLIGHT-denied on a transition it is not driving. Unstamped seats keep today's tree-routed permissions. The in-flight deny names the story without claiming the hook is the driver when it cannot establish that. Isolated performers inherit the parent's `SATELLE_SESSION`. (sty_a8ef4041)

## [0.0.445] - 2026-08-13

### Fixed
- **Edit gate no longer denies the drafting path it names.** Out-of-tree `/tmp/` drafts are seeded exempt (an outside-tree prefix never punches into the session repo). Deny text is derived from live `[gate]` config and no longer promises `/tmp` or dump names the gate does not allow. `satelle migrate --yes` materialises missing `edit_exempt_globs` and appends `/tmp/` onto a non-empty `edit_exempt_paths`; an empty list stays an opt-out. Product-path Writes stay gated. (sty_e8e1879c)

## [0.0.444] - 2026-08-13

### Changed
- **Work-state sync is gRPC only.** `satelle sync workstate push` calls Sync/Apply; `pull` and rehydrate call Sync/Snapshot. The leftover REST methods (`PushWorkstate`, `ListWorkstateItems`, `ListWorkstateLedger`) and the published `/api/v1/projects/{project}/workstate` paths are gone. OAuth, config, documents, and publish stay HTTP. (sty_f85fac97)

## [0.0.443] - 2026-08-13

### Changed
- **Repo connection settings live on `[sync]`, with satelle.dev defaults.** Operators no longer author a `[hosted]` table or Fly proxy knobs. Unset `[sync] server` is `https://satelle.dev`; unset `[sync] project` is this repo's directory name. `satelle project bind` and `login --workspace` write `[sync]` keys. A leftover `[hosted]` table still resolves; `satelle migrate --yes` copies missing server/project/workspace onto `[sync]` without clobbering keys already set. The machine-wide login server still wins. (sty_a13d7c4a)

## [0.0.442] - 2026-08-13

### Changed
- **Checkout-sync Apply/Snapshot now speak gRPC.** `hosted.Client.Apply` and `Snapshot` dial `satelle.sync.v1.Sync` on the hosted origin (`https` → `:443` TLS, `http` → explicit port or `:80`). Every RPC carries lowercase `authorization: Bearer <token>` from the credstore. `UNAUTHENTICATED` refreshes over HTTP `/oauth/token` and retries once — the same policy as REST `doAuthed`. REST `PushWorkstate` / list methods stay for other callers. Generated stubs live in `internal/hosted/syncpb` so `internal` does not import `api/`. satelle-serve still does not import hosted. (sty_3658edff)

## [0.0.441] - 2026-08-13

### Added
- **Edit-gate filename globs so agent story-reference dumps are not blocked.** A sibling `[gate] edit_exempt_globs` key matches basenames via `filepath.Match` (a pattern containing `/` is repo-relative). Init seeds `sty_*_body.md` and `sty_*_ac.md` into the gate config and the managed `.gitignore` block; migrate appends missing managed globs to a non-empty list and leaves an empty list as a deliberate opt-out. Prefix exemptions are unchanged. The no-engaged-story deny text names the session scratchpad, `/tmp`, or those dump names. Removing the authored globs re-gates the names — the decision is configuration, not a Go branch. (sty_fefc88cd)

## [0.0.440] - 2026-08-13

### Added
- **Triggered work-state Apply after local mutate, and a Snapshot CLI verb.** When `[sync]` stories/executions/ledger (or `all`) are opted in and a hosted credential exists, a successful story/task/ledger write posts the existing workstate ingest route. Transport failure is logged and does not fail the local write. `satelle sync workstate snapshot` pulls current hosted rows into the local store; satelle-serve may exec that verb and must not import `internal/hosted`. Bare `satelle sync` is unchanged. (sty_adaf4db1)

## [0.0.439] - 2026-08-13

### Added
- **Stories and tasks carry an assignee (hosted principal id).** Create stamps the cached `PrincipalID` from the credstore when signed in; engage refuses if the item is assigned to someone else (the error names the holder — not "allocated" or "actor"). An empty assignee (offline, or a pre-column row) can be engaged; if a principal is cached, that engage stamps it. No credential keeps assignee empty and still works. The machine seat is unchanged. (sty_8ccaa906)

## [0.0.438] - 2026-08-13

### Added
- **Published the gRPC checkout-sync contract.** `api/checkout_sync.proto` defines `service Sync` with `Apply` and `Snapshot` only (Subscribe is a later story). Hand-written Go types in `api/sync_types.go` mirror the proto and stay JSON-identical to the existing REST workstate shapes. OAuth stays on HTTP `/oauth/*`; every Sync RPC carries lowercase `authorization: Bearer <token>`; `UNAUTHENTICATED` means refresh over HTTP and retry once. satelle-server copies this proto the same way it copies `openapi.yaml`. REST workstate methods are unchanged (sty_7ece3dc3)

## [0.0.437] - 2026-08-07

### Fixed
- **`satelle reindex` no longer aborts its task-sync leg on one hand-authored file.** Every run of `satelle reindex` in this repo ended with `reindex: task sync: workitem: upsert needs an id`. The culprit was a single run file, `.satelle/tasks/tsk_backlog-groom/exe_full-backlog-cull.md`, whose frontmatter parses cleanly but carries no `id:`: `parseItemFile` accepted it, `upsertIfChanged` hit the store's (correct) empty-id rejection, and `ingestExecutions` RETURNED that error — so one unusable file silently cost the repo its entire task index. A file's NAME now supplies its id when the frontmatter has none. That is a widening of an existing convention rather than a guess: the `tsk_`/`exe_` prefix filters upstream have already decided the stem is a well-formed id of that kind, and `ingestItemBody` has always done exactly this for embedded defaults — both now go through one `idFromName` so the two cannot drift. The offending data file was deliberately NOT edited: adding an `id:` to it would clear the symptom and leave the class live for the next hand-authored run file (sty_0828e855)

### Changed
- **A task file sync cannot use is now named, not silently dropped and not fatal.** `SyncTasks` returns a `skipped` list of `<path>: <reason>` notes; a per-file parse or upsert failure becomes a note and the sync carries on to its siblings, and `satelle reindex` prints `reindex: task sync: skipped <path>: <reason>` so an operator goes straight to the offending file. Genuinely fatal errors — creating or reading the task dir, and the store list that drives adoption — stay errors, so reindex keeps its documented pass-through contract in both directions: it never fails on a bad task file, and it never hides one either. On this repo the fix took the leg from aborting to `tasks: indexed 4` — four task files that had never reached the store at all (sty_0828e855)

## [0.0.436] - 2026-08-07

### Fixed
- **A story resumed from a park no longer swallows the commits another story landed while it sat there.** Entering a park writes a change_record anchored at park-time HEAD, but nothing re-anchored on the way OUT, so the next transition enumerated `parkHEAD..resumeHEAD` and attributed every intervening commit to the resumed story. Observed on sty_90e903e9: parked at 4882b8b, resumed past cee11af, and it acquired 9 product paths (`.version`, `CHANGELOG.md`, `internal/cli/*`, `internal/verb/*`, `tests/*`) it never touched — enough for `satelle-substrate-only-check` to reject the close, naming another story's slice back at it. Leaving a park now enumerates NOTHING and instead writes a re-anchor row carrying the resume HEAD, so the story's next transition starts counting from where it actually resumed. Park-ness is read from the workflow spec (`IsParkState`), not a hardcoded status, so this keys to the route in any repo; an unparsable workflow re-anchors nothing and behaves exactly as before. The row is deliberately a FULL record rather than `partial_enumeration` — both anchor walks skip partial rows, so marking it partial would have defeated the fix — and carries `reanchor_resume` so a zero-file row never reads as 'this step changed nothing' (sty_526d6a68)

### Changed
- **Both channels the close gate reads now honour the resume anchor, not just the recorded one.** `satelle-substrate-only-check` unions the recorded change_record set with a LIVE `satelle story diff <id> --include-substrate`, and that live leg anchored at `firstEngagementBaseline` — so fixing only the recorded side would have left the gate rejecting on exactly the same paths. The live path now takes its enumeration anchor from the newest re-anchor row when one exists, and reports that anchor as the baseline rather than claiming to have diffed from a point it did not. Scoped to WHEN only: the working-tree anchor `refuseForeignTreeDiff` enforces and the `baseline_dirty` flag still come from the engagement baseline, because a re-anchor answers 'since when', never 'which tree'. The regression test asserts both channels and carries a control proving it fails without the fix (sty_526d6a68)

### Note
- This fix is FORWARD-ONLY. Stories whose ledgers were already contaminated stay contaminated — nothing legitimately deletes a ledger row — and need their own disposition; sty_90e903e9 was cancelled and superseded for exactly this reason (sty_526d6a68)

## [0.0.435] - 2026-08-07

### Fixed
- **A substrate prune now leaves change evidence for the engaged story.** `satelle substrate prune --yes` deleted unedited seed copies and wrote no `change_record` row, which stranded the very slice it performed: deletions of git-ignored `.satelle/` files are invisible to EVERY channel the `satelle-substrate-only-check` close gate unions — the recorded channel had no rows, the live git leg cannot see an ignored path, and the mtime substrate leg cannot see a file that no longer exists. The gate then refused the close with "no change set found" even though the slice was real and lane-clean, and the only routes out were faking evidence or abandoning a correct change. Prune now records what it removed: the new `verb.RecordPathMutation` ledgers a `change_record` row carrying the removed paths (repo-relative, forward-slashed — the path space the gate greps), so `satelle story diff <id> --recorded` reports them and the close gate accepts a deletion-only slice with no gate-skill change. The seat is resolved off the app handle the command already opened rather than a second `app.Open()`, and recording is best-effort — a ledger failure warns and never costs the operator their prune. `satelle migrate`'s prune passes no story id: it is repo maintenance, not a slice of authored work (sty_30d3bd99)

### Changed
- **A verb-written change row never becomes the enumeration anchor.** `change_record` rows now carry `partial_enumeration`, set only by `RecordPathMutation`, and both anchor walks (`changeRecordSinceTime`, and `changeRecordAnchor` via `lastFullChangeRecord`) skip past them to the newest FULL row — falling through to the engagement baseline when every row is partial. Without this the fix would have introduced the defect it repairs: a partial row states only the paths one verb touched, so letting it anchor would advance the mtime window past authored substrate no full enumeration had seen, and an edit made before a prune would silently drop out of the next transition's change set. The guard is symmetric with the existing `head_sha != ""` one, and the regression test asserts the INVERTED property — a partial row must leave the anchor where it was, while a full enumeration must still advance it (sty_30d3bd99)

## [0.0.434] - 2026-08-06

### Added
- **Version-stamp drift is named at session start instead of discovered in a refusal.** The binary is machine-wide, so `satelle update` in one repo puts EVERY repo on the new binary immediately while each repo's `.satelle/deployed.version` keeps naming the binary that last deployed its scaffolding. That gap had no surface: `readDeployedVersion` was read only by `refuseBreakingDrift`, which runs through `openAppForCmd` for store-backed verbs — so a repo behind the binary learned nothing until a verb REFUSED it, and at session start that verb is the SessionStart hook's own `satelle reindex`, landing the refusal before the operator types anything. `satelle hook context` now injects ONE advisory line when the stamp is behind: the binary version, this repo's stamp, whether the range crosses a breaking release, and the exact heal command — or, for an unstamped repo, that the next store-backed verb will refuse it. It sits BESIDE the existing scaffold-drift block, which keeps its own trigger and text; neither replaces the other. It stays an ADVISORY: `hook context` is deliberately not store-backed and gained no refusal path, because a hook that errors breaks the session instead of informing it. And it is SILENT when the stamp matches the binary, and on every dev build — a line per session for nothing is a token toll on every session in every repo. The breaking classification reuses `verb.ChangelogRange`/`CmpSemverExported`, and the breaking scan that was inline in `breakingDriftError` is now the shared `firstBreaking`, so the refusal and the advisory can never describe the same range differently. Deliberately NOT in scope: nothing auto-runs `satelle update` or `satelle init` at session start — update swaps a machine-wide binary and cycles the live service, init mutates the repo, and both would change the rules under a story another session holds engaged (sty_8ecdae90)

## [0.0.433] - 2026-08-06

### Fixed
- **satelle no longer blocks a session for files satelle wrote.** `init` seeded `[gate] edit_exempt_paths = [".satelle/", ".gitignore"]`, but the binary also writes harness hook scaffolds under `.claude/`, `.grok/` and `.codex/` — some lazily, mid-session, on the first store-backed verb, with no story engaged and no operator action. At the end of that session the stop gate refused it: "these edits were made UNGATED: .claude/". satelle made the change and then blamed the session for it, and the only route out was hand-editing the config the binary had already suggested in a comment. The seeded list is now the footprint the binary actually deploys — `[".satelle/", ".gitignore", ".claude/", ".grok/", ".codex/"]` — and the commented remediation variant is gone, because there is nothing left to remediate. It is still exactly the deployed footprint and nothing more: product paths stay gated, an empty list still gates everything including `.satelle/`, and no path is special-cased in Go — `exemptTarget` resolves solely from config, so this changes the seeded DEFAULT, not the mechanism. The binary now gives ONE answer about its own footprint: the embedded `satelle-substrate-only-check` gained `.codex/`, and a test reads that skill's embedded `allow=` expression and asserts every seeded managed entry appears in it, so the two cannot diverge again in silence (sty_926cfcdc)

### Changed
- **Already-initialised repos converge with `satelle migrate --yes`, not re-init.** Re-init never clobbers authored config, so it does not widen an existing list — `migrate` is the path. It appends every missing managed entry in one rewrite, preserving operator additions and their order; `satelle init` WARNs and names the exact missing entries until you run it. An explicitly empty `edit_exempt_paths` stays a deliberate opt-out and is never converged. Until a repo converges, the old stop-gate refusal is still waiting in it (sty_926cfcdc)

## [0.0.432] - 2026-08-05

### Added
- **Route drift is detected and named instead of blaming the workflow.** A story's `workflow:` stamp records the LIFECYCLE; the LANE inside a derived route is re-derived from the story's category at every transition, so adding or altering a category table re-lanes work already in flight — with no re-stamp, no ledger entry and no notice. Until now a story stranded that way got the generic `skipped-step` text ("the route declares no step at all after X"), which sends the reader to fix a workflow that is not broken. It now gets `route-drift`: both lanes named — what the story walked, what its category derives now — and a remedy that is legal from where it sits. Reclassify while the story is still in its entry state; past that, category is frozen, so the refusal says cancel-with-reason and re-raise carrying `supersedes:<id>` rather than suggesting a `--category` edit the definition freeze would refuse. `wfgovern.DetectRouteDrift` is the enumeration, `verb.RouteWalked` reads the walked lane from the route document's outcome half — parsed in the package that WRITES that heading, pinned by a render→parse round-trip test, so a format change cannot silently disable the guard (sty_6e4f7fd8)

### Changed
- **The transition payload carries `route_drift` when, and only when, drift exists.** The binary enumerates and attaches; it decides nothing about whether a given drift is tolerable. That verdict is configuration: the new embedded `satelle-route-drift-check` judges the softer case — history off-lane while movement is still legal — and ships UNWIRED, so a repo names it on the step it wants guarded, best the engagement step, where reclassification is still legal. On the common path nothing is attached and nothing is dispatched (sty_6e4f7fd8)

## [0.0.431] - 2026-08-05

### Added
- **Every CLI command carries a brief, focused `Long`, and a test keeps it that way.** `--help` is the only description of a verb an agent can read at runtime, and 77 of the 132 commands had only a one-line `Short` — including `story create`, `story set`, `story list`, `story log`, `story seat`, `story estimate`, `task create`, `execution create`, `ledger append`. Each now answers the two questions a `Short` cannot: when this verb is the right one, and the constraint that is not guessable from the flags (`--category` selects the route and freezes on leaving the entry state; `--status` is a transition whose gates can refuse it; the estimate gate greps TAGS, not a plan section; `story cost` is measured cost while `actual-*` is self-report; heartbeat age separates a LIVE seat from a STALE one). `story`, `task` and `execution` share one factory, so their help is written once and PARAMETERISED — seat arbitration and the definition freeze appear only for stories, `--parent` only for executions. `TestEveryCommandHasLong` and `TestLongIsBrief` walk the cobra tree (group parents included; `completion`, `help` and hidden commands excluded), failing on a missing `Long` and on one over 130 words / 18 lines, with an exact-match shrink-only waiver for the 19 commands whose help is genuinely operational documentation. A fixture-tree test proves the guard fires for a command added tomorrow, not just for today's surface. `satelle story diff` was trimmed 166 → 129 words rather than waived, losing no fact (sty_a499e7f5)

## [0.0.430] - 2026-08-05

### Changed
- **The seat concurrency mode is now visible to the agent that must obey it.** `[engagement] parallel` joins the repo settings schema, so `satelle settings` lists it, `satelle settings engagement.parallel` reads it, `satelle settings engagement.parallel epic` writes it (creating the `[engagement]` table that `init` does not seed), and the read-only web settings page renders it — one schema row, every surface, because they all iterate `config.Settings`. The displayed value is the RESOLVED mode, so an unset key reports `none` rather than `(unset)`. The SessionStart seat inject now states the ACTIVE mode: under `none` a single factual line on a held seat, under `epic` the two preconditions a second engagement must satisfy — a sibling under the SAME parent, from a DISTINCT git working tree. With no seat held the inject stays silent under the default and emits the mode under a non-default one, which is the case where the rule matters before a collision rather than inside a refusal. Mechanism only: nothing here says when a repo should choose a mode, and satelle still polices no sibling contention (sty_050f3a19)

## [0.0.429] - 2026-08-05

### Added
- **A shipped `docs` lane for doc-only slices.** The embedded `done.toml` gains a `[docs]` category table — `raised → authored → docs-verified`, with the working lane's park and cancel — so a zero-config repo routes a documentation story through an authoring step and ONE proportionate close instead of the code-shaped `coded` step and its close reviewers. `step.toml` gains the two steps; the close is the new `satelle-docs-only-check`, a functional check that unions the story's recorded, live-plus-substrate and commit evidence channels and rejects any path that is not doc-typed (and rejects an empty change set, which is not evidence of anything). The lane's file-type scope IS its configuration: `doc_paths` ships as markdown only and a repo widens or narrows it by overriding that skill under `.satelle/skills/` — no route or verdict decision moves into Go. A repo carrying its own `done.toml` does not inherit the table (authored substrate overrides the embedded default per FILE); it adopts the lane by copying the table and the two steps, which the `[docs]` comment states (sty_e05bb183)

### Changed
- **`satelle-story-create-review` judges route proportionality.** A story whose declared surface is entirely prose is rejected off a code-shaped category with the lighter lane named (`category: docs`, or the repo's own doc lane), so the route is corrected at create where it is cheap rather than mid-route where it is not. The embedded step-summary gate now also fires on the docs lane; the estimate gate deliberately still does not (sty_e05bb183)

## [0.0.428] - 2026-08-05

### Fixed
- **`satelle init` stops reporting correct configuration as broken.** Five checker defects, each of which made a repo's live, load-bearing configuration look like damage (sty_338a53f8):
  - **`[node.alloc]` counts ADVISOR bindings as allocations.** An agent bound only on an edge — `done.toml`'s park `advisor`, or a step's `advise` — was reported orphaned because the allocation walk read node `agent=` only, and advisors are deliberately absent from the emitted Spec (Spec is topology). The walk now derives each governed category through `wfgovern.RouteSpecFor`, the single derivation chain, so the advisors come off the same parse the Spec did. They are counted as USED and nothing more: an advisor is consulted by the orchestrator and never dispatched, so it carries no role or context-channel obligation.
  - **`[reviewer.unsafe]` no longer claims a live shell grant "is never used".** The claim is about the whole system, not one binding: a reviewer SKILL the workflow reaches may shell `satelle` itself, and then the grant is exactly what makes that gate work. The advisory now runs after the workflow walk, where the reachable skill set is known, and stays silent when a reachable skill shells satelle in a code position. It still fires for a grant nothing exercises. `Validate`/`ValidateEffective` are unchanged; the skill-body resolver arrives via the new `ValidateEffectiveWithSkills`, wired at `doctor.Check` (and so `satelle init`) and `satelle agent validate`.
  - **Hook completeness is per-harness.** `incompleteHookEvents` demanded `Stop` of every harness, so every re-init WARNed that `.codex/hooks.json` was incomplete — about a file the Codex builder deliberately writes without a Stop hook. The expected set now comes from the same table the builder writes from.
  - **One definition of a harness's hook surface.** The heal path expected Codex PreToolUse matchers (`write_file`, `shell`) that no builder emits. Builders, healer and completeness check all read `harnessHooks` now. The create-path values are canonical, so the Codex heal matchers NARROW to the documented tool names — existing files are still recognised by command marker, not matcher, so no repo is rewritten.
  - **The diverged-substrate advisory prints the path the backup was actually written to.** It composed `.satelle/backups/diverged/…`, but backups land under the backup root — the home-keyed runtime dir in a real run — so the path it named did not exist.

## [0.0.427] - 2026-08-05

### Added
- **Seat concurrency mode: `[engagement] parallel = "none" | "epic"`.** The engagement seat is single-occupancy by default, and that default is unchanged — `none` (and an absent section) is byte-for-byte today's behaviour. Under `epic` the seat is claimed at the story's PARENT, so sibling children of one parent may hold sub-leases concurrently, each from a distinct git working tree; a story under a different parent (or none) is refused while such a seat is held, with the refusal naming the parent that holds it, and a parentless story engaging first claims the seat as a singleton. The parent is never engaged — the parent id is an arbitration KEY, not a lease on the container. `engagement_lease` gains `seat_key` and `worktree` (idempotent migration; empty values on pre-upgrade rows behave exactly as before — singleton key, unanchored tree). `lease.Acquire` keeps its signature over a new `AcquireWith(AcquireOpts)`; the lease package applies the key and tree it is handed and never learns the mode's name. An unsupported value (including `"all"`, a recorded non-goal) is refused at config load naming the supported modes. Satelle arbitrates ENGAGEMENT only: convergence policy — merge order, batch suite runs, one push — is the repo's own substrate, and satelle performs no sibling-contention policing, by design (sty_c098dc2d)

### Changed
- **`satelle story diff` is anchored to the working tree its story was engaged from** (sty_c098dc2d). The engagement baseline now records the git toplevel, and an engaged story's diff refuses when invoked from a different tree, naming both the recorded path and the invocation path — run from another tree it would have enumerated that tree's unrelated changes and attributed them to the story. Baselines recorded before the anchor existed carry no tree and keep the previous CWD-resolved behaviour, so an upgrade cannot start refusing an in-flight story's own diff.
- **Every "is the seat held?" reader generalises to N≥1 live story-seat leases** (sty_c098dc2d). `evaluateSeat` returns every qualifying live seat instead of the first; the SessionStart seat inject renders them all, led by this session's own; the create-path `__seat_probe__` and the derived committed-status scan both apply the same arbitration key, so the two cannot disagree about a sibling created straight into an engaging state. A session picks ITS seat by recorded working tree — the default owner is pid-less `local@<host>`, so co-held leases are indistinguishable by owner, and heartbeat routed by owner alone would let the actively-worked sibling go stale at TTL. Task leases still never hold the story seat.

## [0.0.426] - 2026-08-05

### Fixed
- **CLI mutations now reach the live web view without a refresh on large repos.** End-of-verb UI drain posts a light partial snapshot (stories/tasks/seats/identity + recent ledger merge) instead of the multi-MB full body, so it lands inside the 1.5s hang budget. Snapshot apply is one transaction with a detached server deadline so a timed-out drain cannot half-replace a partition. Full snapshots remain for workspace add and the 5-minute reconcile (sty_3562c820).

## [serve-v0.0.21] - 2026-08-05

### Fixed
- **Atomic snapshot ingest with partial-kind support.** `ApplySnapshot` commits all replace/merge kinds in one transaction; `Kinds`/`MergeKinds` on the wire leave unmentioned partitions untouched; `snap_hash` is only set for full bodies so light drains cannot suppress the next reconcile (sty_3562c820).

## [0.0.425] - 2026-08-05

### Fixed
- **Publish the route-panel CSS fix on the live service.** v0.0.424 shipped the CLI with the `.route-doc` chrome alignment (sty_c3b1eb57) but left `satelle-serve.version` at 0.0.19, so `satelle update` reported serve already up to date and the running web service kept the old stylesheet. Bump to serve-v0.0.20 so the embedded `app.css` reaches the persistent service.

## [serve-v0.0.20] - 2026-08-05

### Fixed
- **Route document panel matches sibling prose cards** on story detail pages (sty_c3b1eb57) — full detail-column width and shared card chrome; list attachments keep the 760px measure.

## [0.0.424] - 2026-08-05

### Fixed
- **Route document panel on a story detail page now matches sibling prose panels.** The expanded route card (`.route-doc`) takes the full detail-column width (`max-width: none`) and wears the same border radius, padding and panel background as Description / Acceptance criteria (`pre.prose`). Collapsed documents in the Documents list keep the 760px reading measure on `.doc-article`. CSS-only; no markup change (sty_c3b1eb57)

## [0.0.423] - 2026-08-04

### Added
- **SHA-keyed shared suite evidence** — record an expensive verification suite once and let sibling stories cite it instead of re-running it. `satelle ledger record-run --story <id> --command <cmd> --outcome green|red [--sha] [--started-at] [--finished-at]` writes a `suite_run` ledger row (sha defaults to HEAD); `satelle ledger cite-run --story <id> --run <evt_id>` writes a `suite_citation` whose `refs` names it (the first use of `Entry.Refs`); `satelle ledger citation [story-id]` enumerates the facts a gate needs — citation present, cited run resolved or dangling, its outcome/command/sha, current HEAD and dirty flag, `sha_matches_head` — and exits 0 for every enumerable state, stdin-tolerant like `story diff`. Enumeration only: the accept rule and the refusal names (`missing_citation`, `dangling_citation`, `red_run`, `command_mismatch`, `dirty_worktree`, `stale_sha`) live in the gate's check block, documented verbatim in `satelle help reviewer-checks` and executed by the tests so docs and behaviour cannot drift. `ledger.Store.GetByID` added for the primary-key read `ListFilter` cannot express; no schema change (sty_183a0510)

## [0.0.422] - 2026-08-04

### Added
- **Binary story attachments** at the attach boundary: `satelle story attach --binary-file` stores PNG/PDF/etc. byte-for-byte with a `.satelle.json` sidecar (story id, type, content type, size, sha256). Cap (10 MiB default) and content-type allowlist (SVG/HTML denied) are enforced in the verb; overridable via `[attachments]` in satelle.toml. Binary bodies never enter gate payloads (`fillPayloadDocs` skips them like `type:change`). CLI `story doc --out` writes binaries without streaming raw bytes to stdout. MCP wire contract pinned for the hosted server (`attach_story_document_binary`); implementation is not in this repo (sty_40e5a305).

## [0.0.421] - 2026-08-03

### Added
- **Retired embedded substrate map.** `internal/config/retired.go` declares renamed and removed embedded names. `satelle validate` enriches dangling wikilinks with the retirement note; `satelle migrate` reports them and, under `--yes`, rewrites renames only (removals stay report-only). Future retirements must add a row to the map (sty_55a6aaeb). Known cases: `[[satelle-dot-standard]]` → `[[satelle-route-standard]]`; `[[satelle-configuration-over-code]]` retired into the constitution. Heal: `satelle migrate` (dry-run) then `satelle migrate --yes` for renames, or edit citing files by hand for removals. Validate still fails on the dangling ref (intentionally — not a warning).

## [0.0.419] - 2026-08-03

### Fixed
- **`satelle migrate` no longer deletes operator hand-made `agents.*.bak` as legacy runtime residue.** Those backups are outside migrate's ownership; keep them outside scan roots (or next to the machine-wide catalog). Residue remains satelle.db*, logs/, backups/, stories/ only (sty_0445104b)

## [0.0.418] - 2026-08-03

### Fixed
- **`make install` no longer stamps a plain release version onto a dirty or unreleased tree.** `scripts/build-version.sh` appends `+<sha>[-dirty]` when HEAD is not the tagged release (or the tree is dirty), so `isDevVersion` demotes the fleet-wide binary out of gating. Install still writes `~/.local/bin` by default; recovery remains `satelle update` / `--force` (sty_022929ef)

## [0.0.417] - 2026-08-03

### Fixed
- **`satelle update` no longer treats a matching version string as proof the installed bytes match the published release.** When versions already match, update compares the local file against the published asset checksum and reinstalls on a mismatch (retagged version, or a machine-wide unreleased `make install`). Skips name the identity compared; offline/unverified identity is reported rather than claimed "up to date". `satelle update --force` reinstalls even when version and checksum match (recovery without a scratch clone). Dogfood requires checksum identity, not version alone (sty_1cd2ff01)

## [0.0.416] - 2026-08-03

### Fixed
- **Off-Linux serve-leak detection is no longer a silent no-op.** Registry-only inventory detects suite-owned leaks; every suite run prints OK vs PARTIAL coverage so green is not mistaken for a full /proc sweep (sty_948a2d42)

## [0.0.415] - 2026-08-03

### Fixed
- **Embedded help no longer points at a dogfood-only decision record.** `satelle help global-agents` states the committed-substrate rationale inline (Why committed); unreachable dogfood-repo / gitignored-path / story-as-authority deferrals are banned in help topics and embedded substrate under their existing owners (sty_a319db89)

## [0.0.414] - 2026-08-03

### Fixed
- **Gate cost now includes cache tokens.** `UnwrapUsage` and `usageFromMap` sum `cache_creation_input_tokens` + `cache_read_input_tokens` into InputTokens so `satelle story cost` shows the full prompt, not the uncached remainder. Presentation is stated in the cost note; legacy rows keep pre-fix meaning (sty_8178f1c6)

## [0.0.413] - 2026-08-03

### Added
- **Personal backup for the machine-wide agent profile catalog.** `satelle agent profiles push` / `restore` upload and rehydrate `~/.satelle/agents.toml` via `PUT|GET /api/v1/me/files/agents.toml` (not project sync). `[vars]` is never uploaded; restore is non-destructive without `--force` (sty_940938e3)

## [0.0.412] - 2026-08-03

### Added
- **Repo agents.toml committed-substrate posture is product guidance.** `satelle help global-agents` states that `.satelle/workflows/agents.toml` is committed by default (role set a clone needs for gated steps); secrets and machine HOW stay in the catalog / satelle.local.toml. Init scaffold and managed gitignore comments match; TestHelpGlobalAgentsTopic and init tests pin the claim (sty_552d2d87)

## [0.0.411] - 2026-08-03

### Changed
- **Recorded satelle-code-ac-review cost delta after sty_343fe595.** Post-change stories sty_4c986ed8 (16,617 / 1 round) and sty_bf797fa9 (62,871 / 3) vs baselines 68,699/6 and 53,654/3 — including cases where cost did not fall (sty_c5f64fec)

## [0.0.410] - 2026-08-03

### Changed
- **Integration entry runs mechanical preconditions before code-ac-review.** Project workflow lists satelle-ac-evidence-check and satelle-retired-route-grammar-check first on in_progress→integration; TestProjectWorkflowReviewerFirst matches multi-reviewer Skills (sty_a7eed214)

## [0.0.409] - 2026-08-03

### Fixed
- **Seat activity coverage tests.** TestSeatListExposesActivity and TestGate_emitsActivity pin the AC1 paths the plan named; plan slice 5 (hook/UI decoration) is explicitly deferred (sty_598a8e1b)

## [0.0.408] - 2026-08-03

### Added
- **Queryable transition progress on the engagement seat.** While a transition is in flight, `satelle story seat` reports the current gate/phase, its index in the set, total gates, and elapsed time since that activity stamped. Progress is written best-effort onto the lease row so any observer can inspect a running gate without the dispatching terminal (sty_598a8e1b)

### Fixed
- **Dead transitioning pid recovery is regression-locked with a deliberate kill test.** Spawn a real child, stamp its pid on the lease, assert live, Kill+Wait, assert EffectiveInFlight false and re-acquire without force-release (sty_598a8e1b)

## [serve-v0.0.19] - 2026-08-03

### Added
- **Seat activity columns** for live gate progress (label/index/total/at) on engagement leases (sty_598a8e1b)

## [0.0.407] - 2026-08-03

### Fixed
- **Black-box cost tests assert measured and unreported rows by name.** Gate skill line carries 54/59 or —/—; TOTAL names unreported count only when applicable (sty_56aae77a)

## [0.0.406] - 2026-08-03

### Fixed
- **Integration cost-view tests assert unreported usage as — and the step-edge self-report nudge.** The honesty note substring is re-pinned; a black-box test drives a plain-text reviewer and refuses a confident 0/0 (sty_56aae77a)

## [0.0.405] - 2026-08-03

### Fixed
- **Unreported gate usage no longer renders as a confident zero.** `usage_available` propagates from the agent transport through agent_invocation into `satelle story cost` and the web timeline. Unreported rows show `—`; totals sum measured rows only and name how many invocations were unreported. Legacy rows without the field still treat `tokens_total > 0` as measured. A step-edge stderr nudge after `story set --status` prompts for `step-self-report` (sty_56aae77a)

## [serve-v0.0.18] - 2026-08-03

### Fixed
- **Timeline token chips omit unreported usage.** Web eventChips reads `usage_available` from the shared ledger.EventTelemetry reader so an unmeasured invocation never shows a confident 0 tok chip (sty_56aae77a)

## [0.0.404] - 2026-08-03

### Fixed
- **Integration suite stamps testBin with release ldflags.** TestMain now applies the same `-X buildinfo.Version=…` flags as `make build`, so `writeDeployedVersion` is live under test and TestDeployedVersionStampAndBreakingDrift / TestScaffoldDriftSurfacesAndHeal no longer sit permanently red. SATELLE_BIN=dev builds still skip those paths loudly via isReleaseTestBin (sty_4c986ed8)

## [0.0.403] - 2026-08-03

### Fixed
- **Integration suite no longer leaves `satelle serve` orphans.** All serve spawns go through `StartServe` with process-group kill and Linux `Pdeathsig`; TestMain fails the suite if any suite-owned serve survives the run (sty_bf797fa9)
- **Stuck engagement `in_flight` no longer wedges the edit gate.** The transitioning process pid is recorded on the lease; when that pid is gone, `EffectiveInFlight` is false immediately. Owner stays pid-less `local@host` so sequential CLI invocations still share the seat (sty_bf797fa9)

### Changed
- **Gate consumers read `EffectiveInFlight`, not raw `InFlight`.** Includes evaluateSeat engagement, edit permission, seat list, and migrate live-runtime decoration (sty_bf797fa9)

## [0.0.402] - 2026-08-03

### Fixed
- **`satelle story restamp` accepts the name the create path assigns.** `WorkflowStates` resolved a lifecycle by looking up a DOCUMENT of that name, and the derived route is the one lifecycle with no document of its own — it is BUILT from the two route-source halves. So the lookup always missed and restamp refused with `workflow "default" does not resolve in the substrate`, on the exact value `WorkflowNameFor` hands out at create. The bug predates the rename (the old file-pair name missed the same way); naming the route is what made it visible, because "default" reads like something that obviously ought to resolve. It now resolves BY NAME, and only when a route source is actually present — a repo with no route, or with half a route, still refuses, because a stamp naming a lifecycle that cannot be built is the failure the check exists to prevent. Pinned by a round-trip test asserting that whatever `WorkflowNameFor` returns, `WorkflowStates` accepts: they are two halves of one contract and nothing else held them together (sty_81bb0dde)

## [serve-v0.0.17] - 2026-08-03

### Fixed
- **The running service actually picks up the TOML route source.** `satelle-serve.version` was already `0.0.16` in `.version` before this story — declared by an earlier slice, but never tagged — so a machine that installed serve while 0.0.16 was untagged carries OLDER code under that same version string. This story's release then cut `serve-v0.0.16` at its own commit, making the tag's content current while the installed binary stayed behind. `satelle update` compares VERSION STRINGS, so it reported "satelle-serve already up to date (serve-v0.0.16)" and replaced nothing — the exact silent-staleness failure the serve-version gate exists to prevent, arriving through the one door it does not watch: a version that was bumped but not published. Bumping to 0.0.17 is the smallest change that makes the difference visible to `satelle update`. Without it the live panel keeps reading a repo's route with a binary that cannot parse TOML, so a CONVERTED repo renders whatever the embedded default says — a lifecycle it no longer authors — with nothing anywhere reporting a mismatch (sty_81bb0dde)

## [0.0.401] - 2026-08-03

### Breaking
- **The route source is TOML.** A repo whose `.satelle/workflows/` still holds `done.md` and `step.md` has NO route: every story transition, `satelle doctor` and `satelle validate` are REFUSED by name rather than run ungated. Half a conversion is caught too — `done.toml` beside `step.md` is not a route, and applying the converted half alone would drop every gate the other half declared (sty_81bb0dde)
- **Rename `done.md` to `done.toml` and `step.md` to `step.toml`, then translate the records — `satelle init` does NOT convert them.** Read `satelle help workflow-convert`: it now carries a key-by-key md-to-toml mapping for every construct the markdown spelling had. The one part that is not a straight transcription is the step table's KEY — markdown keyed a step by its heading and named its obligation in `provides:`, TOML keys it by the OBLIGATION — so check that twice. Then diff `satelle workflow show <category>` for every category against the markdown you started from, gate by gate, before deleting the old files (sty_81bb0dde)
- **The `workflow:` stamp on a story names a workflow, not a file pair.** A story governed by the derived route is stamped `workflow:default`; one governed by an authored workflow keeps that workflow's own name. `satelle migrate` rewrites any surviving `workflow:done.md+step.md` stamp in place — dry-run first, `--yes` to apply (sty_81bb0dde)

### Changed
- **`wfdot`'s hand-rolled key/value reader is deleted, not left beside a second front end.** The route source is records with terse commentary, and it is now written in a format that already had a parser in `go.mod`: `[table]`, `#`, `[meta]`. The bespoke reader's own header claimed it avoided adding a TOML dependency; `github.com/BurntSushi/toml` had been there the whole time for `satelle.toml` and `agents.toml`. It had already cost a defect — it skipped only a line STARTING with `<!--`, so a multi-line HTML comment failed on every line after the first (sty_0ba83c4e, sty_8a7987de), and the remedy was a prose convention rather than a parser. Unknown keys are an ERROR now, never a silent drop: a typo'd `reviewrs =` that parsed as "no reviewers" would lose a gate (sty_81bb0dde)
- **`docindex` ingests `.toml` for the workflows kind, with frontmatter from a `[meta]` table.** One predicate (`docindex.Indexable`) answers which files are authored substrate and one reader (`docindex.Frontmatter`) answers what their frontmatter is, sniffing the form rather than being told it — so `wfgovern.SpecFor`, `workflow show`, `validate` and the web panel keep working through `docindex.Doc` with no second resolution path. Three seams that carried a private `.md` test were the real cost of the old shape and are now routed through those two: `validateAuthoredDir` (which silently reported "validated 0" on a good route), `substrate edit`/`prune`/`restore`/placement (which looked for an embedded default at a path it no longer had), and `workflow create` (which wrote `step.md` that the resolver then refused). The repo's `agents.toml` is excluded by name — it is machine configuration in the same directory, not a workflow (sty_81bb0dde)
- **The format tuition the representation makes unnecessary is deleted.** Both route halves carried a "HOW TO READ THIS FILE" preamble teaching a grammar that existed nowhere else, and the seeded `workflows/README.md` taught it again. TOML needs no preamble. What survives is the one rule the format does NOT make obvious — a step's table key is the obligation it discharges, and its `status` is a stage name that repeats by design — plus the reasoning about why a route is shaped the way it is. The conformance test inverted with it: it used to REQUIRE the preamble, and now bans format tuition while requiring the model statement, so the deletion cannot over-correct into a file that explains nothing (sty_81bb0dde)
- **A lifecycle hook can be declared in the TOML form.** `[[meta.hooks]]` is read alongside the markdown `hooks:` block and the `create_review = "…"` shorthand, and all three land on one shared validation so a rule stated once applies to every spelling. Two defects found by making that work: `[meta]` keys were exempted from the strict unknown-key pass by decoding the table into a sink, which left the ELEMENTS of an array-of-tables unmarked — so a route source declaring a create hook failed to parse outright; and `wfgovern.FrontmatterList` scanned for `---` only, making `applies_to` invisible on a TOML doc, which for a key that SELECTS a lifecycle means ungoverned. Both now read through the one frontmatter reader (sty_81bb0dde)
- **The `embedded_sha` provenance stamp works in both forms.** `ApplyStamp`/`StripStamp` understood only `---` frontmatter, so a TOML default could not be stamped — which made every embedded route half look operator-authored to `init`, `rebase` and the placement audit. They now sniff the form and stay exact byte inverses in each, or re-stamping an unedited seed would register as an edit and be backed up on every init (sty_81bb0dde)
- **A `[[gate]]` in prose is no longer a dangling wikilink.** TOML's array-of-tables header is character-for-character a wikilink, so the audit skips `.toml` bodies outright and, in markdown, skips inline `code spans` — a doc that shows `` `[[gate]]` `` is naming syntax, not referring to a document (sty_81bb0dde)

## [0.0.400] - 2026-08-03

### Changed
- **The workspace landing shows when each project was updated, instead of a red stale badge on some of them.** The badge (sty_e6e467fe) was binary where the useful signal is continuous: it said nothing at all until `mirror.StaleAfter`, then said the same thing forever — so "updated 2 minutes ago" and "updated 2 hours 59 minutes ago" looked identical, four permanent red chips trained the eye to ignore red, and the chip sat INSIDE the Project cell pushing the name around. A rightmost `UPDATED` column now carries the elapsed time on EVERY row (`just now`, `12 hrs ago`), with no alarm colour and the absolute stamp one hover away in the `title`. Nothing is lost — it is the same last-ingest fact, continuously and in the form the reader actually wanted. The project page header takes the same inline form, so one fact has one presentation rather than two. The `Stale` VIEW-MODEL booleans go with it: both were computed per request and read by nothing once the render became unconditional, and a boolean surviving there reads as though a threshold still governs what is displayed. `mirror.Partition.Stale`, `mirror.StaleAfter` and the unrelated story-seat `Stale` are untouched (sty_226a661e)
- **The phrase stays current on a timer that needs neither focus nor a refetch.** Each stamp renders as `<time class="rel-time" datetime="…">`, and `app.js` re-derives the text from that absolute instant every 30s. It is deliberately outside `initLive` and deliberately NOT visibility-gated: the SSE closes on a hidden tab because HTTP/1.1 caps ~6 connections per host and a stream per hidden tab starves the ACTIVE one (sty_a4fc4d00), but a timer holds no connection and asks the server nothing, so gating it would only make a returning tab briefly show a stale phrase. 30s not 60s, because a 60s tick can be a full minute wrong exactly at the boundary where the phrase is most read. `relPhrase()` in app.js mirrors `relTime()` in page.go bucket-for-bucket — one table, floor arithmetic, identical plurals — so the first tick over an unchanged instant recomputes the same string; a 15-case boundary test pins the Go side and a literal-string fence catches a divergent edit to the JS side. `copyCellCounts` carries the new cell, without which the column would silently freeze after the first SSE tick with no error anywhere — and that fence had to be scoped to the function body, because the first version searched the whole file and passed with the class removed, satisfied by the comment that explains the trap (sty_226a661e)

## [serve-v0.0.16] - 2026-08-03

### Changed
- Carries the workspace landing's UPDATED column and freshness ticker (sty_226a661e). The templates and static assets live in `internal/web`, so the running service only picks this up through a serve release — the version gate from sty_a8853e85 named the four changed serve-path files and correctly excluded the test files.
- Also carries the TOML route source (sty_81bb0dde). `internal/docindex`, `internal/wfgovern`, `internal/config`, `internal/store`, `internal/structure` and `internal/web` are all in the serve dependency set, so the running service reads the route through the same seams the CLI does — without this the panel would resolve a lifecycle the repo no longer authors.

## [0.0.399] - 2026-08-03

### Fixed
- **The serve-version gate watches what the serve binary is actually built from.** `scripts/check-serve-version.sh` demands a `satelle-serve.version` bump when serve-path sources change, and its watch set was a hand-kept four-entry array while `cmd/satelle-serve` compiles in **sixteen** in-repo packages. Eleven were unwatched, including `internal/serve`, which runs ONLY inside the service. The failure is silent by construction: with no serve bump `satelle update` reports "already up to date", the operator sees a green release, and the running service keeps the old code — which is very nearly how the reconcile fix in 0.0.398 shipped to nobody. The array is gone; the set is now DERIVED from `go list -deps ./cmd/satelle-serve` at run time, because a hand-kept list is a second answer to a question the compiler already answers, and it drifted. Config-over-code holds: the pass/fail RULE stays in the script, and `go list` is a mechanism it invokes to enumerate a surface. Two new modes, `--paths` and `--check-path <file>`, make the gate testable without fabricating commits. Deriving adopts packages shared with the CLI (`config`, `wfdot`, `wfgovern`, `workitem`…), so serve bumps become frequent — correct, not incidental: those packages are compiled into the running service, so their behaviour changed there too (sty_a8853e85)
- **The enforcement lives where CI can see it.** `cmd/satelle-serve/servepaths_test.go` re-derives the dependency set on every run and fails when the gate misses any of it, so a package joining the serve build is caught on the commit that adds it rather than by the next person to read the path list. It sits beside `deps_test.go` — the natural mirror of `TestServeBinaryImportIsolation`, which asserts what must NOT link — and deliberately NOT under `tests/`, where 112 of 113 files carry `//go:build integration` that GitHub CI does not run; a guarantee against a silent gate that CI never checks is the same defect one level up. A third test pins the no-false-positive half against the exact packages the isolation test proves are unlinked. Reverting the old array by hand fails these tests naming all eleven missed packages, and a `go test` cache trap found while proving that — the tests drive the script through a subprocess, so the cache served a stale pass — is closed by reading the script from the test process, with a comment saying why the read is load-bearing (sty_a8853e85)
- **Two fail-closed defects in the first draft of that derivation, found by running the failure paths rather than reading them.** `serve_paths()` called `exit 1` on every error, but the call site was `mapfile -t SERVE_PATHS < <(serve_paths)` — a process substitution is a subshell, so the exit ended only the subshell and the parent continued with an EMPTY array. With `go` absent from PATH the gate passed **green** while deriving nothing; and an empty array in `git diff -- "${SERVE_PATHS[@]}"` supplies no pathspec at all, meaning every file in the repo, so a broken derivation flagged unrelated files instead of refusing. Both now refuse: the status is captured with `$( )` and tested in the parent, with a parent-side empty guard. The trap is recorded in the script because the wrong form reads better than the right one (sty_a8853e85)

- **A new file in a serve package no longer sails through, and a test file no longer demands a release.** `git diff` cannot see an UNTRACKED file and the release path runs this gate before staging, so ADDING a source file to a serve package — as real a change as editing one — passed green; `git ls-files --others` now counts. The carve-out that had to come with it: `*_test.go` is compiled into `go test` and never into the shipped binary, so it cannot make a running service stale, and demanding a serve release for one is a false positive (a pre-existing one — editing `deps_test.go` alone would have tripped the old gate too). Only `*_test.go`; testdata can be embedded, so it stays watched. `TestServeGateSeesNewFiles` plants each case against the real gate; it deliberately SKIPS rather than asserting when the baseline tree is already red, so the `_test.go` leg's evidence is the direct run (sty_a8853e85)

- **CI fetches full history, because the gate it now tests needs a baseline.** The first push of this work FAILED CI and published nothing: `actions/checkout@v5` defaults to a shallow, TAGLESS checkout, and the gate resolves the latest `serve-v*` tag as its baseline — with no tag it takes the early "no serve-v* tag yet — ok" exit and answers ok to everything, so a deliberately planted serve source file "passed" and the test reported a hole that was not there. The gate was inert in CI and the test asserted against it as if it were live: the story's own defect class, one level up. `fetch-depth: 0` fixes it, with a comment naming the symptom so nobody optimises the checkout back. Two guards come with it: the script REFUSES when a `serve-v*` tag exists but its commit is absent (a shallow clone, not a first release — the diff yields nothing and the tagged `.version` cannot be read, so every check below would report ok having compared against nothing), and the test skips loudly rather than concluding anything when no baseline tag is present. Reproduced in real shallow clones both ways round before and after the fix (sty_a8853e85)

### Changed
- **`make check-serve-version` runs on EVERY release, not on the author's read of the diff** — `.satelle/skills/release.md` step 0 drops its four-path condition (and the stale duplicate of the list, which derivation makes unnecessary). Both failure modes are recorded there so the choice stays legible: the chosen rule can block a CLI-only release on another session's unshipped serve-path change, which is loud and correct; the rejected one — author's judgement, the old rule — is silent by construction (sty_a8853e85)

## [0.0.398] - 2026-08-02

### Fixed
- **The mirror reconcile loop stops writing the same failure line every pass.** `Reconciler.pass` logged one line per repo it could not re-seed, every interval, forever — on this machine 9 repos × one line every 5 minutes, roughly 40KB of a 396KB `server.log` and ~1,150 lines a day for repos that cannot heal without a human. A repeated line is not information: it says the same thing on the hundredth pass as on the first, so the log stops distinguishing a NEW failure from a long-standing one, and it dilutes the request log it shares a rotation budget with. A repo is now reported on a TRANSITION — when it starts failing, when the reason CHANGES, and when it recovers (with how many passes it was broken for) — so silence means unchanged rather than lost. The failure line is byte-identical to the one this loop has always written, so an operator's grep still matches, and a repo that leaves `Targets` is forgotten rather than emitting a bogus recovery across a gap it was not polled over. Failure identity is the full error text, deliberately: `reseedViaCLI` inlines the child's output, so a changed refusal re-logs once — which is exactly what happened when 0.0.396 gave every DOT-era repo a new reason to fail (sty_a2162ee3)
- **Deliberate: no backoff.** Every target is still re-seeded on every pass, including one failing identically for a day, and `pass` says why in the file rather than leaving it to be re-derived. This poll is the ONLY signal that a repo recovered — nothing notifies the service when an operator heals one — so backing off would delay the recovery report by the backoff, trading a log problem for the stale-view problem this file exists to fix. The cost avoided was never the subprocess; it was the log volume, which suppression removes outright. Suppression touches the log line and never the reseed, and the test asserts the attempt count alongside the line count so "suppressed the work instead of the message" fails loudly. All four pre-existing reconcile tests pass unmodified, and each new assertion was verified to fail when its mechanism is removed (sty_a2162ee3)

## [serve-v0.0.15] - 2026-08-02

### Changed
- Carries the reconcile-log fix (sty_a2162ee3). The loop runs inside `satelle-serve`, so the running service only picks it up through a serve release — without this bump `satelle update` reports "already up to date" and the fix never reaches the live process.
- Also publishes the serve-path work already merged since serve-v0.0.14 but never shipped as a serve artifact: the derived-route workflow panel and the route-source web changes from 0.0.383–0.0.385 and 0.0.391. `make check-serve-version` had been red on that accumulated debt; this clears it.

## [0.0.397] - 2026-08-02

### Changed
- **`satelle help workflow-convert` covers every construct in the wild, not eight of fifteen.** The topic is the only artifact standing between a DOT-era repo and a stuck agent — every "declares no route" refusal points at it — and a survey of all 37 authored graphs on a real fleet found 15 constructs in use against 8 documented. All seven gaps now have a target. Two are called out rather than tabled, because a table row is too thin for what they cost: **`on_enter_agent=` / `on_enter_prompt=` are a RETIRED MECHANISM, not a renamed key** — flat dispatch removed entry dispatch, so the advisor re-homes as `advise <agent> @<skill>` which the ORCHESTRATOR consults and entry never fires, and both failure modes are named (transcribing it literally fails at parse, which is the GOOD outcome; dropping it silently keeps a route that validates green with behaviour gone). **`goal=`, `vars=` and the `guardrails:` block belong in the constitution** — present in every graph, holding real operator intent, with no home in the route grammar and nothing that warns when they vanish. `reviewer_skill=` sits next to the edge-gate rule it is the legacy spelling of, since it lands on the step the edge ADMITS and putting it on the source moves the gate a step early; `rankdir=` is dropped (sty_e184768e)
- **The two things a document cannot supply are named as DECISIONS.** Which categories get a `done.md` section — an authored done.md overrides the shipped one WHOLLY, and `## execution` / `## task` / `## substrate` are the ones that vanish silently (the shipped default has no substrate lane at all, so it must be authored rather than inherited) — and whether the close step stays ungated. The second was sharpened against a real graph: a terminal edge is often ungated while the terminal NODE carries the close reviewer, so the topic tells the reader to check BOTH before recording "ungated was chosen" for a lifecycle that was gated all along. The verify loop is now an instruction with an order — `satelle workflow show <category>` and `satelle story route <id>` read against the retired graph gate by gate, for every category the graphs claimed, and BEFORE `satelle migrate --yes`, which deletes the thing you diff against. The repo-agnostic guard caught a pre-existing leak in the topic's own example: it named a graph FILE that no longer ships under any name, now stated as what the graph declares (sty_e184768e)

## [0.0.396] - 2026-08-02

### Fixed
- **The DOT retirement is marked Breaking, and the refusal carries its own remediation.** `refuseBreakingDrift` exists to stop a repo whose `deployed.version` predates a breaking release — but the guard keys on a `### Breaking` marker in the changelog, and the series that retired the DOT front end (0.0.383–0.0.385) declared `Removed`, `Changed`, `Added` and `Fixed` and nothing else. So the one release that invalidates every repo with an authored DOT workflow walked past armed and silent: on this machine 12 of 15 registered repos went from a pre-0.0.383 stamp to 0.0.395 unimpeded and each discovered the breakage only when someone tried to work in it. `## [0.0.385]` now declares `### Breaking` — one marker, on the release where `wfdot.Parse` was removed and a route-less workflows doc began refusing, since 0.0.383 touched only this repo's substrate and 0.0.384 was additive, and a single marker in `(deployed, binary]` fires the guard for every repo stamped before it (sty_b36c051c)
- **A breaking release now writes its own heal path.** The refusal hardcoded "run `satelle init`", which for this class sends the operator somewhere that does not help — init does not convert DOT. `breakingDriftError` renders the Breaking entry's OWN bullets verbatim under a preamble the binary owns, so 0.0.385's remediation (`satelle migrate`, `satelle help workflow-convert`, and the explicit "init does NOT convert") reaches the operator with no per-release Go branch; an entry that declares nothing specific still falls back to `satelle init`. The rejected alternative was having the drift check consult `workflowConversionState` — it couples a version gate to workflow-shape detection and puts the remediation choice back in Go. The `release` skill now says a `### Breaking` bullet IS the text an operator reads as instructions. The decision half is split out of `buildinfo`'s reach so it is testable at all: the dev short-circuit made every existing assertion about this gate vacuous under `go test`. A retroactive marker can only refuse repos that are already broken, and that is pinned against the SHIPPED embed rather than a fixture — stamps at 0.0.385, 0.0.387 and 0.0.395 must not be refused, 0.0.380 must be. One further net guards the half the message text cannot: `migrate` and `help` must stay off the store annotation, or the refusal would block the very commands it names (sty_b36c051c)

## [0.0.395] - 2026-08-02

### Fixed
- **A rebase test documented an invariant the preserve carve-out made false.** `TestRunRebaseLeavesDefaultsVirtual` asserted "workflows/ holds only its README after a rebase" — true when written, and made false when rebase began preserving `workflows/agents.toml` (0.0.393). It still passed only because its fixture never created an agents layer, so the assertion was vacuous on exactly the file that made it wrong. Two costs: it stated a rule that is not the rule, and it was a tripwire on the fixture — the natural act of making the fixture realistic would have failed it for reasons unrelated to what it tests. The expectation is now derived from `rebasePreserve` rather than hardcoded, so a future preserved path lands in it for free, and the shared fixture seeds an authored agents layer. Rebase's two invariants — defaults are not re-seeded, configuration IS preserved — are now asserted over ONE identical starting tree instead of two mutually blind ones, which is how the first came to outlive its own truth. Both were verified to fail independently when broken. Test fidelity only; no product change (sty_ec74ba8f)

## [0.0.394] - 2026-08-02

### Fixed
- **An advisor no longer leaks across route families that share a stage name.** `wfroute.AdvisorsFrom` walked the whole shared step catalogue and keyed advisors by step NAME, but stage names repeat by design — this repo has four `## done` sections, one per route family. So the engineering spine's `advise: retrospective @satelle-lessons`, declared on the `## done` providing `closed`, attached to the `## done` providing `children-resolved` too: a container route claimed an advisor its section never selected, in `satelle workflow show epic-parent` and in the story route document alike. Same defect class as sty_ee0f4ae6 (`topoSortSteps` resolving the shared catalogue by name), and the same remedy: **key off the selected step set, never the catalogue**. The selection half of `BuildRoute` is now exported as `wfdot.SelectSteps` — one answer to "which steps did this category ask for", rather than a second one duplicated in a leaf package — and `AdvisorsFrom` takes `[]Step` instead of a `Catalogue`, so the wrong call cannot be written. Fixed once in the single `RouteSpecFor` chain, so every consumer is corrected together. Every other `Catalogue.Steps` consumer was audited and each is legitimately catalogue-wide (they want the union of skills any route names, not one route's selection); the two least obviously deliberate now say so in a comment (sty_a7316b06)

## [0.0.393] - 2026-08-02

### Fixed
- **`satelle rebase` no longer wipes the agents layer and bricks the repo.** Moving the agents layer into `.satelle/workflows/` (0.0.387) put it inside a directory rebase backs up and WIPES, so a rebase left the repo unable to run: every command refused with "missing .satelle/workflows/agents.toml", and the suggested remedy — `satelle init` — would have reseeded a DEFAULT agents layer over the operator's authored bindings. Meanwhile rebase's own help still promised "satelle.toml/agents.toml … are never touched", which is the clearest statement of the defect: the text was true when written and the move made it a lie. Rebase now PRESERVES it, restoring it from the backup after the wipe (a copy, so the backup remains a complete undo), and the help says so. The distinction is recorded where the carve-out lives: every other file under those dirs has an embedded counterpart, so the read-time overlay heals its absence and wiping it is a reset — the agents layer has none, so wiping it is destruction. A new integration assertion runs a real command AFTER a rebase, which is what the existing tests never did — they all inspected files and none asked the binary to do anything — plus a content-marker guard over the whole class of files rebase must not touch, so the next authored file that moves into a wiped directory is caught (sty_72ccafaa)

## [0.0.392] - 2026-08-02

### Changed
- **`satelle rebase` stops re-seeding the embedded defaults onto disk.** It backed up, wiped, and then copied 25+ default files back — which DEFEATED the reset it exists to perform. Under virtual sparse defaults (sty_29e5a9a5) the known-good default solution IS the empty authored dir: `List`/`Get` overlay the embedded bytes at read time, so the wipe alone completes the reset. Copying them back lands stamped copies that SHADOW the shipped defaults, freezing the repo against the next binary upgrade — exactly the state sty_5604e741 had to delete by hand, with rebase putting it back. `satelle init` already followed this rule; rebase now does too, and the reason is recorded beside init's rule so the next reader finds it. Tasks remain the one carve-out in both paths, because a coded gate checks for an on-disk task header and a task cannot live virtually. The help stops promising a redeploy it does not perform and points at `satelle substrate edit <kind> <name>` for materializing one deliberately; `materializeDefaultSolution`, `materializePrinciples` and `materializeAdvisorySkills` are deleted with their last caller, since leaving them invites exactly the re-wire this removes (sty_cc550a88)

## [0.0.391] - 2026-08-02

### Added
- **`satelle workflow show <category>` renders the DERIVED route.** It used to fail with `docindex: not found`, because `show` only ever resolved document names — so "what route does a story of category X actually get?" had no answer short of deriving it by hand from done.md + step.md. The view prints the obligations in route order with the step discharging each, the performer and rubric, the entry gates WITH the binding each runs under and whether they run serially or concurrently, the always-on gates in scope, and the synthesised park/cancel/recover marked as owned by the binary rather than authored. `--tags` derives the tagged variant; `--category` disambiguates a category that shares a name with a workflow document. Two things are disclosed that were previously silent: which done.md section actually governed (the wildcard answers for every category with no section of its own, which quietly changes the answer), and every gate a `for:` EXCLUDED — an excluded gate is deliberately absent from the Spec, so without naming it a reader cannot tell "does not apply to you" from "nobody declared it".

  Nothing here re-derives anything: the Spec comes from `wfgovern.RouteSpecFor` (a thin export of the chain the engine already used, so there is still exactly one), the step lines come from `wfroute` (the same renderer the story route document uses), and gate scoping asks `wfdot.GateInRoute` — the predicate `BuildRoute` itself asks. A second opinion would be worse than no view: it would look authoritative while disagreeing with what runs. The document view (`show done`, `show step`, `show <workflow>`) is unchanged, and a category with no section and no wildcard reports the governing-section problem rather than a document-index miss (sty_a989764d)

## [0.0.390] - 2026-08-02

### Changed
- **The embedded default route teaches its own format.** `internal/config/substrate/workflows/done.md` and `step.md` are the first process files a new operator ever reads — and the only example they have — but they shipped bare: records with no preamble, no grouping, no provenance. Both now carry a "HOW TO READ THIS FILE" preamble stating the two-halves model and the one rule readers get wrong (**an obligation links to a step by that step's `provides:`, never by its heading** — headings are stage names, and `done` appears three times on purpose), route-family banners grouping the records, and a comment on every record naming the obligation it provides and the sections that select it. Deliberate absences are now labelled `DECISION, not omission:` — no park on containers, no park and an ungated cancel on task runs, no step summary or estimate gate for runs — so a reader can tell a decision from an oversight. The seeded `.satelle/workflows/README.md` gained the same linkage rule, the one-line-comment constraint, and a pointer to `satelle workflow validate`.

  Three regression nets landed BEFORE the prose, so they prove something: a golden derived-route fingerprint per category (comment-only edits must not move a single line of it), a positive presence test that fails on a single un-commented record — a ban-list alone would pass an empty file — and a repo-agnostic lint over the embedded comments, since these ship in the binary and belong to every repo (sty_58911b1a)

## [0.0.389] - 2026-08-02

### Fixed
- **Route construction fails closed on an ambiguous selection instead of silently dropping a step.** `topoSortSteps` keys its `provider`, `index`, `deps` and `state` maps by step NAME, so if a category's obligations ever selected two steps sharing a stage name — one `done.md` edit away, e.g. a list naming both `coded` and `authored`, which selects two `## in_progress` sections — `index[name]` resolved last-wins and one step VANISHED from the route carrying its reviewers with it. Two steps declaring the same `provides:` resolved last-wins the same way. That is exactly the "route that quietly loses a gate" failure the constructor's own header forbids, and it was dormant only because no category happens to co-select today. `BuildRoute` now refuses both, BEFORE the sort (checking afterwards would report on already-corrupted state), and the error names the offending SECTIONS — stage name plus both obligations, since the name alone cannot identify which two to fix. The check is on the SELECTED set, never the catalogue: a catalogue legitimately holds one `## done` per route family, and only co-selection is the defect. `Validate` also reports a duplicate state name as defence in depth, because `Spec` is public and every downstream predicate treats a name as a unique key (sty_ee0f4ae6)

## [0.0.388] - 2026-08-02

### Fixed
- **Integration fixtures resolve a skill the way the binary does, instead of assuming it sits on disk.** Four fixtures read `.satelle/skills/<embedded default>.md` directly, which asserts that an unedited embedded default is materialised in the repo — the opposite of what satelle actually does (virtual sparse defaults, sty_29e5a9a5: init converges on-disk copies but never creates a missing one, and the doc index overlays the default at read time). When this repo's stamped shadow copies were deleted, seven integration tests went red. A single `substrateSkillBody` helper now applies the same precedence the overlay does — an authored copy under `.satelle/skills/` wins, otherwise the embedded default — and fails loudly by name when a skill resolves nowhere, rather than yielding an empty body. Harness only; no production code changed (sty_b6ff0335)

## [0.0.387] - 2026-08-02

### Changed
- **The repo agents layer moves to `.satelle/workflows/agents.toml`**, beside the two route halves it binds. A reader who opened `workflows/` used to see half a route: `step.md` names its performer and gates by SECTION NAME, and what those names actually run — command, transport, tools, model, effort — lived a directory away. `satelle workflow validate` already joined the two to print effective models per gate, which is the proof they are one definition. **No repo is bricked by the move**: an initialized repo with no loadable agents layer deliberately refuses to run, so `config.AgentsPath` prefers the canonical location and falls back to the legacy one, and `satelle init` RELOCATES the file (carrying the operator's content, then format-migrating it) and reports the move. Two traps came with living inside a directory-shaped sync area, and both are closed: the `workflows` walk skips the agents file so it is not pushed a second time under a `workflows/` key, and the `agents` area's server key became `workflows/agents.toml` so a deploy round-trips to the new home instead of pinning every pull to the legacy path. The machine-wide `~/.satelle/agents.toml` profile catalog is a different file, does not move, and still resolves `profile =` inheritance for a binding at either location (sty_10f732ed)

## [0.0.386] - 2026-08-02

### Changed
- **satelle installs no statusLine into a repo's `.claude/settings.json`.** A statusline is an OPERATOR preference — which line the person at the terminal wants to look at — while that file is shared scaffold every session in the repo inherits, and in most repos it is committed. Seeding it there imposed one operator's choice on everyone who opened the repo, silently. `satelle status --line` is untouched; the opt-in home is the operator's own `~/.claude/settings.json`, named in the `agents install` output, the `agents` help and the `--line` flag help, with the snippet composed from the verb so the documented invocation cannot drift. There is deliberately no user-level install verb either: writing to the operator's global file to fix "we wrote to a file we should not have" is the same mistake at a wider scope. A repo seeded before this is HEALED — init strips the entry satelle put there and reports where to re-home it — and a statusLine you own is still left byte-for-byte (sty_325df80c)

## [0.0.385] - 2026-08-02

### Breaking
- **An authored DOT workflow no longer resolves a lifecycle.** A repo whose `.satelle/workflows/` still holds a DOT graph, with no `done.md` + `step.md` route source beside it, has NO route: `satelle doctor` reports UNHEALTHY, `satelle validate` fails, and every story transition is refused rather than run ungated. The refusal is deliberate — falling back to some other lifecycle would silently drop every gate the repo authored (sty_b36c051c)
- **Convert the graph forward — `satelle init` does NOT convert DOT.** Run `satelle migrate` to see what the conversion still owes (it retires the superseded graphs, but never authors a route), and read `satelle help workflow-convert` for the route grammar, the construct-by-construct mapping, and the `satelle workflow show <category>` / `satelle story route <id>` diff that proves no gate vanished (sty_b36c051c)

### Removed
- **`wfdot.Parse` and the DOT-text tooling are gone** — the lexer/emitter tail (`dotBlock`, `dotStatements`, `parseDotAttrs`, `checkNodeAttrs`, `ToDOT`, `parseYAML`, `emitDOT`, `materializeParkFrom`), `format_drift.go`, `refresh.go`, `inert_coded_agent.go`, `warnings.go`, the `satelle workflow format-drift` and `satelle workflow refresh` commands, and the ingest-time `ToDOT` normalisation in `docindex.upsert`. `internal/wfdot` is 1530 → 813 lines, and `BuildRoute` is now the only constructor onto `Spec`. `wfdot.Spec` and every method are untouched — the machinery that reads a lifecycle did not change at all (sty_d953c5d8)
- **The equivalence corpus retires with the format it verified** — `wfequiv/authored_test.go`, `converted_test.go`, `embedded_test.go` and both frozen `testdata/` corpora parsed authored DOT to diff it against the derived route. With no second constructor there is nothing to compare; they did their job at conversion time (sty_9835070d, sty_3795e7f6). `wfequiv.go` and its Spec-vs-Spec unit tests stay (sty_d953c5d8)
- **`satelle-dot-standard`** — the embedded principle described a format the binary no longer reads, which is a lie in every repo that ships it. Replaced by **`satelle-route-standard`**: the canonical authored form, what the binary owns versus what the author declares, and the conventions that survive the cutover — about half the length of the file it replaces (sty_d953c5d8)

### Changed
- **A workflows doc that declares no route REFUSES, loudly.** `wfgovern.SpecFor` returns a NAMED error — "workflow %q declares no route … read `satelle help workflow-convert`" — rather than `ErrNoWorkflow`. That distinction is load-bearing: callers treat `ErrNoWorkflow` as a fresh repo and let the transition through, so collapsing the two would silently drop every gate an unconverted repo authored (sty_d953c5d8)
- **Substrate and docs describe the format that ships.** The embedded `satelle-workflow-advisor`, `satelle-workflow-change-review` and `satelle-story-blocked-triage` skills, the `workflows` / `reviewer-checks` / `agent-dispatch` / `substrate` help topics, `README.md` and the `agents.toml` seed comment all speak the route grammar. The `workflows` topic was rewritten — it also carried a paste defect from an earlier pass. One USER-FACING refusal string still said "source edits are permitted only in DOT states" (sty_d953c5d8)
- The whole integration suite is driven through route fixtures (`tests/routefixture_test.go`): 24 files, 43 tests. Coverage that the cutover makes inexpressible is named where it was removed rather than dropped in silence — the `actor=` keyword rejection, `workflow refresh --apply`, the stamp-overrides-category leg, and per-category `create_review` hook allocation (sty_d953c5d8)

### Fixed
- **An unresolved REVIEWER gate is a WARN again, not a structure failure.** `structure.checkRouteSource` hard-failed on any unnamed skill, which contradicts sty_d59ec6a9 AC4: a repo mid-authoring writes its route before its gate skills, so `satelle workflow create` refused the file and the named `validate` failed. Only an unresolved EXECUTOR rubric fails now — that step cannot be performed, so the story can never reach its terminal state; reviewer gates surface as the WARN that names the consequence (sty_d953c5d8)
- **The web Workflow panel listed nothing for a converted repo.** Both route halves were skipped as "not workflows" and no row replaced them, so a repo's only lifecycle was invisible in the panel it exists to show. The route now heads the list as ONE row, named as `satelle workflow list` names it and expanding through `done.md` (sty_d953c5d8)

## [0.0.384] - 2026-08-01

### Changed
- **The embedded default substrate is a derived route** — `internal/config/substrate/workflows/` ships `done.md` + `step.md`, and the four DOT graphs (`satelle-baseline-workflow`, `satelle-parent-workflow`, `satelle-substrate-workflow`, `satelle-task-workflow`) are gone. The default declaration of done is the most basic story implementation there is — `raised → coded → closed`, with a park and a cancel — not this repo's plan/integration/release spine, which is this repo's process and not every repo's (sty_3795e7f6)
- **One precedence rule for the shipped route (`wfgovern.RouteGoverns`).** The doc index overlays an embedded default wherever a repo has no file of that name, so the shipped halves surface in every repo — including one that never converted. A repo's OWN route governs; the SHIPPED route is order zero and yields to an authored workflow for the categories that workflow claims; an authored graph governs otherwise. Without it, upgrading the binary would have silently re-routed every DOT-only install. `workflow list`, the create stamp, the web panel and `agent validate` all ask the same predicate (sty_3795e7f6)
- **The engine has no by-name lifecycle fallback left.** `const baselineWorkflow` and both `Get("satelle-baseline-workflow")` fallbacks are gone: the order-zero lifecycle arrives through the front door like any other, and `activeWorkflow` returns `ErrNotFound` when no authored workflow applies. `satelle rebase` no longer documents resetting a converted repo as a regression, because redeploying the default now redeploys a route (sty_3795e7f6)

### Removed
- **The embedded `substrate` lane** — a stated decision, not an omission. The lane exists to let a markdown-only change SKIP a heavier lane, and the default now has exactly one working lane to skip, so a second one would only offer a way around the default gates. `satelle-substrate-only-check` still ships for a repo that declares the section itself (sty_3795e7f6)

### Added
- `internal/wfequiv/embedded_test.go` and frozen `testdata/embedded/` snapshots — the same safety net this repo's own conversion got, pointed at the shipped defaults. Four of the five retired category routes reproduce their graph EXACTLY; the two divergences are named beside the assertions that exempt them, including the baseline's `done` node, which carried `agent=reviewer` and therefore read as a PARK state (sty_3795e7f6)
- A fresh-repo integration proof: `satelle init` in an empty dir validates green, stamps `done.md+step.md`, and drives a story `backlog → in_progress → done` through the route's real gates — plus a deterministic source inventory that fails if any non-test Go file reaches for a retired embedded workflow by name (sty_3795e7f6)

## [0.0.383] - 2026-08-01

### Changed
- **This repo's four DOT workflows are retired for one derived route** — `.satelle/workflows/done.md` + `step.md` replace `satelle-project-workflow`, `satelle-parent-workflow`, `satelle-substrate-workflow` and `satelle-task-workflow`. `done.md` declares six category sections (`*`, `epic-parent`, `parent`, `substrate`, `execution`, `task`); `step.md` holds the eleven steps and eleven gates they select from. Deriving obligations from a graph is interpretation, so the conversion is authored and reviewed, with the equivalence checker as the net (sty_9835070d)
- **One front door for the lifecycle.** `wfgovern.SpecFor` decides how a work item's lifecycle resolves — a derived route when the substrate carries one, the governing authored DOT until it does — and every consumer goes through it: the engine's seven gating paths (via `activeSpec`), the verbs, the edit-gate hooks, the web panel. `internal/web`'s own copy of that precedence is deleted; a second front door is the defect sty_085e1a5a just removed (sty_9835070d)
- **`SpecFor` returns an error, not `ok=false`.** `ErrNoWorkflow` is the one "nothing governs this item" case a caller may treat as ungoverned; any other error means a lifecycle EXISTS and does not resolve, which must never read the same way. `reviewerSkills` used to answer a resolution failure with `declared=true, no skills` — it now refuses, because falling through would advance a story past every gate its route declares (sty_9835070d)

### Added
- **`## *` in a declaration of done** — the route-source spelling of a workflow's `applies_to: ["*"]`. `wfdot.ListFor` prefers an exact category section then falls back to the wildcard, the same precedence `OrderedWorkflows` already applies, so the two front doors agree. This repo carries 26 distinct live story categories; enumerating them would have bricked the next new one (sty_9835070d)
- **`for:` on a `## gate` section** — the categories whose route a gate belongs to; omitted means every route. The step catalogue is shared, so without it the four deployment gates (`satelle-ci-published-check`, `satelle-dogfood-check`, `satelle-changelog-entry-check`, `satelle-integration-check`) would have fired on the substrate and task routes, which deliberately carry no version bump, no CI and no release. `applies_to` could not express it — that axis is the story's tags (sty_9835070d)
- **`satelle migrate` retires superseded graphs** — it lists the DOT workflows a parseable `done.md` + `step.md` supersedes, and removes them on `--yes`. It carries NO lifecycle content: baking a repo's process into the binary so migrate could install it is the config-over-code violation the constitution exists to prevent. With no route source present it reports the conversion OUTSTANDING and acts on nothing, on every path including the converged one (sty_9835070d)
- `internal/wfequiv/converted_test.go` and frozen `testdata/authored/` snapshots — the retired graphs are the "before" side, the live route source the "after". Five of the six category routes reproduce their authored graph EXACTLY across path, gates, scoped reviewers and executor rubrics over the full tag matrix; the sixth's one divergence is named beside the assertion it exempts, and anything unnamed fails (sty_9835070d)

### Fixed
- `satelle workflow list` named the embedded baseline `active` while the engine was enforcing a derived route; the route now heads the list with the categories it claims. `satelle workflow show` prints a route source's own grammar summary instead of "(no parseable dot block)". `satelle agent validate` expanded a derived route per category — its `agent=` allocations were being skipped entirely, a fail-open on exactly the checks that command exists for. `structure.checkWorkflow` validates each half against its own grammar, resolves the gate skills it names, and rejects `applies_to` on a route source (sty_9835070d)
- `satelle init` would have re-seeded the four embedded DOT defaults on top of the route that replaced them: it read `applies_to` to decide which categories a repo claimed, and a route source deliberately carries none. It now reads `done.md`'s section headings, and the default solution harvests the rubrics and gates a route names (sty_9835070d)
- The story CREATE gate and the workflow STAMP followed the conversion rather than being lost by it: `create_review` is declared on `done.md` (a lifecycle hook is workflow frontmatter, and the declaration of done is where a create gate belongs), and `WorkflowNameFor` stamps the derived route instead of an authored workflow the engine never consults (sty_9835070d)
- `satelle rebase` still resets a converted repo to the embedded DOT defaults — a real regression of the conversion, not a no-op. Its help now says so and points at the backup it writes and at `restore`, which never touches workflows (sty_9835070d)

## [0.0.382] - 2026-08-01

### Changed
- **The web workflow view renders the ROUTE, not a diagram** — the ordered steps to done, one row each: the status, the obligation it discharges, who performs it under which rubrics, and the reviewers that gate ENTRY, with a tag-scoped gate labelled `by tag …` and a filtered-out one struck through as `needs tag …`. Park and cancel destinations follow as off-route exits, and a step that declares an advisor carries the consult line saying nothing dispatches it. It is the same artifact `satelle story route` renders, through the same `wfroute.Build` — presented, not re-derived (sty_085e1a5a)
- **A story in flight shows its route document** on its detail view — the plan half with the "you are here" marker AND the appended outcome half (each reviewer's verdict, its reasoning, the ledger pointer), expanded rather than folded into the attachment list. A story that has not transitioned shows no route: the live-render fallback stays with the CLI, which owns it (sty_085e1a5a)
- The workflow panel resolves a lifecycle through the DERIVED front door when the substrate carries `done.md` + `step.md`, and the authored DOT until it does — so converting this repo's workflows (order 6) needs no further web change (sty_085e1a5a)

### Removed
- **`internal/web` no longer parses workflows.** `parseWorkflowDOT`, `parseWorkflow`, `parseState` and `workflowDiagram` are gone, and with them `edgeGateLabel`, the local `wfSpec`/`wfState`/`wfTransition`/`bindingVM` types, `inlineField`, `shortSkill` and `shortModel` — every one dead once the diagram went. The inline-YAML `states:`/`transitions:` fallback grammar goes too: that fallback WAS the second parser. `internal/web/workflow.go` falls from 730 lines to 232, and `internal/wfdot` is the one workflow front door (sty_085e1a5a)
- The SVG diagram and its progressive enhancement — pan, zoom, hover-trace, gate-label reveal, the cancel/recovery edge toggle — leave `app.js` (5.9 KB) along with the `.wf-diagram*` / `.wf-edge*` / `.wf-annot` / `.wf-controls` stylesheet, replaced by a `.route*` set built from the existing chip idiom. `TestBrowserWorkflowDiagramInteractive` and `TestBrowserWorkflowDiagramPanZoomToggle` are replaced by `TestBrowserWorkflowRoute` — the old assertions inverted, which is the honest record that the behaviour was removed rather than merely untested (sty_085e1a5a)
- `spineDepths` is retyped to `wfdot.Spec` with its algorithm untouched, so the status lights do not move; `TestWebHoldsNoWorkflowParser` reads the package source and fails if any of the five retired identifiers reappears (sty_085e1a5a)

## [serve-v0.0.14] - 2026-08-01

### Changed
- Carries the route view: the workflow panel renders the ordered route instead of the SVG diagram, and a story detail leads with its route document (sty_085e1a5a)

## [0.0.381] - 2026-08-01

### Added
- **Three constitution clauses** — `.satelle/constitution.md` gains "Route, dispatch, and quality". **No route as judgement**: an agent may COMPOSE a route but never subtract from it, reorder past a pending gate, or satisfy an obligation by assertion — agent freedom is additive only. **Flat dispatch**: the orchestrator is the sole scheduler (`orch → step → orch`); steps never call steps, a reviewer returns a verdict and never triggers remediation, an advisor advises the orchestrator and never the step. **Quality over speed**: a route is never shortened to go faster — the tie-breaker for every argument about what an orchestrator may drop. They extend "no gate as code" along the same axis: that clause forbids deciding a *verdict* in Go, these forbid deciding a *route* in an agent's head (sty_05a5e203)
- **Route advisors** — `wfroute.Advisor` and `AdvisorsFrom`, read from the substrate's `advise` declarations. The route now NAMES who the orchestrator may consult at a step or exit, with a consult line that says nothing dispatches it. That is the re-homing of park triage and the post-close retrospective: a state no longer fires an agent at itself; the route says who to ask, and the orchestrator decides when (sty_05a5e203)

### Removed
- **On-enter dispatch is retired.** `on_enter_agent` / `on_enter_prompt` were LIVE mechanism — `DispatchExecutor` fired a one-shot performer when a node's role agent was empty, executor or reviewer (sty_5cabe26f). Removing it is a removal of working behaviour, not a cleanup, and flat dispatch is why: a state that fires an agent at itself hides work from the one place accountable for the route. Entering a state now dispatches nothing; only a named SPINE `agent=<name>` allocation does (sty_05a5e203)
- Both attributes leave the DOT node set, the parser, the formatter, `wfdot.State`, `Spec.StateOnEnterAgent`, the equivalence checker's per-state comparison, the agent-layer entry-binding validation, the seat's dispatch-agent set, and the machine-wide profile policy keys. An authored `on_enter_agent=` is now an UNKNOWN attribute and fails validation loudly rather than being silently ignored (sty_05a5e203)
- **The equivalence checker's one divergence is resolved by removal.** `on_enter_agent` was the only construct the derived route could not express — deliberately, since a state firing another agent is exactly what flat dispatch forbids. Both representations now agree with nothing left to carve out (sty_05a5e203)
- `TestDispatchOnEnterAgentFromPerforming` is replaced by `TestParkEntryDispatchesNothing` — the same fixture with the assertion inverted, plus a resolver that fails the test if a binding is even looked up. That is the honest record that the behaviour was removed rather than merely untested (sty_05a5e203)

### Changed
- `satelle-story-blocked-triage` and `satelle-lessons` are re-homed as ADVISORS: the skills now say the orchestrator consults them, that nothing dispatches them, and that the advice must be recorded on the story — which is how it reaches the route document's outcome half through the existing single writer, rather than vanishing with the dispatch. The retrospective runs via `satelle story retrospect <id>` after close (sty_05a5e203)
- `satelle help agent-dispatch` states flat dispatch: only a spine `agent=<name>` node dispatches, a reviewer node returns a verdict and dispatches nothing, and an advisor is a declaration the orchestrator acts on (sty_05a5e203)
- Known signal change: `satelle agent validate` now warns that `[blocked-triage]` and `[retrospective]` are orphaned. That is literally true and correct — neither is a dispatch target any more, and the warning already carves out "ok if used by a non-workflow verb". Deleting the bindings would be the wrong fix (sty_05a5e203)

## [0.0.380] - 2026-08-01

### Added
- **`satelle story route <id>`** — a story's route and the reasoning behind every outcome, as ONE artifact. The plan half is the ordered steps between the story and done: each step's obligation, who performs it under which rubrics, and the reviewers gating entry — with a gate present only because the story carries a tag marked as such, and one filtered out for want of that tag recorded as `skipped`, so "no gate" and "gate not for you" stay distinguishable. The outcome half is appended as each step resolves: every reviewer's verdict, its reasoning, and a pointer to the full output on the ledger (sty_39e2d9df)
- **`internal/wfroute`** — the rendering half, a leaf with no store and no `verb` import. Its input is a `wfdot.Spec`, which is blind to its front door, so the AUTHORED DOT and the DERIVED route render the same steps, performers and gates. A test asserts exactly that, on the surface the operator actually reads (sty_39e2d9df)
- **`wfgovern.Refusal`** — engine refusals are structured: the rule that fired (`undeclared-edge`, `skipped-step`, `park-resume-origin`, `structure-guard`), why it fired on this story, and the legal moves it leaves open. When the graph was authored, an operator who hit "that edge is not declared" could open the workflow file; a derived route has no file to open, so the refusal itself has to carry that answer. The structure guard is the one refusal with no alternatives — a broken workflow governs no legal move — and it carries a remedy instead (sty_39e2d9df)
- `wfdot.State.Obligation`, populated by `BuildRoute` from the step's `provides`. Empty for an authored DOT, which has no obligation vocabulary; converting this repo's workflows fills it in (sty_39e2d9df)

### Changed
- The route is written FORWARD, on the same best-effort footing as the change record: the plan half is re-rendered on every transition so the "you are here" marker stays true, and everything after `## Outcomes` is carried verbatim, so history is appended and never rewritten. One doc name, one writer, one read verb — that is how "route and reasoning are one artifact" is enforced structurally rather than by convention (sty_39e2d9df)
- **`type:route` is excluded from the gate docs payload**, like `type:change`. The route grows by one block per step, so injecting it into the gate about to write the NEXT block is circular and quadratic in tokens. It is also unsafe: because the route QUOTES prior verdicts, and a coded check's "notes" are its own script, the integration suite caught an estimate gate matching a literal from its own earlier output and accepting a close it should have rejected. The route now records a bounded excerpt — a pointer, not a transcript — and never reaches a gate's stdin (sty_39e2d9df)
- Legibility is enforced, not aspirational: a test asserts the route renders one line per step within ~10 lines and names no workflow file. If it overflows, the route has become too dynamic to read, and the failing test is the signal (sty_39e2d9df)

## [serve-v0.0.13] - 2026-08-01

### Changed
- Ships the timeline fix from 0.0.376 that had not yet reached a serve release: a `gate_skipped` row renders as **fail**, not pass. That edge advanced with no verdict because its gate skill did not resolve, and it must not read as green (sty_d59ec6a9, released here by sty_39e2d9df)

## [0.0.379] - 2026-08-01

### Added
- **`wfdot.BuildRoute` / `wfdot.ParseRoute`** — the second constructor onto `wfdot.Spec`. `Parse` reads authored DOT text; `ParseRoute` reads a declaration of done (`done.md`) and a step catalogue (`step.md`) and DERIVES the same lifecycle. Every consumer downstream — `Validate`, the reachability predicates, `ScopedReviewersSplit`, `ExecutorSkillsFor`, the engine, the seat, the edit gate — sees a normal `Spec` and cannot tell which front door it came through. That is the point: the graph stops being authored without the machinery that reads it changing at all (sty_4fc3d7f7)
- **The binary now owns topology.** Cancel from every non-terminal step, park from anywhere (including expanding the `from="*"` wildcard into explicit inbound edges), and backward movement to a recovery step are SYNTHESISED, not authored. That is the boilerplate every workflow in this repo repeats identically today. Synthesised edges carry their role state's own gate, because an edge without it silently drops the cancel and park reviewers (sty_4fc3d7f7)
- **Order is derived, never chosen.** Each step declares the obligation it provides and the obligations it requires; the route is the topological sort. An agent picks the SET of steps and never the SEQUENCE. A cycle, a prerequisite nothing provides, or an obligation with no discharging step is a construction error that NAMES the offending thing — a route that cannot be built never degrades into a partial one, because a partial route is a route with gates missing (sty_4fc3d7f7)
- **Concurrent review is the shape's rule, not an opt-in.** Two or more entry gates on a step default to `DefaultParallelCap`, so a route cannot accidentally serialise a gate set by forgetting to declare it. An authored `parallel:` always wins, including an explicit `0` (sty_4fc3d7f7)
- Markdown grammar hand-rolled in the idiom the package already uses — `##` opens a record, `key: value` fills it, CSV for lists. No YAML or TOML dependency added for two small authored files, and every malformed line is an error naming its line number (sty_4fc3d7f7)

### Changed
- The equivalence checker now builds through the production constructor rather than the prototype Go literal, and asserts **zero divergence** against `satelle-project-workflow` — the stronger line the previous story could not take, because there a non-empty report was the deliverable. `internal/wfequiv/obligation.go` is deleted; the prototype's two divergences (`on_enter_agent` on `blocked` and `done`) are now expressible via `on_enter:` and `advise`, so the epic removes on-enter dispatch from one place instead of inheriting a permanent carve-out in the checker (sty_4fc3d7f7)
- No engine, seat, lease or edit-gate code changed. The whole diff is `internal/wfdot/` and `internal/wfequiv/` (sty_4fc3d7f7)

## [0.0.378] - 2026-08-01

### Added
- **`internal/wfequiv`** — a behavioural-equivalence checker for two `wfdot.Spec` values, and the paper check it exists to answer: can a route DERIVED from a declaration of done reproduce satelle's authored DOT? This is the go/no-go for `epic:derived-route`, which proposes retiring `.satelle/workflows/*.md` graphs in favour of `done.md` + `step.md`. Answer: it can. The hand-written obligation list reproduces `satelle-project-workflow` on first transcription, across path, gates, scoped reviewers and executor skills (sty_c6184eaa)
- Equivalence is asserted over **method outputs, not struct fields**. `Spec.States` carries authoring detail whose equality is not required for identical behaviour, so diffing structs would manufacture divergence and make the checker useless as a migration net. What is compared is what `PerformingStates`, `ScopedReviewersSplit`, `ExecutorSkillsFor` and their siblings return — what the engine, the seat and the edit gate actually consume (sty_c6184eaa)
- The comparison runs over a **tag matrix**, not once. Three of the four dimensions are tag-dependent because `applies_to` filters both scoped reviewers and executor augmentations; a single tag-less comparison passes while silently disarming the `surface:ui` design gate. A test asserts exactly that failure mode, because it is the one a one-shot comparison invites (sty_c6184eaa)
- The one divergence found is **`on_enter_agent`** on `blocked` and `done` — inexpressible by design, not by accident, since a state firing another agent is what flat dispatch forbids. It is compared *because* it is live mechanism (`agentstep.DispatchExecutor` resolves it); a checker that only compared what the new shape could express would have returned clean and taught us nothing. Retiring it is a removal of working behaviour and is scoped to its own story, which re-homes both advisors onto the orchestrator (sty_c6184eaa)
- The finding is recorded in `.satelle/documents/derived-route-equivalence.md`, with the machine-checked divergence report goldened at `internal/wfequiv/testdata/project-route.golden` — including what the check does **not** prove (sty_c6184eaa)

## [0.0.377] - 2026-07-31

### Added
- **`satelle service restart`** — cycle the running web service onto the installed binary and verify the result by exe identity. Deliberately narrow: it installs nothing and checks no release. `satelle update` would also do it, but it is a wide instrument that queries GitHub and may install a release, so naming it as the fix for "stale process" told the operator to run a network release check to solve a local process problem. It reuses the same restart path `satelle update` uses — no new systemctl call, no new signal logic, no new identity comparison (sty_a7b2cd3c)
- The verb **fails non-zero when the service is still stale afterwards**. The shared restart path deliberately soft-fails on a non-matching respawn (it reports "could not confirm …" and exits 0, which `satelle update` documents and this change does not alter); a verb invoked specifically to fix a stale process must not report success while the process is still stale (sty_a7b2cd3c)

### Fixed
- **`satelle service status` reported a stale service process and then stopped**, leaving the operator to already know that restarting was the answer. The stale verdict now names its remedy on the same output, in the `→ fix: …` form the init validator established. The remedy lives in the one place the mismatch suffix is produced, so every stale-capable branch inherits it (sty_a7b2cd3c)
- The remedy names a command that **exists** — the gap that motivated the story, where the proposed fix was a `satelle service restart` verb satelle did not ship. A test extracts the printed command from the live stale verdict and resolves it against the command tree, so the message and the surface now fail together (sty_a7b2cd3c)
- A healthy service and one whose identity could not be determined carry no remedy: nothing has been shown to be wrong, and prompting a restart on no evidence would be worse than silence (sty_a7b2cd3c)

### Changed
- `scripts/install.sh` mentions the running service in its upgrade block — and deliberately **informs rather than restarts**. `satelle update` restarts without asking because it is a verb the operator invoked interactively about satelle's own installed state, with a `--no-restart` opt-out. A curl-to-shell script is often non-interactive, may run under provisioning or CI, offers no opt-out the operator saw before piping it to a shell, and one system unit can serve a whole estate — cycling shared always-on infrastructure is a side effect nobody asked for (sty_a7b2cd3c)

## [0.0.376] - 2026-07-31

### Added
- **`satelle init --all`** — heal every registered repo whose deployed scaffolding was invalidated by a binary upgrade. Dry-run by default (following the `satelle migrate --yes` convention rather than inventing a second flag shape); `--yes` applies. It reports each stale repo and its findings before changing anything, preserves authored substrate byte-for-byte, and reports-and-skips a registry path that no longer resolves rather than aborting the run — the registry legitimately carries entries for unmounted volumes and detached checkouts (sty_0f471251)
- Upgrading the binary now says so. `satelle update` prints the estate guidance after an actual CLI replacement — naming `satelle doctor --all` to inspect and `satelle init --all` to heal — and stays quiet for a no-op update, a `--check`, a serve-only refresh, and a `--local` pin. `scripts/install.sh` names **both** cases in its closing block ("a new repo" and "repos you already have"), because installing and upgrading are the same command and the script cannot reliably tell which one it just did (sty_0f471251)
- Why it matters: the staleness is not cosmetic. `satelle workspace add` refuses in a repo with stale scaffolding, which wedges the serve mirror's reconcile loop against those partitions — a downstream failure the operator will not connect back to the upgrade. Healing an estate by hand is N × (`cd` + `satelle init`), which is exactly the chore that gets deferred. Verified live: 8 stale repos → 0, `14 registered — 14 healthy, 0 unhealthy` (sty_0f471251)

### Fixed
- **`satelle doctor --all` could not run from a directory satelle does not govern**, which is precisely where an operator lands after upgrading — so the command the new guidance names would have failed for the people most likely to need it. A store-backed command can now declare that it tolerates an ungoverned working directory when what it reports is not about the current repo; `--all` reports on the workspace registry. Only "not a satelle repo" is tolerated, so any other bootstrap failure still stops the command, and plain `satelle doctor` still refuses there and still names `satelle init` (sty_0f471251)

## [0.0.375] - 2026-07-31

### Fixed
- **`satelle agent validate` and `satelle doctor` could contradict each other about the same workflow, seconds apart, on an unchanged tree.** They ran the same checks over different sources: `agent validate` and `satelle validate` read the SQLite doc index, `doctor` read the authored files. Immediately after editing a workflow — which is exactly when the index lags, and exactly when an author validates — `agent validate` would report a binding orphaned while `doctor` simultaneously reported the node allocating it. Both were right about the source each had read; neither said which source that was, and neither named `satelle reindex` as the recovery. The index-backed checks now read authored files through the same helpers doctor uses, so the two agree by construction (sty_540cfcd3)
- The lag is removed rather than reported: no substrate check consults the doc index any more, so there is no staleness to warn about and no reindex step to prescribe. Measured before and after on a workflow written to disk and not indexed — previously `validate` saw nothing while `doctor` saw the problem; now both see it (sty_540cfcd3)
- The two sets stay deliberately distinct, as doctor has always had them: **allocation** judges the governing set (authored ∪ unshadowed embedded), because an embedded default really does govern a repo that has not overridden it; **consistency** judges only the authored set, because an on-disk wildcard workflow legitimately shadows the embedded wildcard baseline and feeding both to the ambiguity check would report every repo as broken. A naive swap to the authored set alone would have hidden every embedded default (sty_540cfcd3)
- No read-only verb started writing to achieve this. Implicit reindexing was considered and rejected — it would have reintroduced exactly the pattern fixed in 0.0.372, where a command that presents as a diagnostic quietly materialises state (sty_540cfcd3)

## [0.0.374] - 2026-07-31

### Fixed
- **A workflow gate naming a skill that does not exist was silent on both the authoring paths an author actually uses, and silent again at run time when the edge advanced ungated.** The consistency check that catches it was wired only into the whole-set commands: `satelle workflow validate <name>` skipped it (the block was guarded on an empty name filter) and `satelle workflow create` never ran it at all. So an author — human or agent — could write a gated edge, validate the workflow **by name**, get PASS, and drive stories through an edge that was never reviewed, with the DOT still showing the gate. The embedded `satelle-workflow-advisor` skill makes this worse by instructing agents to run exactly the named form (sty_d59ec6a9)
- Both authoring paths now report it, as a **WARN** rather than a FAIL, and name the consequence — not just the missing file: *an edge whose gate skill does not resolve is ADVISORY — it advances UNGATED, with no reviewer and no verdict, until the skill exists*. WARN because a repo mid-authoring writes the workflow before it writes the gate skills; blocking would make the ordinary authoring sequence impossible. `create` still writes the file. The whole-set checks (`satelle validate`, bare `workflow validate`, `agent validate`, `doctor`) keep FAIL and their exact existing output — they answer a different question (sty_d59ec6a9)
- **An ungated advance is now recorded as one.** A transition whose gate skill does not resolve still enacts — the fail-open is deliberate, so a fresh repo works before every gate is authored — but it writes a `gate_skipped` ledger row naming the skill, and prints the same line to stderr. Previously the evidence trail was indistinguishable from an edge that never carried a gate. The new row is its own ledger kind on purpose: folding it into a comment would bury it, and folding it into an accept would assert a judgement that never happened. It renders as an outcome of `skipped`, never green in the timeline, and does not count toward the reviewed-transition progress lights (sty_d59ec6a9)

### Added
- `agentstep.WorkflowSkillProblems` — the per-workflow half of `WorkflowConsistency`, so the whole-set callers and the single-document authoring paths share one definition and cannot drift. The cross-workflow ambiguity check stays whole-set only: it compares repo workflows against each other, so firing it per document would misreport (sty_d59ec6a9)

## [0.0.373] - 2026-07-31

### Added
- **`satelle runtime reap`** — clears the debris a deleted repo leaves behind: its home-keyed runtime plane and its workspace-registry entry, in one action, because an operator cleaning up after a deleted repo wants both gone. A bare invocation **reports and removes nothing**; removal requires `--yes` and acts on exactly the set just reported. Default scope is `stale` (the plane's repo root no longer exists); `--include-unknown` widens to planes with no marker and no registry match; a `linked` plane — one whose repo still exists — is never a target under any flag, and is never even reported as one. Dangling registry entries are collected independently of planes, so an entry whose plane was already removed by hand is still cleared (sty_bd8af0b6)
- Why it matters: registry entries for deleted repos count as `repo.unreadable` forever, so `repos: 21 registered — 6 healthy, 15 unhealthy` — the headline an operator reads to decide whether anything needs attention — was mostly tombstones. After a reap the unhealthy count drops by exactly the number of entries cleared, with no live repo changing state (sty_bd8af0b6)

### Changed
- **The "satelle never deletes" position is narrowed, deliberately: satelle never deletes _implicitly_.** It will delete what it has just reported, when you ask with `--yes`, and only where the repo path does not resolve. The help states the hazard that makes the report worth reading — a path can be absent because a volume is unmounted or a checkout is not yet restored, not only because the repo was deleted (sty_bd8af0b6)
- `satelle runtime list` no longer prints raw `rm -rf` lines for orphans; it names `satelle runtime reap`. `list` itself still deletes nothing. Both verbs now share one collection pass, so they cannot disagree about what an orphan is (sty_bd8af0b6)

## [0.0.372] - 2026-07-31

### Fixed
- **A read-only verb no longer creates durable state for a repo satelle does not govern.** Running any store-backed verb — `status`, `doctor`, `validate`, `story list`, 48 in all — in a directory with no `.satelle/` created `~/.satelle/<repo-key>/` containing `repo.path` and a created-and-migrated `satelle.db`, and *then* failed with "missing .satelle/agents.toml". It paid two writes to produce no answer. `app.Open` now refuses an ungoverned repo with `ErrNotInitialised` **before** `store.Open` and `WriteRepoPathMarker` run, so the cost is zero writes. Exit status for every affected verb is unchanged (1) — the refusal simply arrives earlier, and names `satelle init` instead of an agents.toml file the operator never made (sty_20a7824c)
- This is why a first-ever `satelle init` in an empty repo could report `= …/satelle.db (already present)` — reading as leftover state from a deleted repo, exactly the alarm that must not fire spuriously. init now reports the database as created (`+`) (sty_20a7824c)
- **`satelle service status` no longer recreates a runtime plane for every de-registered repo it lists.** `workspace.Load` is read-only aggregation over registered repos, but opened each repo's database unconditionally — so a registry entry whose `.satelle/` had since been deleted got a fresh plane created on every call. It now reports the repo as un-initialised instead (sty_20a7824c)
- A session hook firing in an ordinary non-satelle directory goes **inert** rather than erroring. `resolveSeat` previously returned "cannot determine engagement — fix config and retry", advice the operator could not act on, after paying for the plane. The other two hook sites already failed open (sty_20a7824c)

### Added
- `config.FindDataDir` — one notion of "this repo is initialised", walking up for the `.satelle` **directory**. It deliberately does not key on `satelle.toml`: zero-config is supported, so an initialised repo may carry `agents.toml` and no toml at all (sty_20a7824c)

## [0.0.371] - 2026-07-31

### Fixed
- **The published `curl … | sh` installer printed `curl: (23) Failure writing output to destination` as its very first line, above satelle's own output, on an install that then succeeded.** Both GitHub release lookups piped curl into a truncating reader (`grep -m1`, `head -1`); the reader exits on its first match, curl takes EPIPE mid-body and reports the write failure. `set -eu` never caught it because a pipeline's status is its last command's, so the install completed correctly and the first thing a new user ever saw from satelle was an error they had to learn to ignore. Both lookups now write the API body to a file under `$tmp` and grep the file — no pipe, no EPIPE (sty_87b5d4bc)
- A genuine transport failure on the release lookup is now *more* legible, not less: nothing is redirected to `/dev/null`, so curl's own diagnostic (`curl: (6) Could not resolve host`, a 404) reaches the operator. Note the exit status for that case changes from `1` to curl's real status (6, 22, …) — the lookup is its own statement now, so `set -e` aborts on it. The two failure paths the script handles itself, an unresolvable latest release and a sha256 mismatch, still exit `1` with their existing messages, and the satelle-serve branch stays soft-fail (warn, keep the working CLI install, exit 0) (sty_87b5d4bc)

### Added
- `tests/install_script_test.go` — hermetic integration coverage that drives the real `scripts/install.sh` against a canned `curl` on `PATH`, asserting the success path emits **nothing** on stderr, that no curl in the script feeds a pipe, and that the transport-failure, empty-tag, sha256-mismatch and serve-404 paths keep their exit codes and artifacts. Verified to fail against the pre-fix script at both defect sites (sty_87b5d4bc)

## [0.0.370] - 2026-07-31

### Fixed
- **`satelle sync` was permanently wedged by a read-only generated view.** The documents pull returned on the first file it could not write, and the cursor save sits after that return — so every later pull re-fetched the same batch and failed on the same file, forever. satelle's own protection for generated views (`0o444`, so nobody hand-edits them) was blocking satelle's own restore. A file that cannot be written is now recorded, reported, and stepped over; the batch completes and the cursor advances (sty_4c3729e7)
- A restored document keeps the mode it had, a new one is created `0o644`, and a view carrying the `generated: satelle` marker is written **read-only** — previously a freshly pulled generated view landed `0o644` and silently lost its protection. `subsync.Restore` replaces a read-only destination rather than refusing it, the same way the doc-index materializer already does for exactly this case (sty_4c3729e7)
- The documents pull no longer re-fetches or rewrites a document whose local bytes already match the manifest. A cursor batch lists what changed since your cursor, not what differs from your disk, so the files a push just uploaded came straight back to be rewritten — which is how the read-only view was being touched at all. The push leg has always compared SHAs before uploading; the pull now does the same on the way down (sty_4c3729e7)
- Callers for which a partial restore is a failure stay loud: `sync config deploy` materialises a whole partition deliberately, and `publish adopt` / `publish update` are single-file, so any unwritten file fails them. Only the incremental documents pull continues past one (sty_4c3729e7)

## [0.0.369] - 2026-07-31

### Fixed
- **`satelle update` could stop installing the serve binary entirely, silently.** Serve-release discovery read a single page of `/releases`; serve releases are rare and CLI releases frequent, so once ~30 CLI releases followed a serve release it fell off that page and update reported `no release tag with prefix serve-v` — a published release, reported as absent. Discovery now walks the release list (100 per page, up to 1000 releases), skips drafts, and distinguishes "not found in the newest N" from "does not exist" (sty_0dcedb0d)
- **A serve release that cannot be resolved now FAILS the verb** instead of printing a skip and exiting 0. That silent exit is how a release read green while the live web service kept running an older serve binary. The one exemption is deliberately narrow: no serve release published anywhere AND no serve binary installed is a no-op, so a fork that has never released one still updates cleanly (sty_0dcedb0d)
- `satelle update` now reports `satelle-serve already up to date (<tag>)` and skips the download when the installed serve binary already is the released one, rather than re-downloading it every run. An unreadable installed version counts as unknown and installs — never as current (sty_0dcedb0d)
- The release workflow decides whether to publish the serve artifact by asking whether the **release** exists, not whether the bare tag does, and tags idempotently — so a run that pushed a tag and then failed can no longer block that serve version's release forever (sty_0dcedb0d)

## [serve-v0.0.12] - 2026-07-31

### Added
- The service re-requests a full snapshot for every partition it renders — once at startup, then every five minutes — so a push that never landed no longer leaves the view stale forever, and renders a `stale · last update <time>` flag on any partition it could not reconcile (sty_e6e467fe)

### Fixed
- An identical re-posted snapshot is applied quietly: freshness only, no row rewrite, no live-update doorbell (sty_e6e467fe)
- The listener binds before the repair loop starts, so its first pass cannot race its own endpoint (sty_e6e467fe)
- Released as 0.0.12 because `satelle update` could not find the previous serve release to install. **Correction (sty_0dcedb0d):** the 0.0.368 notes first attributed this to `serve-v0.0.11` being an unpublished tag. It was published, on 2026-07-27, with its full asset set; serve-release discovery simply read only the first page of `/releases` and ~30 later CLI releases had pushed it off that page (sty_e6e467fe, sty_0dcedb0d)

## [0.0.368] - 2026-07-31

### Added
- **The web service repairs its own mirror.** The UI is a push-fed copy: the CLI posts a snapshot as each mutating verb finishes, and a push that did not land — the service was restarting, down, or unreachable — was lost permanently, leaving the page on its last good frame forever. The serving tier now re-requests a full snapshot for every project it renders: once at startup, which is what repairs the push a `satelle update` restart just swallowed, then every five minutes. It re-requests by running `satelle workspace add` in the repo, so the CLI remains the sole reader of a per-repo database and the mirror remains a read-only view (sty_e6e467fe)
- A view the service could not reconcile now says so: the landing row and the project header carry a **`stale · last update <time>`** flag naming the last confirmed ingest and the `satelle workspace add` remedy. A stale view is never again indistinguishable from a stuck story (sty_e6e467fe)
- `satelle help projects` gains a "When the UI looks stale" section, and `satelle workspace add --help` now states plainly that the verb is the manual re-seed — recovery used to be an undocumented side effect (sty_e6e467fe)

### Changed
- `/ingest/snapshot` fingerprints the body it receives. An identical re-post records freshness only — no row rewrite, no live-update doorbell — so the repair loop never makes an open page re-render on a timer and never rewrites state it does not need to (sty_e6e467fe)
- `SATELLE_SERVER_ENDPOINT` and its `none|off|-` off-switch moved to `internal/config`, so the serving tier honours exactly the same switch as the CLI push path (sty_e6e467fe)

### Fixed
- The mutating-verb path is untouched: the push stays fail-silent under its bounded budget, and no verb waits on UI delivery. CLI-side retry was considered and rejected — it repairs only the failures the CLI survived to notice, and it would put retry latency in every transition (sty_e6e467fe)
- The repair loop refuses a partition whose repo directory is gone, or whose database this machine's `SATELLE_HOME` does not hold, so a service under a foreign home cannot re-seed from an empty store and wipe the view it exists to repair (sty_e6e467fe)
- `satelle serve` binds its listener before starting the repair loop, so the startup pass cannot race its own endpoint (sty_e6e467fe)
- The serve artifact ships as **satelle-serve 0.0.12**, because `satelle update` reported `no release tag with prefix serve-v` and installed no serve binary at all, so the running service could not be dogfooded onto this fix. **Correction (sty_0dcedb0d):** these notes first blamed a stale, unpublished `serve-v0.0.11` tag. That was wrong — `serve-v0.0.11` is a fully published release from 2026-07-27 with 10 assets. The real cause was in `satelle update` itself: serve-release discovery read a single page of `/releases`, and the frequent CLI release cadence had carried the serve release past it (sty_e6e467fe, sty_0dcedb0d)

## [0.0.367] - 2026-07-31

### Added
- **`satelle doctor`** — one diagnostic surface answering whether satelle is ready to govern a repository. It COMPOSES the validators that already own their rules (the agents layer and its machine-wide profile references, workflow structure, cross-workflow consistency, lifecycle-hook allocations, reviewer ceilings, required binaries, harness scaffolding) rather than adding rules of its own, so init, doctor, and the engagement precondition can never form three different opinions about the same repo (sty_e9da28e2)
- `satelle doctor --all` checks every registered workspace repository **independently**: each root is contained, and an unreadable or uninitialised one becomes its own one-finding report rather than aborting the sweep. Output ends with a healthy/unhealthy tally and the exit code is the worst result across all of them (sty_e9da28e2)
- `satelle doctor --live` adds bounded, opt-in provider probes — a `--version` call for a command binding, a single `initialize` handshake for an ACP one. Neither opens a session or sends a prompt, so **ordinary doctor performs no paid and no network model call at all**. Every probe runs in its own process group and is killed and reaped on the deadline or on cancellation, so a probe never leaves a provider process behind. Authentication is diagnosed only where the provider says so; an unexplained failure is reported as a spawn failure rather than guessed at (sty_e9da28e2)
- **`internal/health`** — the shared finding vocabulary: a stable identifier, severity, and remediation per defect. The identifier is the contract, so the same defect reports the same id whether you meet it in `doctor`, in `satelle init`, or in an engagement refusal (sty_e9da28e2)
- `satelle doctor --json` emits repos, findings, grants with per-field sources, gate and hook allocations, a summary, and the exit code. Exit codes are documented in both the help and the payload: `0` healthy (warnings allowed), `1` error findings, `2` doctor could not run (sty_e9da28e2)
- `satelle help doctor` documents severity, exit codes, the `--live` side effect, and the distinction between repo workflow POLICY (`.satelle/`) and machine-wide EXECUTION profiles (`~/.satelle/agents.toml`) (sty_e9da28e2)

### Changed
- `satelle init`'s deployment validation is now a PRINTER over `doctor.Check`, not a second check list. Its FAIL/WARN lines carry the finding identifier alongside the artifact. The only check init still owns is the substrate analysis of what init itself just wrote (sty_e9da28e2)
- `satelle service status` reports each registered repository's readiness — `N registered — H healthy, U unhealthy`, naming each unhealthy repo and its first finding — so an unhealthy repo is visible from the service surface instead of looking identical to a ready one. It stays informational: the service itself is running fine. The diagnostic lives on the CLI side deliberately, because satelle-serve is a push-fed mirror that never opens a repo database (sty_e9da28e2)
- An engagement refusal is rendered from the shared vocabulary, so the identifiers and remediation it carries are the ones `satelle doctor` prints for the same repo. Fail-closed behaviour is unchanged (sty_e9da28e2)
- `agentvalidate` reports `Findings` additively: `Problems`/`Warnings` are now derived from them, each finding's detail being the exact prose that always appeared, so the two surfaces cannot drift. Identifiers are assigned by the producing check, never by matching message text (sty_e9da28e2)
- `satelle agent validate` and `satelle doctor` render per-field provenance through one function, so the two displays cannot diverge (sty_e9da28e2)

### Fixed
- Doctor judges the workflows that actually GOVERN a repo — authored files plus the embedded defaults none of them shadows. Reading only the authored directory left a repo governed by an embedded default with no allocation or lifecycle-hook checking at all, reported healthy without the governing workflow ever being looked at. The consistency (ambiguity) check still sees only authored files, because an on-disk wildcard workflow legitimately overrides the embedded wildcard baseline (sty_e9da28e2)
- A missing agent executable is a WARNING, not a failure. Shipping it as an error refused `satelle init` on any machine without the provider CLI on PATH — including CI. A repo is legitimately initialised before its CLI exists; the gates stay inert until one is, and dispatch already refuses at the moment it matters. A MALFORMED command (one whose first token is a placeholder) stays an error: no environment can make it work (sty_e9da28e2)
- Environment values are never printed, in any mode including `--json`. Doctor lists env KEY names with whether each resolved; an unresolved `${VAR}` names the key, never its contents (sty_e9da28e2)

## [0.0.366] - 2026-07-30

### Added
- **Lifecycle hooks are declarable substrate.** A workflow can now declare an operation that fires OUTSIDE the status graph — story creation today — naming both the skill that judges it and the logical agent that runs that skill. Previously create review resolved a skill from `create_review:` frontmatter and ran it against an EMPTY agent selector, which the engine silently resolved to `[reviewer]`; nothing in the substrate could inspect or change that allocation, and nothing validated it (sty_ede16f51)
- One generic grammar, in the new stdlib-only `internal/wfhook`: `hooks:` with `operation`, `skill`, and an optional `agent`. Go's entire per-operation knowledge is a table of which operations exist and which yield a verdict — no provider, model, effort, command, or tool grant appears anywhere on the hook path, and keys like `model:` on a hook are refused. A future lifecycle operation is a name in that table plus its call site, not a new parser, resolver, or validator (sty_ede16f51)
- `satelle workflow show <name>` renders one workflow's identity, graph shape, and each hook's full allocation: operation, skill, logical agent, the machine-wide profile or local binding it resolves through, interface, model, effort, permission ceiling, and source files. Read-only and deliberately tolerant — an unresolvable allocation is marked UNRESOLVED rather than failing, because diagnosing a misconfiguration is what the command is for (sty_ede16f51)
- `satelle agent validate` prints a `HOOK` allocation line per declared hook, carrying how it was declared, and no longer reports a binding a hook allocates as orphaned (sty_ede16f51)
- `satelle help workflows` gains a "Four things a workflow governs" section distinguishing transition nodes/edges, lifecycle hooks, deterministic structure checks, and agent judgments — where each is declared, when it fires, and who decides (sty_ede16f51)

### Changed
- The scalar `create_review: <skill>` form remains fully supported as the documented shorthand: it resolves to the same hook with `agent = reviewer`, but as a DECLARED default with provenance rather than an empty string falling through `gateBinding`. Every shipped workflow — this repo's five and the four embedded defaults — is left on the shorthand, which is the compatibility proof (sty_ede16f51)
- Validation refuses a bad hook before it can run, split by ownership so neither check reports the other's findings twice: `workflow validate` owns the DECLARATION (unresolved skill, unknown operation, missing skill, an operation declared both ways), `agent validate` owns the ALLOCATION (missing binding, non-reviewer role on a verdict hook, an in-loop binding that cannot produce an isolated verdict). `satelle agent validate` runs both (sty_ede16f51)
- `satelle init` seeds hook skills through the same parser, so a `hooks:`-declared skill travels with the default solution — the previous scalar-only read would have missed it (sty_ede16f51)

### Fixed
- Hooks are checked even when a workflow's DOT block does not parse. They are frontmatter, so an unparseable graph must not hide a broken allocation (sty_ede16f51)
- The reviewer permission ceiling is judged in exactly one place. A first cut re-checked it per hook, which turned an intentionally-advisory heuristic into a hard failure and rejected every workflow's shorthand hook in any repo whose reviewer template the heuristic cannot classify. `checkBinding` remains the single owner, keeping its settled severity split: a provable escape (a Codex danger sandbox) is a hard problem, an unexpressed ceiling stays a warning (sty_ede16f51)

## [0.0.365] - 2026-07-30

### Added
- **Machine-wide agent profiles.** An operator running satelle across several repositories can now define each provider's execution binding once, in a profile catalog at `~/.satelle/agents.toml`, instead of restating it in every repo. A profile carries the full `AgentBinding` execution surface — role, interface, command, model, effort, tools, timeout, principles, env, settings, secondary — and may extend another profile (sty_c7dfeedf)
- A repo consumes a profile only by naming it: `profile = "<name>"` on a binding in `.satelle/agents.toml`. Anything the repo states inline still wins, and `env`/`settings` merge key-wise with the repo's key winning. `role` is identity, not an override — a repo declaring a role that contradicts the profile's is refused rather than silently resolved either way (sty_c7dfeedf)
- `satelle agent profiles` lists the catalog; `satelle agent validate` now reports every effective field with the tier that supplied it — `repo`, `profile:<name>`, `global-role:<name>`, or `embedded` — so an operator sees not only what will run but where it was authored. `env`/`settings` lines name the field and its source only; values may be secrets and are never printed (sty_c7dfeedf)
- `satelle agent migrate` seeds a starter catalog from the machine's selected `[agent] cli`. It is opt-in, never automatic, never overwrites an existing catalog, and writes nothing into any repository. The catalog it seeds leaves `[roles]` commented out, so it changes nothing until a repo opts in (sty_c7dfeedf)
- `satelle help global-agents` documents the file, the precedence ladder, and the boundary (sty_c7dfeedf)

### Changed
- Resolution has **one** site — `config.LoadEffectiveAgents` — which every surface (bootstrap, `agent validate`, `init` deployment validation, the process view) now calls. Two independently-merging call sites was the defect worth designing against (sty_c7dfeedf)
- The `${VAR}` KV is layered: the catalog's `[vars]` is the machine-wide base and a repo's own `[vars]` (with its gitignored `satelle.local.toml` overlay) win per key. Expansion still happens in memory at dispatch wiring, so a machine-wide secret referenced as `${NAME}` reaches the agent process without ever being written into a repository (sty_c7dfeedf)
- Reviewer ceiling, interface, context-channel and workflow-allocation checks all run against the **merged** binding, so a profile cannot smuggle a capability past a check by supplying it machine-wide (sty_c7dfeedf)

### Fixed
- Nothing changes for an existing installation. With no catalog on the machine — including a repo relying on `~/.satelle/config.toml [agent] cli` — every repo resolves exactly as before, and a repo with no `profile=` anywhere is untouched by whatever an operator later adds to the catalog. In particular there is **no implicit same-name merge**: a profile called `reviewer` and a repo `[reviewer]` that never mentions it do not combine (sty_c7dfeedf)
- The catalog is execution configuration only, enforced mechanically rather than by convention: a profile carrying `applies_to`, `skill`, `prompt`, `on`, `output_*` or any other policy key is refused at load, and `LoadGlobalAgents` reads three sections and nothing else — workflows and skills dropped into the machine-wide home are invisible to workflow discovery (sty_c7dfeedf)

## [0.0.364] - 2026-07-30

### Changed
- The planner benchmark is now a **controlled study** rather than a race. `tests/plannerbench` answers three questions independently — transport at a fixed provider/model, provider-or-model at a fixed interface, and isolated dispatch versus the in-loop executor — and a comparison is readable only when every dimension it declares held is identical across sides. Anything else is reported as confounded (naming the differing dimension), underpowered, or skipped-with-a-reason, and yields no conclusion. There is no code path that names a provider winner from unmatched cells (sty_115eec5c)
- The study is **data**, not Go: `study.json` declares bindings, comparisons (`free_variable` + `holds`), context bands, seed and sample minimum. Adding a provider or a question is a JSON edit, and nothing in the report or classifier names a provider (sty_115eec5c)
- Fixtures are **seeded source trees** under `testdata/fixtures/<name>/tree/` — real multi-package Go with a `cmd/` entry point, declared symbols and an existing `_test.go` — plus per-criterion `expected_seams` as the oracle's ground truth. The previous synthetic fixtures were title/body/acceptance rows over an empty repo, which also made the read-only fidelity check vacuous: `productDigest` over an empty directory could not fail no matter what the agent did (sty_115eec5c)
- Samples are scheduled as a full work list and **shuffled** under the recorded seed, so run order is randomized across cells instead of nested per binding, where the binding that ran last absorbed every warm-cache and rate-limit effect of the study. Each sample records its global `run_order`, and percentiles use nearest-rank with no interpolation — at n=3 interpolation invents a value no sample produced (sty_115eec5c)
- `report.md`/`report.json` are pure: records in, report out, no clock. `make planner-report` regenerates a byte-identical report from the durable per-sample files without spending a token. The closing binding-change recommendation is gated — it may only fire from a supported comparison whose p50 gap clears the study's declared threshold, never from a collection-mixed one (sty_115eec5c)

### Fixed
- **The artifact score was the transition validator.** `scorePlan` called `agentartifact.ValidateAll` — the same function the gate runs — so the score was true exactly when the run committed and carried no independent quality signal. The oracle is now independent (this package no longer imports `agentartifact` at all, asserted structurally by parsing its own imports): it scores substance against the seeded tree — a file that exists, a symbol the tree actually declares, and a test named in the same section as one of those hits. The literal string `AC<n>` contributes nothing, so a plan that labels every criterion and reaches no seam scores zero and is flagged `label_only`. A committed run can now score low, and a refused run is still scored from the body recovered out of the executor log (sty_115eec5c)
- **Unreported usage could surface as an available zero, and only one attempt was counted.** Token totals came from the first regex match in the ledger text even though the repair/escalate policy makes up to three attempts, and a `TOTAL 0` cost row flipped usage to available. Usage is now summed across every `agent-attempt` event, the numeric fields are pointers that marshal away entirely unless some attempt reported usage, and `satelle story cost` is a cross-check that can never decide availability. A genuinely reported zero stays available with `tokens_total: 0` — reported-zero and unreported are different facts (sty_115eec5c)
- **Failure class came from substring scans of combined output.** `strings.Contains(out, "auth")` matched "author" and "authoritative", routing a quality failure into an infrastructure exit. Classification now reads only typed values: the engine's `agent-failure.outcome`, the attempt event's `validator_ok` and phase, the process exit status, and the harness's own `exec.LookPath` and digest facts. `malformed_output` and `denied_mutation` are deliberately not infrastructure — a model that answered badly, or a performer that wrote when it should not have, are results worth counting. The text-matched `auth` class is removed rather than reimplemented; auth failures surface as `exit_status` with the message preserved in `diagnostics.detail` (sty_115eec5c)
- **The failure-mode tests asserted the class literal they were handed**, which proved nothing about classification. All nine classes are now exercised through the real path — an actual `satelle story set --status plan` transition against a stub agent script — covering none, exit_status, signal_killed, timeout, malformed_output, denied_mutation, spawn, setup and attachment. Those runs spend no tokens, so they are permanent hermetic coverage rather than an opt-in extra (sty_115eec5c)
- **The benchmark bindings granted `Bash(satelle:*)` and called it read-only**, so the study did not measure the tool policy the planner ships with. Bindings now default to the grant read from the repo's own `[planner]` section, and a binding that diverges must declare its own policy name and a reason; both `shipped-planner-grant` and the legacy `read-only` label are reserved against it. Because the report holds `tool_policy` as a dimension, a divergent binding cannot be compared against a shipped-grant one — so the default study's cross-provider comparison is confounded by construction, and says so (sty_115eec5c)
- Topology is a first-class variable, so an in-loop session's richer progressive interaction is no longer attributable to whichever provider happened to run in-loop. In-loop samples cannot be spawned by a test (the in-loop executor *is* the driving session), so they are ingested as operator attestations and must carry the same dimensions and the same accounting — interventions, conversation state, visible progress — as an instrumented sample, or they are refused. Topology conclusions are always labelled collection-mixed and never justify a default change (sty_115eec5c)

## [0.0.363] - 2026-07-30

### Fixed
- Unit tests no longer read the operator's real `/etc/systemd/system/satelle.service`. `systemUnitPath()` hardcoded that absolute path and no test redirected it, so `go test ./...` passed or failed depending on whether the developer happened to have a satelle system unit installed — and six tests in `internal/cli` went red the moment one was. GitHub runners have none, so CI would have stayed green while every developer with a real install saw red: CI and the developer machine silently testing different things (sty_d50218d1)
- The path now resolves through a package-level `systemUnitDir` that hermetic tests redirect to a temp dir, the same idiom `restartHooks` and `procRoot` already use. Production is unchanged: the default is the real location, nothing outside tests assigns it, and there is deliberately no environment or flag knob for a test-only need (sty_d50218d1)
- The six tests were repaired by isolating their environment, not by relaxing assertions — each still asserts exactly what it did before. `TestSystemUnitPathIsInjected` guards the isolation itself (empty `HOME` so the user unit cannot mask the system one), so a revert to a hardcoded path fails the suite (sty_d50218d1)

### Added
- The project workflow now carries four coded deployment gates, one per objective, so a failed deployment names the objective that failed: `satelle-build-unit-check` (go build + go test, on entry to integration — nothing ran the unit suite in the workflow before this), `satelle-integration-check` (make integration, plus an assertion that the operator's installed binary and running service are byte-identical before and after), `satelle-ci-published-check` (the test and release workflows concluded success for the actual HEAD SHA, and the tag for .version is published), and `satelle-dogfood-check` (the installed CLI reports .version and the serving process runs the installed binary under a persistent supervisor). All four VERIFY and never PERFORM — no gate pushes, tags, updates, installs or restarts. Substrate only; no binary change (sty_7a2dc74b)

## [0.0.362] - 2026-07-30

### Fixed
- **Regression in 0.0.361:** `satelle update`'s bus-free restart sent SIGTERM first and escalated to SIGKILL. systemd classifies SIGTERM as a CLEAN exit, so a `Restart=on-failure` unit did not respawn from it — it STOPPED, permanently, and the escalation could not recover it because the process was already gone. Any user on a bus-unreachable host whose unit predates 0.0.361 (every user unit satelle wrote before then) lost their service on update. **Upgrade from 0.0.361.** (sty_d45618d5)
- The signal is now DERIVED from the unit's `Restart=` policy, read from the unit file on disk with no bus: `always` → SIGTERM, `on-failure` → SIGKILL (the only signal systemd counts as a failure, hence the only one that unit respawns from). A signal is sent only when its effect on that policy is established (sty_d45618d5)
- Blind escalation is removed rather than reordered: exactly one signal is sent, ever. A policy that never respawns (`Restart=no`, absent, an unestablished conditional like `on-abnormal`) or an unreadable unit file means NO signal at all — the process is left running and the output says why, because stopping a service nothing will restart is strictly worse than leaving it stale (sty_d45618d5)

### Changed
- One reader now serves every unit-file directive the update path consults (`installedUnitFile` + `unitDirective`), so exe identity and the Restart policy can never be read from different files or parsed by different scanners (sty_d45618d5)
- The test fake now models systemd's documented signal semantics instead of the author's expectation — an on-failure unit that receives SIGTERM goes down and stays down in the suite. The 0.0.361 defect was invisible because its fake respawned on SIGKILL regardless; a fake cannot falsify a belief about the system it stands in for (sty_d45618d5)

## [0.0.361] - 2026-07-30

### Fixed
- `satelle update` now cycles the live service onto the new binary **without a reachable systemd bus**, instead of printing two systemctl commands that cannot work on the affected host. A supervisor respawns its own child from the unit file autonomously — D-Bus is only how an *external* actor requests a restart — so update signals the stale process and lets the supervisor do it. Graceful stop first, escalating to a forceful one only when a legacy `Restart=on-failure` unit ignores it (sty_f20f3f3b)
- Success is confirmed from kernel facts — the pid now holding the port, its exe identity against the installed binary, and that its parent is still the same supervisor — never from the signal's exit code. A cycle that does not converge makes `satelle update` exit non-zero with what it observed, so it can no longer report an installed-and-live release it did not verify (sty_f20f3f3b)
- Releases no longer require the operator to restart the service by hand. Three release stories (sty_acd4b61e, sty_4e6f0788, sty_87c0ef37) were delayed or parked by this; the last was parked at `release` with a green, published, CI-verified build and every check passing except the live footer (sty_f20f3f3b)

### Changed
- A process under **no persistent supervisor** is now deliberately left running rather than signalled — nothing would respawn it, so stopping it would be strictly worse than leaving it stale. `satelle update` says so and names the one-time `satelle service install --system` fix. A systemd *user* manager without lingering counts as non-persistent, because it dies with the login session (sty_f20f3f3b)
- The per-user unit written by `satelle service install` is now `Restart=always` (was `on-failure`) so a graceful stop respawns it. Units already on disk keep their policy until re-installed — adopting a new one needs `daemon-reload`, which needs the bus this path exists to avoid — and are handled by the escalation instead (sty_f20f3f3b)
- `.satelle/skills/release.md` no longer instructs the operator to cycle the service. `check_live_footer` is now a verification of what `satelle update` already did, the manual commands are demoted to the unsupervised case, and the section states that a per-release hand-restart is a defect to raise rather than a step to follow (sty_f20f3f3b)

## [0.0.360] - 2026-07-30

### Added
- `satelle agent validate` now JUDGES the role-vs-grant contract it previously only displayed. A binding a workflow allocates to a DISPATCHED performer (a spine `agent=<name>` node, or `on_enter_agent=`) whose `tools` grant carries no context channel is now an ERROR naming the binding, the allocating node, and both fixes — surfaced before a story is engaged, instead of as a dispatch refusal mid-transition (sty_87c0ef37)
- A `role = "reviewer"` binding that grants shell is now a WARNING (exit 0). Reviewers are fed their documents in the transition payload and never reach the dispatch path that consults a grant, so the shell is capability that is never exercised. Stated as the mechanical fact — an unused grant — not as a prohibition; whether a repo keeps it stays the repo's call (sty_87c0ef37)
- `satelle help agent-dispatch` states the contract positively and once, under "What each role needs": performers PULL their context and so need one of two channels (`Bash(satelle:*)` or `read_file`); reviewers are PUSHED theirs and need none. Previously the rule was only inferable from a refusal message you had to trigger first (sty_87c0ef37)

### Changed
- One predicate now owns "does this grant carry a context channel": `config.GrantsContextChannel`. The private `agentstep.grantsSatelleCLI` is deleted and its three dispatch call sites retargeted, so the runtime refusal and the validate error cannot disagree about any grant string. It carries its own quote-stripping tokenizer, so a quoted TOML token is judged identically on both paths (sty_87c0ef37)
- `agentstep.isInLoopCommand` likewise folded into `config.IsInLoopCommand` rather than left as a second copy alongside it (sty_87c0ef37)
- Engagement now refuses one transition EARLIER for an under-granted performer. `agentvalidate` is the shared authority for `satelle agent validate`, `satelle init`, and leaving the workflow entry state, so a workflow allocating a performer whose grant has no context channel is refused at engage rather than at the dispatch it protects — no agent is spawned. Same condition, same predicate, reported sooner (sty_87c0ef37)

### Fixed
- The `agent-dispatch` help topic no longer claims satelle's grant check "expects Claude-shaped tools for dispatch" — that contradicted `read_file` being an accepted channel, and would have sent a Grok-native repo hunting for a `Bash(satelle:*)` it does not need (sty_87c0ef37)

## [0.0.359] - 2026-07-30

### Added
- `satelle status --line` renders one statusline row — a live server link plus the engaged `<story_id>::<stage>` — for a terminal status area. It is strictly read-only: the engaged story comes from the `Leases.List` seat lookup, never `satelle story seat`, which reaps stale leases (writes) and would thrash lease state under Claude's 300ms debounce. It never fails loudly: any error yields one degraded, well-formed line and exit 0, so a statusline can never show a stack trace (sty_4e6f0788)
- `satelle agents install claude|all` now installs a `statusLine` entry into `.claude/settings.json` running that renderer. Claude allows exactly one statusLine and offers no composition, so a statusLine the operator already owns is left byte-for-byte, the install still succeeds, and the exact snippet to fold satelle's segment into their own script is printed instead. `satelle agents remove` prunes only a satelle-owned entry (sty_4e6f0788)
- The link degrades honestly: it is a real OSC 8 hyperlink only on terminals known to render one (iTerm2, WezTerm, Kitty) and plain text everywhere else, so Terminal.app never receives raw escape bytes; and only a service that actually answers is linked at all — a dead one renders unlinked and says so (sty_4e6f0788)

### Changed
- Grok and Codex intentionally get no statusline, and the reason is now recorded in the tree (`satelle agents` help and the Claude statusline source): Grok has no scriptable statusline, and Codex's built-in `[tui].status_line` takes a fixed item list with no command backing. Both harnesses see the same facts through the SessionStart availability line (sty_4e6f0788)

## [0.0.358] - 2026-07-30

### Changed
- `satelle status` now reports the web service as a URL plus its real answering state (`live` / `not answering`), probed via the existing `healthzOK` seam, replacing the `web port <n>` config echo — a value that read like confirmation but could not fail (sty_fb5e6d96)
- The global service port is now resolved through one path (`servicePortResolved`, which `servicePort` delegates to) for every surface that states a user-facing URL, so `satelle status` and the session-start line can never name different ports for the same machine state (sty_fb5e6d96)

### Added
- `satelle hook context` injects one line naming the web service URL and whether anything is answering on it, so a new session learns where the server is without asking the agent. It reaches Claude, Grok and Codex through the SessionStart wiring all three scaffolds already carry — no per-harness config change. An unreadable global config renders as `availability unknown`, never a fabricated `live`, and the hook stays fail-open (sty_fb5e6d96)

## [0.0.357] - 2026-07-30

### Fixed
- `satelle service status` now derives its verdict from the unit file on disk, cgroup/listening-port process discovery, and exe identity (shared with `satelle update`'s restart path), instead of collapsing a failed or empty `systemctl --user is-active` query into "inactive (not installed or stopped)" — the exact false negative that wrongly parked two release stories as "world-not-ready" on a WSL host whose user-session D-Bus was unreachable (sty_acd4b61e)
- `.satelle/skills/release.md`'s dogfood triad now names `satelle service status` as bus-independent evidence for `check_persistent_supervisor` and `check_live_footer`, so both checks are satisfiable on a host with no reachable user bus (sty_acd4b61e)

## [0.0.356] - 2026-07-30

### Fixed
- Exe-identity extraction now compiles on Windows via a platform-specific helper (`syscall.Stat_t` does not exist there); the v0.0.355 release failed its cross-compile matrix on this exact line and never published a tag (sty_c344d080)

## [0.0.355] - 2026-07-30

### Fixed
- `satelle update`'s restart step now verifies the live process is running the newly installed binary by exe device+inode identity, instead of trusting a restart command's exit code or a version string that may not have changed (sty_c344d080)
- When neither the systemd user nor system bus is reachable, the running service is now located via cgroup and listening-port inspection so a stale-but-running process is never reported identically to "no service configured" (sty_c344d080)
- A start-limited (restart-exhausted) systemd unit is now named explicitly with a `reset-failed` remedy, instead of falling through to a generic "no restartable service was found" message (sty_c344d080)

## [0.0.354] - 2026-07-30

### Fixed
- satelle-code-ac-review now judges a single primary objective and falsifies prohibition/invariant-shaped acceptance criteria by enumerating the paths that could violate them, instead of accepting on the presence of one compliant sample (sty_02acce1b)
- satelle-reviewer-objective-audit now audits reviewer skills for evidentiary underreach (a presence-only bar unable to falsify a prohibition) alongside its existing overreach checks (sty_02acce1b)

## [0.0.353] - 2026-07-30

### Fixed
- Planner benchmark runs now preserve schema-versioned redacted final results, attached artifacts, hashes, timestamps, and structured diagnostics in durable per-run evidence (sty_dec87e60)
- Artifact scoring now parses the attached document body and reports explicit findings per acceptance criterion instead of relying on a fragile aggregate substring oracle (sty_dec87e60)
- Unreported benchmark usage is now represented as unavailable with provenance and omitted token totals rather than ambiguous numeric zero (sty_dec87e60)

### Changed
- Infrastructure and under-sample failures now fail the live benchmark harness while artifact-quality findings remain auditable benchmark data, with hermetic coverage for failure classes and interruption retention (sty_dec87e60)

## [0.0.352] - 2026-07-30

### Added
- Contracted isolated steps can opt into bounded validate–repair–escalate policies through skill frontmatter, including efforts, escalation bindings, attempt limits, token/time budgets, and fail-on-exhaust handling (sty_b5c082ac)
- Every quality attempt records provider-neutral binding, model, effort, duration, usage availability, validator findings, and escalation reason telemetry (sty_b5c082ac)

### Changed
- Structured artifact validation now reports all deterministic findings for targeted repair, attaches only the final valid candidate, and distinguishes unreported usage from measured zero (sty_b5c082ac)
- Agent-dispatch help documents policy defaults, rate-limit failover separation, budget behavior, cancellation, and attempt evidence (sty_b5c082ac)

## [0.0.351] - 2026-07-30

### Added
- Skills can declare generic typed output contracts that Satelle decodes, validates, and attaches before committing a dispatched workflow transition (sty_52dbbf69)
- Command and ACP final responses share one structured artifact decoder with field-level validation and optional acceptance-criterion coverage (sty_52dbbf69)

### Changed
- Contracted planners can run with read-only repository tools and return JSON artifacts, while legacy self-attaching steps retain their existing migration-compatible path (sty_52dbbf69)
- Agent-dispatch help documents structured output contracts, canonical envelopes, transactional refusal, and legacy migration (sty_52dbbf69)

## [0.0.350] - 2026-07-30

### Added
- Provider-neutral progressive execution events now cover lifecycle, heartbeats, safe messages, tool activity, artifact candidates, usage, completion, and failure across command and ACP agents (sty_f926256c)
- Named dispatches now write normalized live diagnostics, with an explicit opt-in raw transport trace that filters hidden reasoning and redacts credential-shaped values (sty_f926256c)

### Changed
- Interactive isolated-agent progress and silence-aware heartbeats now stream on stderr while structured stdout and the authoritative final agent response remain unchanged (sty_f926256c)
- Agent-dispatch help documents normalized diagnostics and the `SATELLE_AGENT_TRACE_RAW` troubleshooting opt-in (sty_f926256c)

## [0.0.349] - 2026-07-30

### Fixed
- Claude, Grok, and Codex PreToolUse wrappers now return coherent structured denials with actionable reasons instead of mixing JSON stdout with exit 2 and empty stderr (sty_56cda59c)
- Hook infrastructure and malformed-output failures now use safe harness-specific fallback denials without leaking captured stderr, while irrelevant Bash remains fail-open (sty_56cda59c)

### Changed
- Agent-dispatch help documents each harness deny envelope and the distinction between structured exit-zero handling and exit-two stderr fallback (sty_56cda59c)

## [0.0.348] - 2026-07-29

### Added
- Opt-in Claude command vs Grok ACP planner benchmark with representative fixtures and wall-time, usage, artifact, policy, and failure-observability evidence (sty_a3526df6)

### Fixed
- Workflow edit hooks now derive source-edit permission from executor-owned DOT states, deny driving-session work-ahead during transitions, and preserve exact isolated-performer dispatches across Claude, Grok, and Codex (sty_a3526df6)
- In-tree shell mutations are governed consistently through edit and commit hooks, while read-only preflight and Satelle artifact recording remain available (sty_a3526df6)

### Changed
- Planner transport evidence retains Claude command as the repository default because the one-run ACP pilot did not meet the predeclared artifact and observability threshold (sty_a3526df6)

## [0.0.347] - 2026-07-29

### Fixed
- Codex ACP no longer calls authenticate for CLI-owned api-key/chat-gpt methods; session reuse follows operator `codex login` (sty_71491143)
- Agent-dispatch help documents CLI-owned auth for live Codex dogfood instead of SATELLE_CODEX_DOGFOOD (sty_71491143)

## [0.0.346] - 2026-07-29

### Fixed
- Codex compliance hooks now use documented tool matchers and explicit synchronous command handlers; local hook verification uses Codex CLI login and proves SessionStart plus no-story PreToolUse denial (sty_71491143)
- Codex install/remove verification now covers all generated hook ownership and the engaged-story allow path (sty_71491143)

### Changed
- Codex help and binding guidance no longer require Satelle-specific credential or opt-in environment variables (sty_71491143)

## [0.0.345] - 2026-07-29

### Added
- `satelle agents install|remove` creates/reconciles harness compliance scaffolds (`.claude/settings.json`, `.grok/hooks/satelle.json`, `.codex/hooks.json`) with blocking PreToolUse hooks, in addition to launchers (sty_9e86f407)
- Codex harness support: `.codex/hooks.json`, `hook gate|commitgate --harness codex`, init `--harness codex` (sty_9e86f407)
- Hermetic installed-launcher ACP suite and opt-in `make codex-smoke` / `tests/codexlive` (sty_9e86f407)

### Fixed
- Codex ACP install binding no longer appends unsupported `stdio` subcommand; generated command is `sh <launcher>` → `npx -y @agentclientprotocol/codex-acp` (sty_9e86f407)
- `bashCommandFromEvent` accepts Codex shell `command` as a JSON argv array (sty_9e86f407)

### Changed
- Agent-dispatch help and README document three-harness compliance, ownership boundaries, and live smoke (sty_9e86f407)
- Re-run `satelle init` (or `agents install`) after upgrade: `satelle-hook.sh` now forwards `--harness` (scaffold drift until healed)

## [0.0.344] - 2026-07-29

### Fixed
- Codex ACP no longer receives the Grok-only `--reasoning-effort` argv flag; effort rides `session/set_config_option` (Grok ACP argv injection preserved) (sty_aa726901)
- Command-transport Codex forwards binding `effort` via `-c model_reasoning_effort="{effort}"` (empty drops the flag) (sty_aa726901)
- Agent validate hard-rejects `role=reviewer` Codex *command* templates whose sandbox is not read-only (`workspace-write` or omitted), not only danger modes (sty_aa726901)

### Added
- Hermetic Codex-shaped ACP fixtures for init, model/effort config, CaptureAnswer/Full, mutation deny, Satelle-only grant contract (sty_aa726901)
- `satelle agents install|remove` for `claude`/`grok`/`codex`/`all` — satelle-owned launchers under `$SATELLE_HOME/agents/bin/` without changing the default reviewer (sty_aa726901)

### Changed
- `buildArgs` supports fused `{model}`/`{effort}`/`{settings}` placeholders (exact-token behaviour unchanged for `{system}`/`{payload}`) (sty_aa726901)
- Agent-dispatch help documents effort per transport, Codex sandbox hard-reject, and `satelle agents` vs `satelle agent` (sty_aa726901)

## [0.0.343] - 2026-07-29

### Added
- Codex as a first-class agent transport: preferred ACP spawn (`DefaultCodexACPCommand` via `@agentclientprotocol/codex-acp`) and secondary `codex exec` template (`DefaultCodexExecCommand`); dogfood practice in `satelle help agent-dispatch` (sty_3b4909bb)

### Changed
- `NewRunner("codex")` maps to the exec command template (unmapped stub removed); migrate expands bare `command = "codex"` (sty_3b4909bb)
- Agent validate treats Codex `-s read-only` as reviewer ceiling evidence and hard-rejects `danger-full-access` / `--dangerously-bypass-approvals-and-sandbox` for role=reviewer (sty_3b4909bb)

## [0.0.342] - 2026-07-29

### Fixed
- Seat-refusal message names `satelle story stop-request` for a LIVE seat and `satelle story seat release` only for a STALE seat, so agents no longer cancel a healthy story to free the engagement seat (sty_7b69954a)

### Changed
- Operator preemption is a named park reason on the existing `blocked` state (`preempted-by:<id>` free-form tag) — no new `hold` lifecycle state (sty_7b69954a)
- `satelle-recognise-blockage` covers preemption as a separate case from blockage and forbids cancel-to-free-seat (sty_7b69954a)
- `satelle-story-cancel-review` hard-rejects seat-contention justifications; `satelle-story-blocked-review` accepts preemption parks (sty_7b69954a)

## [0.0.341] - 2026-07-28

### Added
- `satelle-executor-deliverables` principle promoted to the embedded system default (ondemand residency): executors are told required outcome artifacts, never gate-judging criteria; clean-init discoverability covered by an integration test (sty_fd4c3466)

### Changed
- Repo-local `.satelle/principles/satelle-executor-deliverables.md` removed so the embedded default is the single source (same shape as other binary-only principles); no second copy remains to diverge (sty_fd4c3466)

## [0.0.340] - 2026-07-28

### Fixed
- Coded-check gate nodes no longer advertise a live `agent=` binding: `format-drift` reports `inert_coded_check_agent` when a scoped node (or non-default edge agent) names a skill with a ```check fence, and `workflow refresh` strips/rewrites it consultatively; embedded baseline `estimate` and substrate `subcheck` omit `agent=`; `structure.CheckCommand` is the single coded-check definition (`skillCheck` delegates) (sty_4cebc624 / epic:coded-check-gate-honesty)

## [0.0.339] - 2026-07-28

### Fixed
- ACP dispatch no longer persists pre-tool agent narration into step summaries and other prose artifacts: capture segments on `tool_call`/`tool_call_update` and keeps the last `agent_message` run by default; verdict path keeps full capture so parseDecision still finds a decision before trailing chatter (sty_844b6ab1)

## [0.0.338] - 2026-07-27

### Changed
- `satelle-story-classification`: `order:<N>` is the position in the sprint only (not per-epic / per-grouping); non-durable; epic members keep consecutive sprint slots; `epic-parent` carries no order (sty_0656d3c6)

## [0.0.337] - 2026-07-27

### Added
- Step-summary and scoped judge nodes may allocate a named `role=reviewer` binding (`agent=<name>`); `Summarise` uses that binding's harness/model (default remains `[reviewer]`) so high-frequency step recaps can run on a cheap Grok agent (sty_8ee40f94)

### Fixed
- Named judge agents on the step-summary node or scoped `on=` gates no longer misclassify as performing lifecycle states, executor augmentations, or phantom `from="*"` park edges (sty_8ee40f94)

## [serve-v0.0.11] - 2026-07-27

### Fixed
- Workflow diagram footnote routing treats the step-summary skill (not only literal `agent=reviewer`) so a cheap named summariser stays off the main flow (sty_8ee40f94)

## [0.0.336] - 2026-07-27

### Fixed
- Substrate close gate no longer requires a git commit: unions recorded change set, live worktree with opt-in `--include-substrate`, and commits naming the story; rejects empty sets and uncommitted non-substrate paths (sty_6469025e)

### Changed
- `satelle story diff --include-substrate` opt-in mtime leg for authored dirs + data dir; default live path stays git-only for scope-review (sty_6469025e)
- Substrate workflow prose no longer mandates commit+push when `.satelle/` is git-ignored (sty_6469025e)

## [0.0.335] - 2026-07-27

### Added
- Enacted status transitions record a `change_record` ledger row (paths/counts) and optional type:change patch attachment; `satelle story diff --recorded` unions the file lists; substrate-only-check prefers recorded files over git log (sty_948ad5df)

### Changed
- Gate payloads exclude type:change attachments so large patches do not starve plan/step-summary under the docs ceiling (sty_948ad5df)

## [0.0.334] - 2026-07-26

### Changed
- Work-state push is incremental: a per-(server, project, repo) cursor outside the repo sends only records changed since the last successful push; the silent 2,000-item ceiling is gone; story-less ledger rows are included; `--full` re-sends everything (sty_88e83180)
- Config and documents push skip unchanged content via server manifest sha comparison, reporting skipped-without-upload separately from server-side unchanged (sty_88e83180)

## [0.0.333] - 2026-07-26

### Removed
- Embedded `code.md` executor rubric no longer ships: it named lifecycle states and gates the baseline does not define, and claimed the generic word `code` in every repo's skill namespace (sty_01f49dd5)

### Changed
- Dispatched-step contract (fresh stdin payload, pull-by-id, never advance status) lives in `satelle-agent-model`; `satelle-dot-standard` examples mark `@skill:code` as repo-authored; this repo's `.satelle/skills/code.md` matches in-loop `agent=executor` (sty_01f49dd5)
- New embed self-consistency test rejects embedded prose that names non-shipping states/gates as though they ship (sty_01f49dd5)

## [0.0.332] - 2026-07-26

### Changed
- UserPromptSubmit injection replaces the static create-and-engage reminder when a live engagement seat has a forward DOT advance: the engaged form names the story id, status, next gate(s), and `satelle story set` command, all derived from the governing workflow via `wfdot.AdvanceOptions` (terminal/park/back-edge filtered; multi-Msquare cancel sinks do not silence the spine) (sty_e16a2cd7)

## [0.0.331] - 2026-07-26

### Fixed
- Hook handlers (`gate`, `commitgate`, `prompt`) refresh `heartbeat_at` on a live owner-held engagement seat so a long `in_progress` step no longer drops the seat after HeartbeatTTL; stale and foreign-owner leases are never revived, and heartbeat write failures are fail-open (sty_3bb1d8be)

## [0.0.330] - 2026-07-26

### Fixed
- `satelle.toml` joins the config push bundle as the `settings` area so rehydrate can restore `[sync]` scopes; `[hosted] project` is redacted at push and re-applied from the local binding at deploy so another repo cannot be rebound; `satelle.local.toml` remains excluded by the unconditional `.local` rule (sty_ea18294f)

## [0.0.329] - 2026-07-26

### Security
- Unconditional `.local` exclusion on every outbound file bundle: config push, documents push, `satelle publish`, and hosted backup never transmit a path whose name carries a `.local` segment (satelle.local.toml, notes.local.md, …). Applied at bundle assembly so a new sync area cannot bypass it; dry-run and `sync scopes` report withheld paths (sty_698e70b6)

## [0.0.328] - 2026-07-25

### Added
- Controlled story **category** vocabulary: default list ships as embedded data (`internal/config/substrate/config/categories.toml`), not a Go literal; satelle.toml `[categories]` may extend (`extra`) or replace (`vocabulary`) (sty_b2315e17)
- Enforcement phase `off | warn | reject` (default **warn** so upgrades never hard-break); reject fails create/set in verb; warn is a CLI stderr advisory (web/MCP get casing + reject only — no warning channel in this slice) (sty_b2315e17)
- Case-insensitive category ↔ `applies_to` matching in workflow resolution (`containsStr` uses EqualFold) so a legal-but-differently-cased value never silently resolves no workflow (sty_b2315e17)

### Changed
- Synonym collapses in the shipped default: bug/bugfix/defect → `fix`; infra → `infrastructure`; frontend/web/ui/cli are not categories (use `surface:` + a TYPE category) (sty_b2315e17)

## [0.0.328] - 2026-07-25

### Added
- Controlled story **category** vocabulary: default list ships as embedded data (`internal/config/substrate/config/categories.toml`), not a Go literal; satelle.toml `[categories]` may extend (`extra`) or replace (`vocabulary`) (sty_b2315e17)
- Enforcement phase `off | warn | reject` (default **warn** so upgrades never hard-break); reject fails create/set in verb; warn is a CLI stderr advisory (web/MCP get casing + reject only — no warning channel in this slice) (sty_b2315e17)
- Case-insensitive category ↔ `applies_to` matching in workflow resolution (`containsStr` uses EqualFold) so a legal-but-differently-cased value never silently resolves no workflow (sty_b2315e17)

### Changed
- Synonym collapses in the shipped default: bug/bugfix/defect → `fix`; infra → `infrastructure`; frontend/web/ui/cli are not categories (use `surface:` + a TYPE category) (sty_b2315e17)

## [0.0.327] - 2026-07-25

### Added
- Create-path notice when `[review] gate_create` is undefined (pre-seed scaffold): stories/tasks file without create review and the one-line enable fix is named; explicit `gate_create = false` silences; never auto-rewrites config (sty_d4d0ee59)
- `[review] gate_create` joins `scaffoldConfigDefaults` so init/validate advisories cover the same seed as the create notice (sty_d4d0ee59)

## [0.0.326] - 2026-07-25

### Fixed
- Create-review premise example is repo-agnostic (`<path>:<symbol>`) so v0.0.325's leaked satelle-internal illustration does not ship in the binary (sty_b9692e1f)

## [0.0.325] - 2026-07-25

### Changed
- Create gate gains premise falsification: reject story rationales that assert checkable facts about this repo contradicted by named evidence; opinion/value/priority never reject grounds (sty_b9692e1f)

## [0.0.324] - 2026-07-25

### Fixed
- `satelle agent validate` no longer warns that a named `role=reviewer` binding is a perform-only allocation — that contradicted live gate NODE lines after per-gate agent= (sty_6ab016dc)

## [0.0.323] - 2026-07-25

### Fixed
- Named gate bindings (`agent=<name>` on a gated edge) run that binding's harness, not the default `[reviewer]` runner (sty_68dafd5f)

## [0.0.322] - 2026-07-25

### Added
- Controlled `cancel-reason` tag namespace (repo config via `[tags.vocabulary]`) so cancelled stories are queryable by why they closed — shelved, duplicate, superseded, withdrawn, wrong (sty_de8e8e2c)

### Changed
- Cancel gate is category-aware: parent/epic-parent cancels accept only when every child is done or cancelled (mirrors done-review); conditional `cancel-reason` tag expectation when the repo declares the namespace; cancelled is terminal and revival is a new story tagged `supersedes:<id>` (sty_de8e8e2c)

## [0.0.321] - 2026-07-25

### Fixed
- ACP model set_config_option: hard-fail only on explicit model rejection; Method not found is a soft miss so peers without the method still run (sty_a476a2f8 follow-up)

## [0.0.320] - 2026-07-25

### Changed
- Workflows name agents, never models: DOT `model=` is superseded (warn + strip on refresh); gated edges and scoped reviewers allocate any `role=reviewer` binding via `agent=<name>`; ACP surfaces peer rejection of `session/set_config_option` model (sty_a476a2f8)

### Added
- Integration coverage for named gate bindings, role-mismatch refusal, and model= strip (sty_a476a2f8)

## [0.0.319] - 2026-07-25

### Changed
- Optimise embedded substrate for token economy, focus, and repo-agnostic fitness: promote satelle-skill-naming; strip dogfood sty_/tsk_ ids; language-agnostic code.md; description budgets ≤60 for skills/workflows; single-source binding and bundling doctrine; scope:system on embedded defaults; sprint index form is a repo choice (sty_24365c69)
- Downstream: `satelle update` then `satelle restore` to pick up new defaults (seeded copies with a stale embedded_sha are not auto-healed) (sty_24365c69)

## [0.0.318] - 2026-07-25

### Added
- Property-based conformance suite over `config.EmbeddedDefaults()` (Tier 1): structure/OKF, scope manifest, legal tags, description ceilings, dogfood-id ratchet, banned stories paths, reviewer accept/reject + verdict blocks (sty_6830e78e)
- Embedded wikilink + workflow `@skill:` resolution via wfdot, with justified allow-lists and negative fixtures (sty_6830e78e)
- Generalised ```check fence extractor `structure.CheckFence` and integration golden tables for every coded-check skill (sty_6830e78e)
- Gate-wiring integration tests (reviewer stub accept/reject, status-unchanged) and discovery/waiver map for embedded workflow gates (sty_6830e78e)
- Opt-in LLM judgment suite under `tests/llm/` (`//go:build llm`, `make judgment`) with labelled fixtures and best-of-three rule (sty_6830e78e)

### Changed
- Retired brittle substring pins subsumed by Tiers 1–3 from `substrate_okf_test`, `embed_principle_test`, `principle_consistency_test`; deleted `substrate_path_test.go`; survivors comment the non-tabular property (sty_6830e78e)
- README Testing table documents judgment cost and hermetic default path (sty_6830e78e)

## [0.0.317] - 2026-07-22

### Fixed
- Stories tab backlog and engaged chips share one chip style and baseline; engaged label is number-first ("N engaged") to match backlog (sty_c7ab5180)
- TestRepoReviewerModelIsActive skips when `.satelle/agents.toml` is absent so CI is not red after operator-owned `.satelle/` (sty_c7ab5180, follow-up to sty_91a390a0)

## [serve-v0.0.10] - 2026-07-22

### Fixed
- Same backlog/engaged chip alignment and number-first labels as CLI 0.0.317 (sty_c7ab5180)

## [0.0.316] - 2026-07-22

### Fixed
- Stories tab backlog and engaged chips share one chip style and baseline; engaged label is number-first ("N engaged") to match backlog (sty_c7ab5180) — tag not cut (CI red on pre-existing agents.toml pin)

## [0.0.315] - 2026-07-22

### Changed
- Project tab headings: no link underline in any state; slight bold on hover (weight 500) under selected bold (600) with bold-ghost width reserve so the row does not reflow (sty_e4632f45)
- Stories tab active accent bar runs continuously under the engaged chip (cluster owns the border); chip hidden entirely when engagement count is 0, with live fragment insert/remove on 0↔n (sty_e4632f45)

## [serve-v0.0.9] - 2026-07-22

### Changed
- Same tab-heading polish and hide engaged-0 chip as CLI 0.0.315 (sty_e4632f45)

## [0.0.314] - 2026-07-22

### Added
- Root AGENTS.md: drive engaged stories/epics to a terminal status; dogfood as you progress; prefer `satelle help` for process detail (sty_c0de8674)

## [0.0.313] - 2026-07-22

### Fixed
- Project Stories tab "engaged 1" chip no longer nests a story link inside the tab anchor (invalid HTML that floated the chip between Stories and Tasks); badge is a tab-cluster sibling with CSS under `.tabs .n-engaged` (sty_dd9396d4)

## [serve-v0.0.8] - 2026-07-22

### Fixed
- Same engaged-chip nested-anchor layout fix as CLI 0.0.313 (sty_dd9396d4)

## [0.0.312] - 2026-07-22

### Fixed
- PreToolUse hook commands use an absolute path to satelle-hook.sh so a drifted agent shell cwd can no longer brick Bash/Edit with "No such file"; init/heal upgrades relative form; wrapper probes CLAUDE_PROJECT_DIR/SATELLE_PROJECT_DIR for binary resolution (sty_57582675)

## [0.0.311] - 2026-07-22

### Fixed
- Bash fence no longer treats fd-duplication redirects (`2>&1`, `>&2`, `n>&-`) as file targets, so allowed cross-repo `satelle story|task` commands with stderr merge are not falsely denied (sty_74c0556f)

## [0.0.310] - 2026-07-22

### Changed
- Local web client favicon matches satelle.dev animated ◐ monogram (SMIL terminator + reduced-motion fallback, brand green) (sty_2b1af84b)

## [serve-v0.0.7] - 2026-07-22

### Changed
- Same satelle.dev-aligned favicon monogram as CLI 0.0.310 (sty_2b1af84b)

## [0.0.309] - 2026-07-21

### Added
- Project Stories tab always shows live story-engagement count (including 0); soft-refreshes via `fragment/engagement` without full page reload (sty_01ba9482)

## [serve-v0.0.6] - 2026-07-21

### Added
- Same always-visible engagement count chrome as CLI 0.0.309 (sty_01ba9482)

## [0.0.308] - 2026-07-21

### Changed
- All agent bindings in `.satelle/agents.toml` set `effort = "high"` (reasoning/thinking) by default (sty_a82c3ed0)

## [0.0.307] - 2026-07-21

### Added
- Workspace landing shows Stories (with backlog badge), Tasks, Workflow, and Documents counts matching project tabs; `GET /fragment/projects` for soft-refresh without full-page reload (sty_f968f9db)

## [serve-v0.0.5] - 2026-07-21

### Added
- Same workspace landing counts + soft-refresh as CLI 0.0.307 (sty_f968f9db)

## [0.0.306] - 2026-07-21

### Changed
- Embedded `satelle-story-cancel-review`: supersede claims must name delivering story/commit and verify ACs; sibling bundling is an explicit reject with stop-and-surface (sty_9d7be832 / epic:scope-integrity)

## [0.0.305] - 2026-07-21

### Fixed
- Init/rebase seed all CSV edge skills (scope-review + workflow-change on baseline close), not only the first skill in the list (sty_814ad29a CI)

## [0.0.304] - 2026-07-21

### Added
- Embedded `satelle-story-scope-review` on implementation exit (baseline close + project in_progress→integration) using engagement baseline / story diff (sty_814ad29a / epic:scope-integrity)

## [0.0.303] - 2026-07-21

### Added
- Engagement baseline ledgered on first performing-state entry; `satelle story diff` enumerates changes since baseline (incl. untracked) for scope gates — report only, no pass/fail (sty_da169e03 / epic:scope-integrity)

## [0.0.302] - 2026-07-21

### Added
- Workflow panel diagram and Transitions list show full multi-reviewer CSV gates and parallel= concurrent marker (sty_1b7a0ca2)

## [serve-v0.0.4] - 2026-07-21

### Added
- Same workflow multi-reviewer / parallel diagram UX as CLI 0.0.302 (sty_1b7a0ca2)

## [0.0.301] - 2026-07-20

### Added
- Opt-in concurrent multi-reviewer gates: edge `parallel=true`/`parallel=N` runs CSV reviewers concurrently with no short-circuit; multi-reject errors name every rejecter; ledger records all verdicts (sty_4f0a15db)
- Plan gate dogfood: plan→in_progress runs plan-review + architecture + integration-coverage with `parallel=true` (sty_4f0a15db)

### Changed
- Default multi-reviewer behaviour remains sequential first-reject short-circuit unless `parallel=` is set

## [0.0.300] - 2026-07-20

### Added
- Workflow binding-form knowledge: edge CSV vs scoped on=, over-fire trap, list-order short-circuit in `satelle help workflows`; advisor check 7; init/workflows README pointers (sty_9882b8c6)
- `satelle workflow validate` WARN for single-state scoped reviewers that re-fire on rework inbound edges (non-fatal; sty_9882b8c6)
- Embedded `satelle-workflow-change-review` gate (n/a fast-accept when slice touches no workflow file), bound as CSV edge reviewer on baseline close and project in_progress→integration (sty_9882b8c6)

### Changed
- Planner binding: Claude/fable via command transport (plans only; Bash(satelle:*) for attach)

### Fixed
- Help topic reviewer-checks: paste defect jammed validate/done-gate prose mid DOT bullet (sty_46c584b1 dogfood)

## [0.0.299] - 2026-07-20

### Fixed
- Nav topbar no longer shows an unexplained **mirror** mode pill; empty-identity RO UI omits the strip, and identity email keeps a read-only/CLI-fed accessible description (sty_eea989dd)

## [serve-v0.0.3] - 2026-07-20

### Fixed
- Same topbar mirror-pill UX fix as CLI 0.0.299 (sty_eea989dd)

## [0.0.298] - 2026-07-20

### Verified
- Operator system unit ExecStart migrated to satelle-serve; live footer brands satelle-serve 0.0.2 (sty_2c51cc2c)

## [0.0.297] - 2026-07-20

### Verified
- Large-repo mirror STATUS/PROGRESS dogfood: live mutator matches CLI within ≤1s under 0.0.296-generation serve; original stale badge classified as (a) stale binary/unit (sty_3a76a5fa)

## [0.0.291] - 2026-07-20

### Removed
- Parallel epic path and `[gate] allow_parallel` opt-out — single performing story always enforced; deleted parallel workflows/skills/launcher (sty_a614a0ea)

## [0.0.296] - 2026-07-20

### Verified
- effort= field closed for sprint:7 order:3 (sty_657f77b9; shipped 0.0.294)

## [0.0.295] - 2026-07-20

### Verified
- Sprint 7 agent failover + effort fields closed on co-shipped 0.0.294 (sty_5bf61f89, sty_657f77b9)

## [0.0.294] - 2026-07-20

### Added
- Engagement seat recovery: `story set --status <same>` re-acquires a dropped seat on a performing story; edit-gate deny distinguishes dropped-seat vs no story (sty_4f74d01f / sprint:7)
- Agents.toml `effort=` (reasoning/thinking) with `{effort}` placeholder + ACP injection (sty_657f77b9)
- Agents.toml `secondary=` / `[defaults] secondary` — one retry on rate-limit/unavailable (sty_5bf61f89)

## [0.0.293] - 2026-07-20

### Verified
- Mirror partition lifecycle on operator host: junk `002`/`sse-abort*` partitions pruned; landing is satelle + solidsafe-engine-v0 + solidsafe-ui-v0 only (sty_eb61be02)

## [0.0.292] - 2026-07-20

### Added
- Mirror partition lifecycle: `POST /ingest/remove`, `satelle workspace partitions` / `workspace prune`, `workspace remove` purges the partition (sty_eb61be02 / epic:mirror-hygiene)
- Serve `X-Satelle-Instance` on `/healthz`; `SATELLE_SERVER_ENDPOINT` env (`none` disables discovery+push) (sty_5aa08259)

### Fixed
- Hermetic integration tests no longer seed the operator's live `:8787` mirror via auto-probe (sty_5aa08259)
- `workspace add` with no matching serve registers and exits 0 with seed skipped (was non-zero)

## [0.0.290] - 2026-07-20

### Added
- Opt-in `[gate.command_allow]` step policy: restrict git subcommands (e.g. push) to permitted story statuses via satelle.toml; commitgate enforces when configured (sty_c21490cc)

## [serve-v0.0.2] - 2026-07-20

### Changed
- Footer brands the running artifact via `buildinfo.Name` (`satelle` vs `satelle-serve`); Makefile and release.yml stamp Name per binary (sty_4a5c6924)
- `make check-serve-version` fails closed when serve-path sources change without advancing `satelle-serve.version`; release skill runs it on serve-path slices (sty_4a5c6924)

## [0.0.289] - 2026-07-20

### Fixed
- Serve landing slug uniqueness: snapshot ingest returns 409 when two repo_keys share a directory basename; landing/crumbs fall back to repo_key for legacy collisions so hrefs stay unique (sty_57d5ce25)

## [0.0.288] - 2026-07-20

### Changed
- `satelle workspace add` bootstraps `[server] endpoint` into `satelle.local.toml` when a local serve answers the service port, then seeds; fails non-zero with exact remedy when seed cannot proceed (sty_0122610a / epic:serve-adoption)
- `satelle init` qualifies registry-only `workspace: member` when endpoint is unset; help/README state the local.toml endpoint prerequisite

## [0.0.287] - 2026-07-20

### Changed
- README documents serve-adoption surface: workspace add, satelle-serve, independent versions, CI vs local tests; retired ui push named as removed (sty_1fef9026)

## [0.0.286] - 2026-07-20

### Fixed
- `satelle update` still refreshes satelle-serve from serve-v* when the CLI is already current (sty_19ff03f4)

## [0.0.285] - 2026-07-20

### Added
- Independent version keys in `.version`: `satelle.version` (CLI) and `satelle-serve.version` (serve); release tags `vX` (latest) and `serve-vY` (not latest) (sty_19ff03f4 / epic:serve-adoption)

### Changed
- Makefile stamps each main with its own version; release.yml / install.sh / `satelle update` resolve CLI and serve releases independently
- Changelog gate requires both CLI and serve-v headers when serve version is set; live footer dogfood greps serve version

## [serve-v0.0.1] - 2026-07-20

### Added
- First independent satelle-serve version line (sty_19ff03f4) — tag `serve-v0.0.1`, assets `satelle-serve-v0.0.1-*`

## [0.0.284] - 2026-07-20

### Added
- Dedicated `satelle-serve` binary (`cmd/satelle-serve`) for the push-fed mirror UI; Makefile, install.sh, release, and `satelle update` ship both artifacts (sty_80233c10 / epic:serve-adoption)
- `satelle service install` prefers sibling `satelle-serve` (or `--serve-bin`); falls back to `satelle serve` with a notice

### Changed
- Live verb-dispatch web.Server removed from `internal/web` — package is mirror-only (link isolation for the serve binary)
- `satelle serve` is a deprecated alias that shares `internal/serve.Run` with the dedicated binary

### Fixed
- `ledger.EventTelemetry` owns display extraction so web need not import verb

## [0.0.283] - 2026-07-20

### Breaking
- `satelle ui` / `satelle ui push` removed — use `satelle workspace add` (register + seed mirror). Invoking the old spelling prints the replacement (sty_805bee9c / epic:serve-adoption)

### Added
- `satelle workspace add` seeds the push-fed serve mirror when `[server] endpoint` is set; register-only notice when unset (sty_805bee9c)
- `satelle init` prints agent-readable `workspace: member` / `workspace: not-member` lines (sty_805bee9c)

### Changed
- README, projects help, and empty-mirror landing copy point at `workspace add` instead of `ui push`

## [0.0.282] - 2026-07-20

### Fixed
- CLI UI push drain: mutating verbs deliver change + snapshot before process exit (no fire-and-forget race against store close); coalesced one snapshot per invocation; black-holed endpoint bounded to 1.5s and fail-silent (sty_9ba3d709 / epic:serve-adoption)

## [0.0.281] - 2026-07-19

### Added
- Push-fed mirror UI parity: full snapshot kinds (docs with mod_time/provenance/source, ledger, seats, identity, settings, story docs) and template-rendered project/workspace pages on the mirror serve (sty_400c022b / epic:mirror-ui-parity)

### Changed
- Serve templates use absolute `/static/` assets so CSS/JS resolve under `/r/{slug}/` base href
- Browser and integration e2e re-enabled against the push-fed mirror (seed via ui push)

## [0.0.280] - 2026-07-19

### Added
- Push-fed read-only serve mirror (`~/.satelle/serve/mirror.db` per repo-key) with `/ingest/change` and `/ingest/snapshot` (sty_dbdadfa0)
- `satelle ui push` full-state reconcile to the local UI server; auto-snapshot on CLI mutations when `[server]` is set (sty_1dde0d47)

### Changed
- `satelle serve` is a single process with no supervisor children, no repo DB open, no fingerprint poller or maintenance loops
- Service unit WorkingDirectory is `$HOME` (not a single repo) for the push-fed server (sty_455f0d6e)

### Fixed
- Integration suite covers mirror render, SSE, restart restore, and non-ingest POST rejection

## [0.0.279] - 2026-07-18

### Added
- CLI change publisher: optional `[server] endpoint` fire-and-forget POSTs on the ChangeNotifier seam; init scaffolds the commented block; unreachable servers never fail verbs (sty_126228b2 / epic:serve-split order:2)

## [0.0.278] - 2026-07-18

### Added
- CLI substrate freshness without serve: post-story-verb backlog refresh, documented SessionStart + reindex + post-verb triggers, self-sufficiency integration test (sty_d0950127 / epic:serve-split order:1)

## [0.0.277] - 2026-07-18

### Fixed
- Fail-visible wrapper tests: binary-present re-emit/exit-code matrix and writeHookScripts retirement of legacy/kimi scripts (closes sty_616c5454 order:2 formal verification)

## [0.0.276] - 2026-07-18

### Changed
- On-demand harness scaffolds: `satelle init` no longer PATH-guesses or defaults to Claude; scaffolds only existing `.claude`/`.grok` dirs or `--harness claude,grok` (sty_92b5ad23)
- Store-backed verbs lazy-install the matching harness scaffold from session markers (`CLAUDE_CODE_*`, `GROK_AGENT`); first session of a new harness may lack hooks (documented trade-off)
- Single parameterized `.satelle/hooks/satelle-hook.sh gate|commitgate <harness>` replaces per-harness pretooluse scripts; re-init retires legacy scripts (epic:minimal-harness-footprint order:2)

### Fixed
- Integration tests that assumed default Claude scaffolds now pass `--harness` / existing dirs; local-binary re-exec and version-stamp tests isolate env/cwd from a repo pin (sty_92b5ad23)

## [0.0.275] - 2026-07-18

### Added
- agents.toml `interface = "command" | "acp"` dual transport for isolated dispatch; default remains full command templates (any CLI including Claude)
- ACP client runner for `interface=acp` (e.g. `grok agent stdio`): session protocol, permission auto-policy, decision fold, timeout kill (epic:agent-dispatch-transport / sty_669f060a, sty_2a8c5d6f, sty_d70c2f5a)
- `satelle help agent-dispatch` documents CLI control-plane in vs command/ACP agent I/O out; validate reports interface= on grants

### Changed
- `RunnerFromBinding` is the dispatch factory; agentstep and applyAgentGrants resolve interface+command together

## [0.0.274] - 2026-07-17

### Fixed
- Isolated agents receive story attachments in the transition payload `docs` array; skills/CTA stop directing attachment I/O at in-repo `.satelle/stories/`; status warns on leftover residue (sty_58fa970e)

## [0.0.273] - 2026-07-17

### Fixed
- `satelle migrate` / `runtime migrate` refuse relocating a live runtime (fresh engagement lease or responding serve); `--allow-live` overrides with an explicit stranded-writes warning; split-brain recovery documented in migrate help (sty_5308eb60)

## [0.0.272] - 2026-07-17

### Fixed
- Default `[gate] edit_exempt_paths` seeds `.gitignore` alongside `.satelle/` so init/migrate's managed `.gitignore` rewrite no longer trips the engaged-story stop hook; `satelle migrate` appends the entry to pre-existing non-empty lists without clobbering operator additions (empty list remains deliberate opt-out) (sty_f115e6bf)

## [0.0.271] - 2026-07-17

### Changed
- Hosted client adopts project-addressed sync routes (`/api/v1/projects/{project}/{config,documents,workstate}`); drops workspace id + `?project=` query; `published` stays workspace-level (sty_ca64d0cb)
- OpenAPI + contract tests pin the project-addressed surface; document sync cursor keys on (server, project, repo)
- Declared park nodes: `from="*"` / `from="s1,s2"` materialize inbound park edges; resume enforced to `park_origin` on work_items; wildcard edge endpoints rejected (sty_f75286dc)
- Workflows (project, substrate, baseline) collapse park idiom to `from="*"` on blocked; `satelle-dot-standard` documents the rule

# Changelog

All notable changes to satelle are documented here. Format: Keep a Changelog–style,
newest release first. Each release is a level-2 `## [X.Y.Z] - DATE` header.

**Breaking marker:** a non-empty `### Breaking` subsection under a version means that
version is breaking — the single marker require-init and post-upgrade heal key on.
Agents retrieve deltas with `satelle changelog [--from X] [--to Y]` (no git history).

**init-heals marker:** a `### Breaking` subsection refuses a repo's ordinary commands
(until `satelle init` runs) only when it contains a bullet starting `init-heals:` —
the release's own declaration that `satelle init` performs every migration the entry
names. Without it the migrations are manual (hand edits, a `jq` command, removing
config), init cannot heal them, so commands run and print the entry's bullets once per
session as a stderr warning, and `satelle init` still re-stamps the repo. Declare it
only when init alone is the whole remediation.

## [0.0.270] - 2026-07-17

### Fixed
- `satelle init` re-run converges the managed `.gitignore` block (stale pre-relocation ignores replaced; content outside markers kept); init help names the home-keyed runtime plane (sty_87c8a69c)

### Tests
- Init-path regression for stale managed-block convergence and home-keyed help text (sty_87c8a69c)

## [0.0.269] - 2026-07-17

### Added
- `satelle migrate [--yes]`: dry-run-by-default compose verb that converges a repo to the current structure — runtime relocate, legacy residue removal, unedited-seed prune, gitignore managed-block converge, then deployment validation (sty_a3915840)

### Fixed
- `ensureGitignore` rewrites the content between managed markers to the current form (upgrade path for pre-relocation ignore blocks) (sty_87c8a69c / sty_a3915840)
- `satelle runtime migrate` is dry-run by default; pass `--yes` to apply (aligned with migrate/prune)

### Changed
- `satelle init` help describes the home-keyed runtime plane and steers half-upgraded repos to `satelle migrate`

## [0.0.268] - 2026-07-17

### Fixed
- Tests that resolve the home-keyed runtime plane without SATELLE_HOME now panic instead of minting orphan dirs under the real ~/.satelle (sty_c36c211f)

### Added
- `satelle runtime list [--orphans]`: list home-keyed key dirs with linked/stale/unknown status, repo reverse-map (repo.path marker + registry), and rm suggestions (sty_c36c211f)
- `testutil.IsolateHome` helper and `repo.path` marker written on open/init for reverse resolution (sty_c36c211f)

### Changed
- Integration host-surface guard records new runtime key dirs as pollution while still allowing pre-existing live-service key trees (sty_c36c211f)

## [0.0.267] - 2026-07-17

### Fixed
- Containment fence denies only another repo's git working tree; temp/scratchpad/non-repo paths are allowed (sty_a8454d10)
- cp/mv/rsync/ln treat only destinations as mutation targets so cross-repo reads are not denied (sty_a8454d10)

### Changed
- Shared foreign-tree predicate (`gitRootOf` / `foreignTreeTarget`) for Bash and Edit gates; removed `isBenignOutsidePath` allowlist (sty_a8454d10)

## [0.0.266] - 2026-07-16

### Fixed
- Status transitions that skip a required workflow DOT step are refused with the expected next step named, even when the transition gater is unwired (sty_ebd3d666)

## [0.0.265] - 2026-07-16

### Added
- Web effective-process view: provenance chips (default|edited|authored) on docs/workflows and node agent/model bindings on the workflow diagram (sty_ba0eb5c6)
- `process-view` verb: substrate list + agentvalidate allocations for CLI/web

### Changed
- Provenance classification extracted to `internal/substrate` (shared by CLI list and web)

## [0.0.264] - 2026-07-16

### Fixed
- Containment no longer denies Bash redirects to `/dev/null` and other stdio sinks (sty_aadd4d6c)

## [0.0.263] - 2026-07-16

### Added
- Cross-repo containment: Bash PreToolUse denies mutations outside the session-home anchor (sty_aadd4d6c)
- `[gate] allow_outside_tree_edits` opt-in (default deny) for deliberate multi-repo installs
- Session principle `satelle-cross-repo-containment` (create anywhere, action only at home)
- Quote-aware Bash tokenizer: `git -C` commit/push forms match; quoted prose does not

### Changed
- Session anchor prefers `SATELLE_PROJECT_DIR` / `CLAUDE_PROJECT_DIR` over CWD-derived config root

## [0.0.262] - 2026-07-16

### Added
- Virtual sparse defaults: workflows/skills/principles resolve from the binary without seeding unedited copies (sty_29e5a9a5)
- `satelle substrate list|edit|prune` for effective provenance, materialize-on-edit, and seed cleanup

### Changed
- Fresh `satelle init` no longer materialises unedited default markdown (tasks still seeded — coded gates need headers)
- DocIndex List/Count overlay embedded defaults at read time

## [0.0.261] - 2026-07-16

### Added
- Home-keyed runtime plane: satelle.db, logs, backups, and stories cache under `~/.satelle/<repo-key>/` (sty_4660bbe1)
- `satelle runtime path` and `satelle runtime migrate` for inspect + explicit legacy migration

### Changed
- Fresh `satelle init` no longer writes runtime files or gitignore entries under the repo for the DB/logs/backups
- Authored substrate stays under `.satelle/`; effective process uses DataDir vs RuntimeDir throughout

## [0.0.260] - 2026-07-15
### Changed
- Rehydrate operator path fully documented (install → login → bind → rehydrate); post-deploy all-local scope note; empty-tree bind+rehydrate happy-path test (sty_2f1538a4)

## [0.0.259] - 2026-07-15
### Added
- Workstate pull: `satelle sync workstate pull` restores stories/executions/ledger from personal hosted into the local store (store-first + view regen); conflict policy fails when both non-empty unless `--force` (sty_45bfcc50)
- Sync-down rehydrate: `satelle sync rehydrate` (alias `sync pull`) runs config deploy → documents pull → workstate pull without pushing; `project bind` creates minimal satelle.toml when absent (sty_2f1538a4)
### Changed
- OpenAPI publishes GET workstate/items and workstate/ledger for pull/rehydrate; workstate wire includes acceptance_criteria, archived, ledger refs/project_id for round-trip fidelity

## [0.0.258] - 2026-07-15
### Changed
- Local DB placement decision: per-repo default path; never multi-repo global blob; agents stay repo-scoped (sty_1eaa15f5)

## [0.0.257] - 2026-07-15
### Changed
- Workspace continuity posture: local disk + init gitignore defaults; personal sync is backup/rehydrate — supersedes git-as-SoT guidance (sty_c5c54f02)

## [0.0.256] - 2026-07-14
### Changed
- Documents pull skips excludedLocal paths before content fetch (no wasted download for backups/ poison); skip visibility hard-asserted on poisoned-partition sync (sty_0fd04503)

## [0.0.255] - 2026-07-14
### Fixed
- Documents pull skips excludedLocal paths (e.g. backups/) and advances the cursor so poisoned partitions unwedge; skipped paths are reported (sty_84f14ace)
- Hosted pre-mutation backup into the documents partition is opt-in via `[backup] hosted = true` (default off) so init no longer wedges sync (sty_84f14ace)

## [0.0.254] - 2026-07-14
### Added
- First surface-scoped consumer: `satelle-design-review` gate on project workflow (`on=integration`, `applies_to=surface:ui`) using this repo's app.css as design authority (sty_e4359efe)

## [0.0.253] - 2026-07-14
### Added
- Decision: v1 trusts surface: tags (accepted risk) with scoped-gate-skipped telemetry when applies_to filters a gate (sty_dcce86d5)

## [0.0.252] - 2026-07-14
### Added
- Additive executor augmentation: edge-less `agent=executor` nodes with `on=` + `applies_to` compose surface-scoped rubrics onto spine steps without forking the workflow graph (sty_8225d8a5)

## [0.0.251] - 2026-07-14
### Added
- Step-level `applies_to` on edge-less scoped reviewer nodes: gate is enqueued only when the story holds a matching tag (EqualFold ANY-match); unknown DOT attributes and mis-placed applies_to fail closed (sty_c6d093c8)

## [0.0.250] - 2026-07-14
### Fixed
- Repair CHANGELOG structure after 0.0.249 entry was inserted into intro prose (sty_034d843c)

## [0.0.249] - 2026-07-14
### Added
- Controlled tag vocabulary via `[tags.vocabulary]` in satelle.toml: namespaces like `surface` reject unknown values at create/set (case-insensitive match, stored in declared casing); multi-surface via repeated keys (`surface:ui` + `surface:cli`) (sty_034d843c)

## [0.0.248] - 2026-07-14
### Added
- Bare `satelle sync` runs the configured sync — config push, documents push+pull, and work-state push across every opted-in area, self-gating on each `[sync]` scope; all-local repos print one "nothing to sync" line. Per-kind subcommands and `sync config deploy` are unchanged/excluded (sty_cfcc3bb8)

## [0.0.247] - 2026-07-14
### Fixed
- Actually ship matching embedded CHANGELOG for 0.0.246 tag-filter release (sty_f7115cd2)

## [0.0.246] - 2026-07-14
### Fixed
- Sync embedded CHANGELOG with root for tag-filter release (sty_f7115cd2)

## [0.0.245] - 2026-07-14
### Added
- `story list` / `task list` support `--tag` for exact tag filter (ANY-match on multi-value namespaces); document repeated-key multi-value as canonical (sty_f7115cd2)

## [0.0.244] - 2026-07-14
### Fixed
- Close-out release for additive story/task tag mutation already shipped in 0.0.241 (sty_033d4611)

## [0.0.243] - 2026-07-14
### Fixed
- init incomplete-hook WARN path tested for unparseable settings; document harness exit-2 enforcement for reinforcement hooks (sty_0699637c)

## [0.0.242] - 2026-07-14
### Fixed
- Strengthen multi-serve dead-root filter test: childRoots + assignSlug never see missing/non-.satelle registry paths (sty_7a8d5d44)

## [0.0.241] - 2026-07-14
### Fixed
- init auto-heals inert principle `scope:` and `principles:always`→`principles:session` before restamp so old repos can stamp and serve (sty_cc8ce91c)
- multi-serve skips registry roots whose path is missing or has no `.satelle` (sty_7a8d5d44)
- init reinforces missing SessionStart/PreToolUse on existing harness hook files and WARNs if the set stays incomplete (sty_0699637c)
### Added
- `story set` / `task set` support `--add-tags` and `--remove-tags` (incl. namespace group remove like `sprint:*`) without clobbering the rest (sty_033d4611)

## [0.0.240] - 2026-07-14
### Added
- Progress strip shows a temporary step-0 pulsing "starting" light while a live pre-transition engagement seat is held and no ledger transitions have landed yet (sty_e1314fe3)

## [0.0.239] - 2026-07-14
### Fixed
- Heal-path release commit subject names the story id for release-gate convention (sty_a9ec33e7)

## [0.0.238] - 2026-07-14
### Fixed
- Remove story-id from init_analysis advice path so repo-agnostic unit check stays green (heal-path release)

## [0.0.237] - 2026-07-14
### Fixed
- gofmt drift_test.go so CI gofmt job passes (sty_a9ec33e7)

## [0.0.236] - 2026-07-14
### Fixed
- Harden init restamp heal integration assertions (restamped report + deployed.version required on release binaries) (sty_a9ec33e7)

## [0.0.235] - 2026-07-14
### Fixed
- Complete sty_a9ec33e7 heal path release with restore-exemption integration coverage

## [0.0.234] - 2026-07-14
### Fixed
- Integration proof that one `satelle init` heals unstamped-identical embedded files (sty_a9ec33e7)

## [0.0.233] - 2026-07-14
### Fixed
- `satelle init` re-stamps stampless embedded-owned files whose body is byte-identical to the current default (breaks pre-stamp repo deadlock); `restore` is exempt from the deployed.version stamp gate so heal commands can run (sty_a9ec33e7)

## [0.0.232] - 2026-07-14
### Fixed
- Rebase best-effort pushes the pre-wipe backup tree to the personal hosted documents partition when configured (sty_873a5380)

## [0.0.231] - 2026-07-14
### Fixed
- Init materializers thread backup policy (ResolveHostedServer + Notice) so pre-mutation backups honor hosted/advisory on converge; scaffold documents `[backup] local_only` (sty_873a5380)

## [0.0.230] - 2026-07-14
### Added
- Pre-mutation substrate backup helper: local floor under `.satelle/backups/`, optional personal hosted push, and `[backup] local_only` to suppress the online advisory (sty_873a5380)
- Converge-overwrite and restore now back up existing files before rewrite; rebase documents the same backups/ policy (sty_873a5380)

## [0.0.229] - 2026-07-14
### Fixed
- Project-workflow integration assertion expects in-loop `in_progress` (`agent=executor`) after sty_db003275 revert (sty_5faf46f1)

## [0.0.228] - 2026-07-14
### Fixed
- Multi-project `serve` hub supervises children for life: log exit to stderr, tear down the dead proxy, respawn with capped backoff, and park after consecutive fast failures — no more silent permanent 502s while the landing shows healthy (sty_5faf46f1)
- Unhealthy child boot no longer registers a live reverse-proxy route; failed projects appear as errored landing rows (sty_5faf46f1)

### Changed
- Project workflow `in_progress` is in-loop (`agent=executor`) again; the `[coder]` binding remains dormant (sty_db003275)

## [0.0.227] - 2026-07-14
### Fixed
- `satelle-substrate-only-check` accepts the binary's managed footprint outside `.satelle/` — the root `.gitignore` and the init-deployed harness hook scaffolds under `.claude/` and `.grok/` — so an init-footprint story closes on the substrate lane instead of being forced onto the full project/code workflow; `[gate] edit_exempt_paths` stays the repo-side extension knob and product code (`.go`, `cmd/`, Makefile, CI) is still rejected naming offenders (sty_40973fb6)

## [0.0.226] - 2026-07-14
### Added
- Project workflow dispatches `in_progress` to an isolated Grok `[coder]` binding with plan-consumption evidence (sty_565a0202)
- `grantsSatelleCLI` accepts grok `read_file` as a disk context channel when headless shell is unavailable (sty_565a0202)

### Changed
- Code skill rewritten for the dispatched coder (plan pull first + PLAN-CONSUMED evidence) (sty_565a0202)

## [0.0.225] - 2026-07-13
### Fixed
- Detect deployed harness scaffold drift vs binary-canonical wrappers; warn on SessionStart/status and fail-closed store verbs until `satelle init` heals (sty_ac25b787)

## [0.0.224] - 2026-07-13
### Fixed
- PreToolUse fail-visible wrappers deploy as `.satelle/hooks/*.sh` with $-free harness commands so Grok no longer skips the edit/commit gates (sty_adfb9862)

## [0.0.223] - 2026-07-13
### Fixed
- Integration suite guards the full host production surface (SATELLE_HOME tree, ~/.config/satelle, installed binary hash/mtime), not only config.toml bytes (sty_6d824d6a)

## [0.0.222] - 2026-07-13
### Fixed
- Integration suite hard-isolates `SATELLE_HOME` (TestMain backstop + per-test `run()` env) so tests never write the host workspace registry (sty_ee7f40c6)

## [0.0.221] - 2026-07-13
### Fixed
- `satelle changelog` and require-init prefer the CHANGELOG **embedded in the binary** — disk is build input only, so downstream repos never get empty false negatives (sty_b5fa838a)
- Integration tests use free listen ports for multi-serve and browser hermetic servers (sty_b5fa838a)

## [0.0.220] - 2026-07-13
### Fixed
- Browser integration tests allocate a free port in `serveRepo` so a leftover satelle process cannot answer healthz for the wrong repo (sty_23aae116)

## [0.0.219] - 2026-07-13

### Breaking
- `satelle install` removed — use `satelle init` (collided with `service install`)
- `satelle workspace rm` removed — use `satelle workspace remove`
- `satelle sync config pull` removed — use `satelle sync config deploy`
- agents.toml: `harness=`, `inject_principles=`, and nested `[agents.NAME]` are no longer dual-read at runtime — run `satelle init` (MigrateAgents) to rewrite
- deployed repos behind a binary with breaking changelog entries fail closed until `satelle init` stamps `.satelle/deployed.version`

### Added
- CLI surface audit (`.satelle/documents/cli-surface-audit.md`) and require-init drift gate (sty_23aae116)

## [0.0.218] - 2026-07-13

### Added
- CHANGELOG.md + `satelle changelog` verb; release fails closed without an entry (sty_f52ba0c3)

## [0.0.217] - 2026-07-13

### Added
- init substrate analysis reports placement/residency/tag-axis and config drift (sty_c73f8905)

### Fixed
- orphaned engagement leases no longer open the edit gate or hold the seat (sty_1738f973)
- PreToolUse wrappers fail-visible: infrastructure failure never bare-denies (sty_c75c73ed)

## [0.0.199] - 2026-07-12

### Added
- feat(init): embedded_sha provenance — converge unedited embedded substrate on re-init (sty_304ee454)

## [0.0.198] - 2026-07-12

### Added
- feat(workflow): per-gate model= override for reviewer bindings (sty_19456622)

## [0.0.197] - 2026-07-12

### Fixed
- fix(hooks): emit harness-specific PreToolUse deny JSON (sty_5e4bc568)

## [0.0.196] - 2026-07-12

### Added
- feat(init): register repo in local workspace registry by default (sty_3bdbdc38)

## [0.0.195] - 2026-07-12

### Changed
- docs(init): gitignore + [sync] comments — git source of truth, sync mirror (sty_aa7cd897)

## [0.0.194] - 2026-07-12

### Changed
- docs(init): scaffold satelle.toml documents [sync]/[hosted]/[vars] (sty_8966f18a)

## [0.0.193] - 2026-07-12

### Fixed
- fix(init): agents.toml scaffold declares role= for zero-warning init (sty_5f1d7b2e)

## [0.0.192] - 2026-07-12

### Added
- feat: on_enter_agent one-shot park entry dispatch (sty_5cabe26f)

### Changed
- release: v0.0.192 on_enter_agent park dispatch (sty_5cabe26f)
- test: copy task-run skill in execution lifecycle fixtures (sty_5cabe26f)
- Wire blocked-triage binding; keep park as reviewer (sty_c77a1672)
- Add satelle-story-blocked-triage performing skill (sty_c29009e7)
- Add satelle-recognise-blockage principle; restore .gitignore managed block (sty_c1c4457b)
- Add recognise-blockage principle for process park/triage (sty_c1c4457b)
- docs(substrate): close record for epic:workflow-review-followups (sty_0fdb7188)
- docs(substrate): hard plan-fidelity gate in code-ac-review (sty_ca97c680)

## [0.0.191] - 2026-07-12

### Changed
- docs(substrate): record hybrid agent-model decision (A) (sty_e3687ec4)

### Fixed
- fix(hooks): teach commitgate pre-execution and fused engage+commit (sty_577d292f)

## [0.0.190] - 2026-07-11

### Added
- feat(agent): role-based gate resolution + verdict transparency (sty_e21cbc08)

## [0.0.189] - 2026-07-11

### Added
- feat(agentstep): formalize Invoke over shared buildRequest+runOnce (sty_ba860c8a)

## [0.0.188] - 2026-07-11

### Changed
- docs(agent): role-contract design for epic:agent-invoke-unify (sty_69fd4e20)

## [0.0.187] - 2026-07-11

### Changed
- docs(epic): close epic:workflow-review — children shipped (sty_171f747e)
- config(gate): exempt .claude/ for harness skill authoring (sty_fad29f15)
- docs(skill): clarify format vs binding drift and refresh path (sty_fad29f15)

## [0.0.186] - 2026-07-11

### Added
- feat(workflow): consultative workflow refresh to canonical format (sty_084f4879)

## [0.0.185] - 2026-07-11

### Added
- feat(workflow): deterministic format-drift detection (sty_2c0c8599)

## [0.0.184] - 2026-07-11

### Added
- feat(agent): validate agents.toml + workflow bindings on demand and at engage (sty_93eec36d)

## [0.0.183] - 2026-07-11

### Added
- feat(wfdot): emit canonical node-consistent edge gates (sty_ccf41efa)

## [0.0.182] - 2026-07-11

### Added
- feat(agents): grok/codex presets, harness→command rename, documented preset menu (sty_17cae74b)
- feat(gate): un-bypassable story+workflow enforcement + agents.toml command presets (sty_949e8739)
- feat(update): sudo-free service restart onto the new binary; release dogfood via satelle update (sty_1ac9f095)
- feat(service): persist the supervisor when systemd --user is unreachable (sty_00dadc91)
- feat(sync): blanket [sync] all scope fallthrough (sty_3ba374e7)

### Changed
- docs(release): mechanism-agnostic dogfood restart (sty_dfc73ced)
- chore: gofmt substrate_okf_test (sty_e4902c51)

### Fixed
- fix(agents): keep [reviewer] grant visible — full grok command, not the bare preset (sty_17cae74b)
- fix(service): --system install restarts a running unit to load the new binary (sty_1ac9f095)
- fix(update): use os.Process.Signal, not syscall.Kill (windows cross-build) (sty_1ac9f095)
- fix(hooks): config-only edit-gate exemptions + fix relative-path bypass (sty_8c3d345c)
- fix(hooks): dual Claude+Grok deny reason on edit/commit gates (sty_e4902c51)

## Older releases

See git tags (`git tag --sort=-v:refname`) for history before the window above.
