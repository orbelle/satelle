# Workflows, gates, performers and agents

A workflow is one file, `.satelle/workflows/<name>.toml`: its states and the
transitions between them, written out explicitly.

    name = "default"
    [states.backlog]
    start = true                 # exactly one start state
    [states.blocked]
    park = true                  # a park state; resumes = true lets "@prior" return
    resumes = true
    [states.done]
    terminal = true

    [[transition]]
    from = ["backlog"]           # "*" = any state that is not terminal or park
    to = "done"
    groups = [{ mode = "sequential", gates = ["review"] }]

An item gets its workflow at create: `--workflow`, else the first
`[[workflows.rule]]` in `satelle.toml` whose `category`, `tag` and `kind` all
match, else `[workflows] default`. It keeps that workflow, with one
exception: `satelle <kind> set <id> --reselect <w> --session <session>` moves it to `<w>`'s start
state when the rules now select `<w>` for it and its workflow has neither
moved it nor judged it (no transition out of a status, no gate run). A refusal
for that evidence exits 2; any other refusal exits 1; neither writes anything.
`satelle workflow select --kind <k> [--tag <t>]... [--category <c>] --session <session>` prints the
workflow and start state the rules choose for those fields.

A workflow may name a panel, `panel = "<name>"` at the top of its file: satelled
lists its stories on a tab of that name instead of Stories (they leave the
Stories count, its backlog badge and the projects list's count), and the tab
counts those not in a terminal state, highlighted while that count is above
zero. Workflows naming the same panel share its tab. A blank panel is refused.

A state may name queues an item entering it joins, `enqueue = ["<name>"]`,
each a `.satelle/queues/<name>.toml`: `satelle validate --session <session>` refuses a name with no
file, and the move is refused when the queue is closed (`satelle help
queues --session <session>`).

A state may declare what its exit waits on outside the session that drives
the item, `waits = "<on what>"` (a person's approval, an outside CI or
score). It is free text, shown by `satelle story route <id> --session
<session>`, and refused on a terminal or park state and when blank. It is the
one place a wait is declared: a session drives each seated item to a terminal
state, and its turn may end with one short of that only at a park state, a
`waits` state or the operator's state; otherwise the stop is blocked once,
naming the next move (`satelle help edit-gate --session <session>`). A queue
an outside party fires is declared by `waits` on the state that enqueues into
it.

## Gates

A gate is a named judgement on a transition:

    [gates.review]
    skill = "review"             # .satelle/skills/review.md or .satelle/skills/review/SKILL.md
    agent = "reviewer"           # or panel = ["a", "b"], combine = "all|any|majority"; satelle.toml [review] may pick them by the item's tier
    paths = ["**/*.go"]          # relevance: runs only when a changed path matches
    trigger = "relevant"         # relevance: a check skill; exit 0 runs the gate, exit 1 skips it
    carry = true                 # an earlier accept over unchanged coverage carries across transitions
    after = ["notes", "publish"] # check skills run in order once the gate has a verdict
    timeout = "30m"              # bounds this gate's check scripts; default 10m
    diff_max_bytes = 64000       # bounds the diff in a judge's prompt; default 64000
    placement = "remote:integration"  # a check gate only: run its check on a node

A transition runs its groups in order. A `sequential` group stops at its first
reject or error (fail-fast); a `parallel` group runs every gate. Every gate's
result — accept, reject, skipped (not relevant), carried or error — is
recorded on the ledger with its notes, tokens and duration. The item moves
only when no gate rejects or errors.

A gate's result is `error` when its judge could not be asked: the dispatch
failed (the harness would not start, exited with an error, or settled with
no answer). An error is not the judge's verdict: `story set` lists it under
`could not judge (error)`, not `rejected by`, and the web shows it in the
warning colour. It still refuses the move (exit 2), but it is not a rejected
round toward a hold, is not dissent, and is not handed to a re-run as a
finding; carry never carries over it. A panel that does not pass is a reject
when any member rejected, else an error. Other failures (a missing skill or
agent, an answer with no verdict) stay rejects. A judge whose binding names a
`fallback` (`satelle help agents --session <session>`) is not an error when
its dispatch fails and the fallback answers: the fallback is asked with the
same prompt and its verdict is the judge's result. An answer with no verdict
never falls back.

Every model gate's verdict (a skill that is not `type: check`) records the
change it saw: its base, head, branch and the hash of the diff alone. `story
diff --json` reports those verdicts as `judged`, each with `now`, the hash of
the item's change today over the same base, branch and paths: read from the
judged branch when it still resolves (this tree when it is that branch, else
the branch's commits, whatever the presenting checkout's HEAD carries), else
from the presented tree. The diff hashed is git's text with `a/` and `b/`
prefixes and the myers algorithm, whatever the user's diff config says, so a
change hashed in its own tree hashes the same once its branch lands; a
verdict read between commits ending at the head it recorded reports `now`
equal to `judged`. The binary does not refuse a terminal move on its
own. A workflow that needs a terminal state reached only with a change a judge
accepted places a check gate on that transition, as satelle's own repository
places `satelle-change-judged-check` first on each move into `done` no model
gate judges; that check's reject names the gate and transition to re-run. A terminal transition with its own
judge covers the change that judge sees. The move into a terminal state
records the change it was made with, and `story diff` of a terminal item lists
that frozen change; later work in the tree is not attributed to it.

A skill whose frontmatter says `type: check` is a deterministic check: its first
fenced `sh` block runs with `SATELLE_ITEM`, `SATELLE_FROM`, `SATELLE_TO` and
`SATELLE_BASE` set; exit 0 accepts, anything else rejects, and its output is the
notes. Each check also gets one JSON object on stdin with the same values:
`id` (`SATELLE_ITEM`) is the item's id, `from` (`SATELLE_FROM`) the state
the move leaves, `to` (`SATELLE_TO`) the state it enters, and `base`
(`SATELLE_BASE`) the base commit the item's change is read against.

When a check skill is a directory (`.satelle/skills/<name>/SKILL.md`),
`SATELLE_SKILL_DIR` is set to the skill's directory and `skill_dir` is added to
the stdin JSON object. The script still runs with the item's worktree as its
working directory, and shell check scripts resolve relative `scripts/<file>`
and `assets/<file>` paths against `$SATELLE_SKILL_DIR` automatically, both
locally and on a remote node. On a remote node `SATELLE_SKILL_DIR` names a
read-only copy of the skill directory, staged from this repo's substrate into
the node's gate input and never read from the checked-out head: the target repo
needs no `.satelle/skills`, and a commit cannot change the check that judges
it. A remote gate refuses a skill that holds a symlink or special file, and
`*.local.*` files in the skill are not shipped.

Every check (a gate's check, its trigger, an after skill, a doctor check),
every dispatched performer and every judge also gets `SATELLE_SUBSTRATE_DIR`:
the absolute directory of the `.satelle` governing the run. A script should
read the substrate through it, not through a `.satelle/...` path relative to
its working directory. In a single-repo project it is `<worktree>/.satelle`
and the working directory is unchanged, so a relative read still works; on a
remote node it names the `.satelle` of the checkout there.
`satelle doctor --session <session>` notes each skill, task or remote `[setup]`
line that reads the substrate by such a relative path.

Reviewer independence is the repo's configuration, not the binary's. `satelle
init --session <session>` writes the check skill `satelle-judge-differs-from-performer-check` to
`.satelle/skills`, and a repo opts in by naming it as the `skill` of a gate
with no `agent` or `panel` on the transitions it chooses. It fails each gate
where no judge is of another model family (claude, gpt, gemini, grok, its
`FAMILIES` table) than the dispatched performer of the from-state: a single
judge must differ, and a panel passes when one member differs. A judge whose
binding names a `fallback` counts as differing only when its fallback is of
another family than the performer too. A session performer has no model in
config and is not compared.

A gate's `trigger` is a check skill whose exit decides relevance: exit 0 means
relevant and the gate runs; exit 1 means not relevant, and the gate is recorded
`skipped` with the trigger's output as its notes. Any other exit, or a trigger
killed by a signal, means the trigger failed: the gate runs anyway (it fails
closed) and its notes start
`trigger <skill> failed (exit <code>); the gate ran` (or
`failed (killed by a signal)`), then the trigger's output. A trigger
that times out, cannot start or names a missing skill rejects the gate.

A gate's `after` is a list of check skills run, in order, once the gate has a
verdict: an accept or reject from its check or judge (every panel member
answered), or a carry. They do not run on a skip, on an error, or on a reject
that is a failure rather than a verdict (a missing skill or agent, a trigger
that timed out or could not run, a coverage error, an answer with no
verdict). Besides the check variables each sees
`SATELLE_GATE`, `SATELLE_GATE_RESULT` (accept, reject or carried) and
`SATELLE_GATE_BINDING`, and its stdin JSON object adds `gate`, `result`, `binding`,
`notes` and, for a judged gate, `members`: each judge's own `binding`,
`result` and `notes` (one entry for a single judge, one per panel member in
panel order; none for a check gate or a carry). Exit 0 keeps the result and
its output, if any, joins the notes as `after <skill>: …`. Any other exit (or a
check that times out, cannot start or is missing) turns the result into a
reject with its output, and the checks after it do not run: each is noted
`after <skill>: not run: after <failed> refused the verdict`. When the list
refuses a judged verdict, the gate.run keeps the judge's raw answer: `answer`
for a single judge, `members[].answer` for a panel (the panel's own members
keep their verdicts, so such a round shows no member dissent). `satelle
validate --session <session>` refuses an entry that is not a check skill and an entry listed
twice. `after` is a list only: a single string (`after = "publish"`) is
refused, naming the key and the list form to write (`after = ["publish"]`).

An entry whose check skill says `after: always` in its frontmatter runs on
every result of the gate, not only on a verdict: accept, reject (a failure
included: a check that timed out or could not start, a missing skill, an
answer with no verdict), carried, skipped and error, and after an earlier
entry refused the verdict. It sees the result as it then stands, so
`SATELLE_GATE_RESULT` may also be `skipped` or `error` and the stdin notes
hold the earlier entries' lines. It can never change the result: exit 0 adds
its output as `after <skill>: …`; any other exit, a timeout or a failure to
start adds `after <skill>: failed, the result stands: …`, and the entries after
it still run. Use it to report a result (a commit status, say) rather than to
judge one. `satelle validate --session <session>` refuses `after` with any other value
and `after: always` on a skill that is not a check.

Each gate of a presentation is recorded as it happens: a `gate.start`
(`transition`, `gate`, `attempt`, `seq`, its position in the presentation)
immediately before it runs, and its `gate.run` (with the same `seq`) as soon
as it finishes, not after the whole presentation returns. A gate runs only
when its start is on the ledger; after a failed write no further gate starts,
the gates already started still record their results, and the item does not
move. Results are read in declared order, so the round cap and carry see what
they saw before. satelled shows a step's exit gates running while it holds a
start with no result for the item's newest presentation leaving the step. A
dry run (`satelle gate run`) records neither. A gate with `carry = true`
reuses an earlier accept of the same gate on the same item across transitions
while its coverage is unchanged (and while its review `tier` is unchanged when
`[review]` in `satelle.toml` picked its reviewers; `satelle help configuration --session <session>`).
The carried `gate.run` names the reused accept's transition and time in
`notes` and `carried_from`, even across chained carries; any change to
coverage, or a newer reject or error, runs the gate again.
Each judged `gate.run` also records the judge's `command`, `model`, `effort`
(when its binding sets one; omitted when unset) and `prompt_bytes` (the byte
length of the prompt given to the judge): on the top-level record for a single
judge, and on each `members[]` entry for a panel.
When a judge's dispatch failed and its fallback judged, that record (the
top level for a single judge, the member's `members[]` entry for a panel)
names the fallback as its `binding`, `command` and `model`, sums both
attempts' tokens and duration, starts its `notes` with `<first> could not be
asked (...); fallback <fallback> judged`, and carries `fallback_from`: the
first judge's `binding`, `command`, `model` and `error` (its `dispatch
failed: ...` text, which passes through the same redaction as `notes`).

Use `after` to hold a verdict to a rule the gate declares, or to publish it.
This repo's judged gates list `satelle-verdict-notes-check` (each judge's notes
hold at least 40 non-whitespace characters) and, on gates that judge a plan or
a change, `satelle-verdict-reference-check` (an accept quotes the work or names
a file, criterion or record path); a refusal's notes say `the judge gave no
reasons`. The rules live in those skills, not in the binary: a gate that
declares none records its judge's verdict as given, and `satelle init --session <session>` declares
none.

A gate's `timeout` (a Go duration such as `"30m"` or `"1h30m"`, positive)
bounds each check script the gate runs — its check skill, its trigger and its
`after` — each run separately; without it the default of 10m applies.
`satelle validate --session <session>` refuses a timeout that does not parse, one that is not
positive, and one on a gate that runs no check script (a judge with no trigger
and no `after`). A check cut at its bound rejects the gate, and the notes
name the bound and where it came from: `timed out after 30m0s (gates.full-test
timeout)`, or `timed out after 10m0s (default; set timeout on
[gates.full-test] to raise it)`. A trigger or `after` that times out keeps its
`trigger <skill>: ` or `after <skill>: ` prefix. A timeout is a failure, not a
verdict, so of the `after` list only an `after: always` entry runs on it.

A judged gate's `diff_max_bytes` (a positive integer) bounds the diff included
in its judge prompt; without it the default of 64,000 bytes applies.
`satelle validate --session <session>` refuses a value that is not positive or
`diff_max_bytes` on a check gate (strict workflow decoding also refuses
`diff_max_bytes` on older satelle versions). Before the bound is measured, each
deleted file is summarised to its path and line count (`deleted file: <path>
(<N> lines)`, or `(binary)` for a deleted binary file) rather than its removed
lines. When the assembled diff exceeds the bound, it is cut on a UTF-8 rune
boundary at or below `diff_max_bytes` and ends with `… diff truncated at
<bound> bytes; cut files:` naming each file whose section was cut or omitted.

`satelle story set <id> --status <to> --session <session>` runs the gates in that command before
it returns. A gate with a long `timeout` (see `satelle workflow show --session <session>`) can
outlast a harness's bound on one shell command: run such a move as the
harness's background command, with that command's own timeout above the sum
of the sequential gates' bounds.

A check gate's `placement` says where its check skill runs: `local` (the
default) here, or `remote:<id>` on a node of the docker-local or fly-sprite
remote `.satelle/remotes/<id>/` (see `satelle help remotes --session <session>`). Only a check
gate may be placed on a remote; `satelle validate --session <session>` refuses any other.
Relevance (`paths`, `trigger`), carry and `after` still run
here, so a skipped or carried gate starts no node and an `after` keeps its
credentials here. The node judges the item's pushed head: its collected
branch's commit, else HEAD on its branch, which must be committed (a clean
tree, not detached) and in the history of `origin/<branch>`; otherwise the gate
rejects saying to push. The gate's `timeout` bounds the whole node run (start,
setup, fetch and the check). The result is the gate's gate.run as a local
check records it, with `location: remote:<node>` and notes that start
`remote <id>: node <node> at <sha>: exit <code>`. A node that fails to start,
a head that does not check out, a timeout, or a run that brings back no exit
code rejects with the cause named; none of them is a verdict, and none passes.

### Hold

A state may cap the rejected rounds of leaving it, and name the park state
that holds the item for the operator once the cap is reached:

    [states.plan]
    hold = { rounds = 3, to = "blocked" }
    [states.blocked]
    park = true
    resumes = true

    [[transition]]
    from = ["plan"]
    to = "blocked"               # declared, and gateless
    [[transition]]
    from = ["blocked"]
    to = "@prior"

A round is one presentation of a transition leaving the state, to a target
that is not a park state, that at least one gate rejected. The count starts
again whenever the item moves, so the rounds counted are rejections in a row.
When a rejection brings the count to `rounds`, the item is moved to `to` in
the same command; the move records a summary: the state and rounds, each
dissenting verdict (gate/binding, or the gate for a check) with how many
rounds it rejected and the class it gave most often (a judge's optional
`"class"` in its verdict object, `unclassified` when it gave none), and the
verdicts that accepted in the last round. `story set` exits 2 and prints it;
`story get` shows it while the item stays held. A park state has no performer,
so the item is not performed again.

`satelle validate --session <session>` refuses a hold whose `rounds` is below 1, whose `to` is not
a park state, that sits on a terminal or park state, or that has no declared
transition from the state to `to`, or one that carries gates.

The move out of the hold is the operator's, never the driving session's: the
session reports the hold and stops. Once the operator has decided, they run
`satelle <kind> set <id> --status <state> --session <session>` (for a resuming park, the state it
was held from), which starts the count again.

## The base

`SATELLE_BASE` is the item's diff base: the commit its gates judge the change
from. A seat records it: HEAD when the seat is taken, or the commit
`satelle story seat <id> --base <ref> --session <session>` names (a stacked branch is seated on the
branch it is stacked on). When HEAD's history no longer contains the base (the
branch was rebased or reset), every transition with gates refuses before any
gate runs, naming the base, HEAD and the re-seat that fixes it; gateless moves
still go through. A gate skipped for relevance (its `paths` or trigger matched
nothing) on an empty diff, or on one missing paths the item's last judged diff
had, says so in a warning on the output and its `gate.run` record.

The base is judged against the item's base branch: the ref the seat's `--base`
named, else `origin/main`. When the recorded base is an ancestor of HEAD's
merge base with that branch, the gates judge from the merge base instead, so
merging `origin/main` into the item's branch never widens the judged diff. The
recorded base stands when the branch is not there, when HEAD is wholly inside
it, or when the base is not behind it (a stacked or story-branch seat).

The item's changed paths are the paths changed by its commits since the base
and by uncommitted changes to tracked files. Untracked files in the worktree
are not changed paths: `satelle <kind> diff` lists them apart, each marked
`untracked:`, and a judge's prompt names them in their own block, marked
untracked. A new file counts as a changed path once it is added (`git add`).
`satelle <kind> diff <id> --stat --session <session>` adds each changed path's
added and removed line counts and a total; a binary file is marked `binary`;
untracked files are not counted.

## Performers

A state may carry a performer, an agent plus a skill:

    [states.build]
    performer = { agent = "builder", skill = "build" }

`satelle story perform <id> --session <session>` dispatches it. A performer whose agent's command is
`session` is the interactive session itself: nothing is dispatched, and the
session does the work under the edit gate.

### What a performer may change

A state with an agent performer says what that performer may change with
`edits`: `edits = "tree"` (the default when the key is absent) lets a seat on
the state edit the worktree; `edits = "attachments"` lets it record only the
item's attachments, so the edit gate refuses every edit and shell write to the
tree from that seat, exempt and locked paths included (see
`satelle help edit-gate --session <session>`):

    [states.plan]
    performer = { agent = "planner", skill = "plan" }
    edits = "attachments"

`satelle validate --session <session>` refuses any other value, and the key on
a state with no agent performer (no performer, or the operator's).

A planning state is judged before any code, so the move out of it can also
refuse a branch that already holds code. `satelle init --session <session>`
writes the check skill `satelle-tree-unchanged-check`, which refuses the move
when the item's tree differs from where its branch left origin/main (or the
branch on origin of an item it is stacked on) outside `.satelle/runs`; its file
in `.satelle/skills/` states the rule. Wire it first on the move:

    [gates.tree-unchanged]
    skill = "satelle-tree-unchanged-check"

    [[transition]]
    from = ["plan"]
    to = "in_progress"
    groups = [{ gates = ["tree-unchanged", "plan-review"] }]

### The operator's states

A state can be the operator's alone: no agent performs it, and leaving it needs
the operator's recorded word. Its performer names the attachment type the
operator records, and takes no agent or skill:

    [states.deploy]
    performer = { operator = "deploy-record" }

While an item is in such a state, satelle dispatches nothing (`perform`
refuses, a drive stops there) and a seat on it allows no agent edits. Every
transition out of it, `*` moves such as cancel included, is refused until the
operator has recorded an attachment of the declared type since the item last
entered the state; a record from an earlier visit does not count. The
transition then runs its configured gates as usual.

The operator records it from their own terminal, with no session named
(`SATELLE_SESSION` unset and no `--session`):

    satelle story record <id> --file F [--name N] [--type-media M]

The type comes from the state; the name defaults to it. Before writing
anything the verb asks for the item id to be retyped on the controlling
terminal (`/dev/tty`); it never reads stdin, and without a terminal it refuses.

Three checks refuse an agent: the kernel refuses any call made with an agent
session (`--session` or `SATELLE_SESSION`), the edit-gate hook refuses the verb in any agent
shell call (behind wrappers such as `env`, `timeout` or `xargs`, and named in
quoted, heredoc or `-c` program text), and the terminal retype needs the
person at the keyboard. The operator mark is set only by this verb: `attach`,
performer artifacts and the server's attach tool never set it, and
`satelle ledger append` refuses every kind satelle reads (item, seat and
attachment events), so a raw append can neither forge the record nor move the
item.

### What a performer and a judge receive

Each dispatched performer gets its skill and the item (title, body, criteria).
When the latest attempt to leave the item's state was rejected, the prompt also
lists each rejecting gate with its notes (8 KiB each), so a re-run can address
them. Each judge gets the item, the change it judges, and the same record.
A judge answers with a JSON object `{"verdict": "accept"|"reject", "notes":
"..."}`; the last such object in its answer counts, and an answer without one
is a reject.

Each judge's dispatch is logged under the runtime logs dir (`satelle logs
--path --session <session>`, then `dispatch/`), one file per dispatch named for the binding, the
time and the item: the command line, then for `pi` every RPC line written
(`> `) and read (`< `) in order, for `claude` and `grok` their stdout (`< `),
then stderr. `satelle logs --session <session>` shows the newest. Only the newest few are kept,
as many as the rotated log files; they hold prompts and tool output, stay on
this machine and are never written to the ledger.

The record is the newest version of each attachment and the item's children.
Before a run on this machine (and on a remote node, from its own ledger)
satelle writes it to a read-only folder under `.satelle/runs/` (gitignored),
names every file in the prompt, and removes it after the run. A folder left by
a run whose process exited before removing it is removed when the next run
writes its folder; a folder whose run is still in progress is kept. Small bodies are
also inlined, up to 16 KiB each and 64 KiB in all, newest first, each cut
marked with the bytes shown and the file to read. A judge's prompt names the
files only: no attachment body is inlined, and the judge reads each file from
the folder. A detached run gets the record inline only. The session reads any version itself with
`satelle story attachment <id> <name> [--sha S] --session <session>`.

To see a gate's input and verdict before presenting, dry-run it:
`satelle gate run <workflow>/<gate> --item <id> --session <session>` runs that
one gate against the item as its transition would. A check gets the same env,
stdin and directory, and a judge gets the same prompt (naming the record files,
not their bodies) and the same record folder,
including a draft plan already attached. The command prints the result, each
panel member's verdict and the full notes. It writes nothing to the ledger, so
it adds no gate.run and no hold round. Every script the gate starts gets exactly
the env a transition gives it. While the gate runs, the dry run leaves a marker
under the ledger, and a satelle that the gate started (a process descended from
the dry run) opens the ledger read-only. A check that attaches a file is
refused, so the dry run records nothing, and every other session writes as
usual. `--from` defaults to the item's state,
and `--to` to the one transition from it that runs the gate. A judged gate
really dispatches its judges and logs them as above. `--show-input` prints the
check's env and stdin, or the judge's prompt, and starts nothing. A gate with a
long timeout runs as a background command, as `story set --status` does.

To compare judges, `satelle gate report --session <session>` folds every judged
gate.run on the ledger into one row per gate and model: runs, accepts, rejects,
errors, confirmed catches, overturned rejects, the runs measured, tokens in and
out, and the median duration. A run counts under its recorded `model_id`, the
normalised identifier recorded beside the harness's own `model` spelling (see
`[models]` in `satelle help configuration --session <session>`), so one model reported under
several spellings totals as one row. A run recorded before `model_id` existed
counts under its recorded `model`, unchanged. A panel member counts under its
own model; a run recorded without a model counts under `unknown`, never a model
read from `agents.toml`. A reject is a confirmed catch when the next accept of the same
gate on the same item judged a different change (another coverage), and an
overturned reject when it judged the same one; a panel member's reject in a run
the gate accepted is overturned. The report counts recorded results, so a
single judge's reject that an after check gave counts as that judge's. A
panel's gate.run records each member's `tokens_in`, `tokens_out` and
`duration_ms` (its dispatch alone; a single judge's duration is the whole
gate's), and `measured` counts the runs behind the tokens and the median: an
older panel run without them counts in runs and verdicts only. `--since` and
`--until` bound the window by the gate.run's time, each an RFC 3339 time, a
`YYYY-MM-DD` date or a duration back from now; `--json` prints the same rows
as one document. The command only reads the ledger.

To inspect one item's outcome, `satelle gate report --item <id> --session <session>`
lists its workflow, current status (marked `terminal` when its workflow
declares the state terminal), each performed state with its command and model
from `performer.run` records, and each judged gate's rounds with its judge's
model and verdict (expanding panel members; check gates, skipped and carried
runs are omitted), each by its `model_id` when recorded, else its recorded
`model`. A run recorded without a model shows `unknown`, never the model
`agents.toml` names today. `--json` prints the same outcome and rows as
one document; `--since` and `--until` do not apply with `--item`.

## Agents

Bindings and adapters: satelle help agents.
