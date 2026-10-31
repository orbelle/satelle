# Named-agent dispatch — how an isolated step receives its instructions

A route's step can allocate its work to a **named agent** instead of the in-loop
session. When a step carries `agent: <name>` (any name other than `executor` or
`reviewer`), satelle **dispatches** that step: it spawns the agent CLI configured
in `.satelle/workflows/agents.toml` under `[<name>]`, hands it the work, and folds the
result back in. The agent runs with a **fresh context** — it never sees the
conversation — so the contract below is how it learns what to do and how it gets
the rest of the story.

The repo agents file is **committed substrate by product default** (role set +
bindings a clone needs to run gated steps); execution detail and secrets live
in the machine catalog and `satelle.local.toml`. See
`satelle help global-agents` for that posture and the fresh-clone checklist.

## The dispatch contract

Dispatch fires **on entry** to the step, after that step's entry gate accepts,
and ONLY because the route allocated that step to a named agent.

**Flat dispatch.** The orchestrator is the sole scheduler: `orch → step → orch`.
Steps never call steps. A reviewer returns a verdict and dispatches nothing.
An agent-less, `agent: executor` or `agent: reviewer` step dispatches nothing —
entering a step never fires an agent of its own.

An **advisor** is a named agent the route says the orchestrator MAY consult —
park triage, a post-close retrospective. It is a declaration, never a dispatch:
`satelle story route <id>` names it, the orchestrator decides when to consult it,
and the orchestrator records the advice on the story. (The earlier
on-enter entry dispatch is retired: a step that fires an agent at itself hides
work from the one place accountable for the route.)

The agent receives:

- **System prompt**, assembled in this order:
  1. the session-resident principles (unless the binding sets
     `inject_principles = false`; default on),
  2. an **executor charter** — you are performing this step of this workflow;
     do the step's work, but **never change the item's status** (the workflow's
     gates govern every advance),
  3. the **pull-context call-to-action** (see below),
  4. the step's `skills:` **rubric** — the instructions for the step.
- **Payload (dual delivery):** the work item as JSON —
  `{story, from, to, review_skill}`. `story` carries the id, title, body, and
  acceptance criteria; `from`/`to` are the transition being performed. Delivery
  is **always on stdin**, and the same bytes substitute into the command
  placeholder **`{payload}`** when the template includes it (one argv token).
  Stdin-first CLIs (e.g. Claude) leave `{payload}` out of the template so the
  prompt is not double-fed; argv-first CLIs (e.g. `grok -p {payload} …`) opt in.
  Empty `{model}`/`{settings}` drop their flag; empty `{payload}` does not.
- **Capabilities**: the binding's `tools` grant, and its `model` unless the

### Multi-reviewer steps (`parallel:`)

A step with several `reviewers:` runs them **concurrently** by default (cap 4):
all verdicts are collected with no short-circuit, ledger order stays list order,
and any reject still refuses the transition (the error names every rejecting
reviewer). Trade-off: a rejected parallel round spends tokens on every reviewer.
Set `parallel: 0` on the step for byte-for-byte sequential execution with
first-reject short-circuit, or `parallel: N` to bound the fan-out. See
`satelle help workflows`.

### Gate binding by agent name

A step's `reviewer_agent:` (or a `[[gate]]` entry's `agent`) may name any
`role = "reviewer"` binding in `.satelle/workflows/agents.toml`. Omitted, the gate uses
`[reviewer]`. The agents layer owns harness, tools, and model — the route names
*who*. See the satelle-route-standard principle.

### A panel of seats

A step or `[[gate]]` may name `panel = ["reviewer-claude", "reviewer-grok"]`
and `combine = "satelle-panel-all-accept"`. satelle runs that reviewer skill
once per section, cold and one-shot, then runs the named check skill with the
seat records on stdin. The check's exit code is the skill's verdict. Who sits
and which check folds them is configuration: the binary ships
`satelle-panel-all-accept` and `satelle-panel-majority` as skills, and a repo
adds a rule by adding a skill. A seat that cannot start, or whose output does
not parse, is recorded unavailable and is not an accept. The ledger row for
the skill carries every seat; each seat that started has its own invocation
row.

### What each role needs

The two roles get their context by opposite routes, so they need opposite grants.

- **Performers** — a spine step with `agent: <name>` —
  are **dispatched** as a child process with **no conversation history**. They
  reconstruct context by *pulling* the story, its documents, and the ledger, so
  the grant must carry a **context channel**: either `Bash(satelle:*)` (a broad
  `Bash`, `Bash(*)` or `*` also qualifies) for the read-only satelle CLI, **or**
  the adapter's own disk-read tool for disk reads under
  `~/.satelle/<repo-key>/stories/<id>/` (Claude `Read`, pi `read`, grok
  `read_file`); another adapter's tool name does not qualify.
- **Reviewers** (`role = "reviewer"`, named by a step's `reviewers:` or by a
  `[[gate]]` entry) need **no channel**: satelle *pushes* the attachments into the transition payload's
  `docs` array, and reviewer bindings never reach the dispatch path that
  consults a grant. A shell grant on a reviewer is capability that is never
  exercised — it only widens the ceiling.

`satelle agent validate` judges both **before** you engage anything: a performer
with no channel is an **error** (non-zero exit — dispatch would refuse it later
anyway), and an unused reviewer shell grant is a **warning** (exit 0 — keeping
it is the repo's call). One predicate decides the channel question for both the
runtime refusal and validate, so they cannot disagree.

**Refusals (fail loud, never silent):**

- A step names `agent: <name>` but `.satelle/workflows/agents.toml` defines no `[<name>]`
  binding → the transition is **refused** (there is no silent in-loop fallback).
- A dispatched binding's `tools` grant carries no context channel (see *What
  each role needs* above) → the dispatch is **refused**, because the agent could
  not pull its context.
- A binding whose `command` is `in-loop` keeps the step with the orchestrating
  session — not dispatched, and so exempt from the channel requirement.

## Control plane in vs agent I/O out

- **In** (orchestrator / any agent → satelle): **satelle CLI verbs** only
  (`satelle story set`, `story get`, …). Small local surface — not an MCP tool
  dump of every verb. Status and engagement change only through satelle.
- **Out** (satelle → isolated worker): subprocess configured in
  `.satelle/workflows/agents.toml`. Four local **transports**, one binding shape
  (epic:agent-dispatch-transport):

| `interface` | Meaning |
|-------------|---------|
| **`command`** (default for a one-shot use) | Full multi-token argv template; any CLI (Claude Code, `grok -p`, wrappers, custom). One-shot stdin/argv; reviewers stay here. |
| **`acp`** | Agent Client Protocol over stdio; `command` is the **spawn line only** (e.g. `grok agent stdio`). System/payload ride the session, not `{placeholders}`. |
| **`stream`** | Stream-json live session (Claude preset, opt-in: `DefaultClaudeStreamCommand`). `{system}`/`{payload}` are rejected — they ride the first user message; `{tools}`/`{model}`/`{effort}` remain argv. For the orchestrator binding (live turns), not reviewers. |
| **`pi-rpc`** | A pi command run in pi's RPC mode, one-shot (`agentcli.PiRPCCommand`). The payload is sent as pi's one `prompt` command; satelle reads every turn, message and tool call as an event, ends the seat (stdin closed, process group killed) once pi reports `agent_settled`, aborts it when it starts a turn beyond `turn_budget`, and records usage from `get_session_stats`. Building the runner refuses a template without `--mode rpc`, `--no-session`, `--no-extensions`, `--no-skills`, `--no-prompt-templates`, `--no-context-files`, `--no-mcp` and `--system-prompt {system}`. Only a pi command may use it. |

Shared fields on all four: `role`, `tools`, `model` (one model, or an ordered
comma list — see *Model list*), `effort`, `secondary`,
`principles`, `env`, `timeout`, `idle_timeout`, `settings`, and — reviewers
only — `isolation = "operator-attested"` (see *Reviewer tool isolation*). Reviewers keep Claude on
`interface = command` (cold one-shot, verdict contract). `stream` is the
live-session option for an orchestrator binding. An ACP-capable CLI is usable
when it implements ACP agent stdio **and** the binding sets `interface = "acp"`.
Workers never advance story status; they return text/verdicts that satelle
enacts after gates.

### Default interface (epic:model-selection child 2)

Omitting `interface` no longer means "always command" — the resolved
transport now depends on **how the binding is used**:

- **Live** (a rework relay's coder seat or a `rework.consult` binding) resolves to the first entry in
  `[defaults] live_interfaces` that can actually open the binding's AUTHORED
  command — two questions, both must hold: MECHANISM (which CLI is this? only
  `stream` for a Claude command, `ExecutableToken` == `claude`; `acp` for any
  other spawn line) and SHAPE (does the argv actually parse for that
  transport? checked by the same construction the runtime opens with, not
  guessed — an authored ONE-SHOT command, e.g. the `[reviewer]` shape's
  `--append-system-prompt {system}`, fails both `stream` and `acp`
  construction, since `{system}`/`{payload}` ride the live protocol, not
  argv, so it is correctly excluded rather than waved through on "first token
  is claude"). A binding with **no `command` and no `profile=`** is refused, never
  resolved to a provider: satelle compiles no live default command. It reports
  `live use: no command` (`satelle agent validate` warns; opening the session
  refuses) with a message naming both live options — `stream` (a stream-json
  CLI spawn line) and `acp` (an ACP agent spawn line). Any default comes from
  authored configuration: a `profile=` in the machine catalog or the `[roles]`
  opt-in. When no candidate can open an authored command, it falls back to
  `command` and names the gap:
  `live use: in-loop` when the command is the in-loop preset (no live
  transport exists for it at all), or `live use: not live-capable` otherwise
  — the existing not-live-capable WARN/refusal then fires with that reason.
- **One-shot** (a gate reviewer, a planner, an edge advisor) always resolves
  to `command`, exactly as before.
- **Explicit** always wins, on either path — an `interface=` that names a
  transport its CLI cannot serve live still produces the not-live-capable WARN
  from `satelle agent validate` and is refused at session open, unchanged.

**Baseline seats.** The logical seats (executor, reviewer, planner, coder,
orchestrator, and the rest the shipped route names) ship embedded. A repo
`agents.toml` names only the seats it changes: a named seat overrides just the
fields it writes, an unnamed seat stays the baseline's, and with no repo file the
baseline runs. A baseline seat carries role and principles only; its command,
tools and model come from a `profile=` the seat names, or from the catalog's
`[roles]` when the repo sets `[defaults] use_global_roles`. Without either, the
embedded fallback stands. A catalog never retargets a repo that did not ask.

The preference order between `stream` and `acp` for a live binding is
configuration, not a compiled opinion — `[defaults] live_interfaces` in
`.satelle/workflows/agents.toml`:

```toml
[defaults]
live_interfaces = ["stream", "acp"]   # shipped default — list only "acp" (or
                                       # only "stream") to exclude a transport;
                                       # mechanism, not list order, decides
                                       # which one an authored command can
                                       # serve, since a Claude command can
                                       # never be acp-capable or vice versa
```

`satelle agent validate` prints each binding's resolved interface and why:
`interface=stream (live use)`, `interface=command (one-shot default)`,
`interface=acp (explicit)`, `interface=command (live use: in-loop)`,
`interface=command (live use: not live-capable)`,
`interface=command (live use: no command)`.

### Progressive execution diagnostics

Command JSONL and ACP `session/update` traffic are normalized into the same
provider-neutral execution events: start, heartbeat, safe message, tool start
and completion, artifact candidate, usage, completion, and failure. Interactive
CLI progress is written to stderr, while the command's structured result remains
on stdout and the final agent response remains authoritative.

Named dispatches write a sanitized normalized event log under the repository's
Satelle runtime `logs/dispatch/` directory. Set
`SATELLE_AGENT_TRACE_RAW=1` only for short-lived transport debugging to create a
sibling `-raw.log`; raw traces are opt-in because provider traffic may be
sensitive. Satelle filters hidden reasoning and redacts obvious credential
shapes from both surfaces, but operators should still protect and remove raw
traces after diagnosis.

### Idle-stall detection vs a hard timeout (`idle_timeout` / `timeout`, sty_752c4ef2)

A dispatch runs for as long as it keeps making progress. Satelle stops it only
when it has genuinely **stalled** — no REAL event (a tool start/end, a
message, or a usage report) for `idle_timeout`. A heartbeat alone does not
reset the clock: a heartbeat proves the transport is still alive, not that the
agent is doing anything, so a dispatch that heartbeats forever with no real
event is still judged stalled.

- **`idle_timeout`** is a Go duration string (`"5m"`), set per binding or once
  for every binding under `[defaults]`:

  ```toml
  [defaults]
  idle_timeout = "5m"   # shipped default when neither level sets one

  [coder]
  idle_timeout = "10m"  # this binding's own override wins over [defaults]
  ```

  There is **no compiled wall-clock cap** — a binding that authors no
  `idle_timeout` and whose `[defaults]` is also silent still gets the shipped
  default (currently 5 minutes of no real event), not an unbounded run.
- **`timeout`** is now an *optional hard ceiling* on one dispatch's total
  wall-clock time. It is **unset by default** — a progressing agent is never
  cut off by elapsed time alone. An operator who genuinely wants an upper
  bound (e.g. a cost or CI-slot limit) still sets `timeout = "20m"` on the
  binding; when set, it wins over the stall detector even for an agent that is
  still emitting real events.
- A stall is recorded as its own outcome, **`stalled`** — distinct from
  `timeout` (the hard ceiling fired) or `error` (the process failed) — carrying
  the idle duration and the last real event's label. It is ledgered as
  `agent-stalled` telemetry, and the refusal text names the stall (`"stalled:
  no activity for 5m0s (last event: tool: Bash)"`), not a generic deadline.
- Applies to every transport (`command`, `stream`, `acp`) and to a live
  session's own turn (a rework relay coder/consult
  round) — not only the one-shot dispatch path.

### After a dispatch ends without a completion (`story recover`, sty_f0ed2473)

A dispatch that stalls, dies or is reaped leaves work on disk and no committed
transition. Before you pay for another performer to re-read it, ask what the dead
one left:

    satelle story recover <id>

It reports the newest dispatch log's last recorded event and the wall time since,
the files present since the engagement anchor, the `agent-stalled` row when the
watchdog wrote one, and the lease. It reports and never vouches: every report says
the transition did NOT commit and the work is UNVERIFIED, and the file list is an
enumeration, not a coherence check (git cannot say which dispatch wrote a file).
A log that ends in a `completed` event is not an unfinished dispatch; the report
says so and lists nothing. A dispatch whose lease is still in flight is reported as
running, not as a recovery.

The decision stays yours. Record it, then act through the normal verbs:

    satelle story recover <id> --choice redispatch|finish|park [--reason ...]
    satelle ledger list --story <id> --kind recovery_choice

The choice writes one `recovery_choice` row and nothing else — it never
dispatches, transitions or parks. This is recognition, not a resume: no live
session is persisted or re-entered.

### Silent one-shot command bindings (`busy_timeout`, sty_db62a3b9)

A `command`-transport binding whose CLI prints one envelope at exit (for
example `claude -p --output-format json` or `grok -p … plain`)
emits nothing while it works, so output alone cannot tell a thinking run from a
hung one. For the command transport only, satelle also samples the CPU time of
the child's **process tree** (Linux: `/proc`); each time it advances, that
counts as liveness and resets the `idle_timeout` clock. A process that makes no
CPU progress (a hang, a sleep) still stalls at `idle_timeout` exactly as before.

- **`busy_timeout`** caps that exemption, measured from the last *real* output
  event (the start event when the CLI printed nothing). Past it, CPU progress no
  longer keeps the run alive and the stall message ends `(busy cap exceeded)`.
  A Go duration string set per binding or under `[defaults]`; binding wins over
  `[defaults]`, which wins over the shipped default (60 minutes). `"0"` or
  `"off"` disables CPU liveness and restores strict `idle_timeout` behaviour.

  ```toml
  [defaults]
  busy_timeout = "60m"   # shipped default when neither level sets one

  [planner]
  busy_timeout = "off"   # strict idle_timeout for this binding
  ```
- Stream and ACP transports are unchanged — they never use the probe.
- On a platform where process CPU time cannot be read (anything but Linux) the
  probe reports that it is unavailable and the run keeps the strict behaviour.
- An explicit `timeout` (hard ceiling) still wins over both.

### Spend budgets — `context_budget`, `turn_budget`, fresh session, coder-step edits (sty_a7914904)

A long driving session and an unbounded coder dispatch are the two ways a story
quietly costs ten times what it should (one repo's driver drove a dozen stories
in one session and made most edits itself during coder steps; one coder dispatch
ran 31 minutes). satelle bounds neither by default: **the numbers are the
repo's, and the binary ships none.** Everything below is configuration.

**The keys.** Both are integers; `0` or absent means *unset*, a negative value is
refused at load.

```toml
# .satelle/workflows/agents.toml
[defaults]
context_budget = 400000        # last tier; backs any binding or step that sets none

[coder]
turn_budget    = 40            # model turns for ONE dispatched coder run
context_budget = 300000        # input tokens for that run
command = "claude -p --output-format json --max-turns {max_turns} …"
```

```toml
# .satelle/workflows/step.toml — a step's own bound wins over its binding
[coded]
status = "in_progress"
agent  = "coder"
turn_budget = 25
```

Each bound resolves on its own: **step, then binding, then `[defaults]`**.
`context_budget` is measured in **input tokens** — fresh + cache read + cache
write, the sum a `driver_usage` row carries — of one dispatched run, or for the
driving session the cumulative input its `driver_usage` rows record.
`turn_budget` is model turns of one dispatched run.

**Handing the turn budget to the harness — `{max_turns}`.** Where the adapter
has a turn-limit flag, write it into the binding's command template with the
`{max_turns}` placeholder (`--max-turns {max_turns}`): satelle substitutes the
resolved `turn_budget`, and drops the placeholder *and its preceding flag* when
none is set, exactly like `{model}`. The **turn budget** column of the
per-adapter capability table below says which adapters can: claude and grok
`command`/`stream` can; **grok `acp` cannot** (its spawn line carries no
placeholders and the session protocol has no turn option — `{max_turns}` in an
acp command is refused, naming the adapter). Where the harness is not told, the
limitation is recorded on the dispatch's `agent_invocation` row, adapter-named,
and the run's reported `num_turns` is checked against the budget instead. A run
that reported no turn count records *unavailable* with the adapter's name — never
a zero, so it can never look like an overrun.

**With no budget set, satelle only warns and records.** Each dispatched run
prints one line with what it measured (turns, input tokens) and records the same
under `budget` on its `agent_invocation` row. A story is **never** parked for
spend unless the repo configured a bound.

**With a budget set, an overrun has a consequence — and only then.** It is
recorded as a `budget_overrun` ledger row (`kind`, `budget`, `measured`,
`consequence`, `reason`), then:

- the step **has a `rework` key** → **rework**: the story is not parked; the
  output names `satelle story rework <id>` (the entry gate still decides);
- the step has **no** `rework` key → the story is **blocked**, the reason
  (`budget overrun: <agent> at step <step> used <n> … > <kind> budget <m>
  (configured by this repo)`) kept on the ledger as the parked story's comment;
- the workflow offers **no blocked state** → the transition is refused with that
  reason. satelle never invents a status (`satelle-agent-goals`).

**Start a fresh session per story — the engage warning.** When a story is
engaged (entering an engaging state from a non-engaging one), satelle checks the
driving session's own `driver_usage` rows. If they already cover **another
story**, the engage output tells the driver to *start a fresh session for this
story*, and the same is recorded as a `session_advisory` ledger row
(`trigger = "other-story"`). This fires with **no budget configured**. A
configured `context_budget` for the step is a second trigger
(`trigger = "context-budget"`): the session's cumulative input has reached it.
It is a **warning only** — engage is never refused for it — and a no-op when no
session id resolves for the caller.

**Coder-step edits go through the named coder.** While the story sits at a step
the route allocates to a named (dispatched) agent, the edit gate refuses the
driver's own in-loop edits. The refusal names `satelle story rework <id>` **only
when that step declares a `rework` key**; otherwise it names the one-shot
dispatch — advancing the edge dispatches the coder. Steps the route allocates to
the in-loop `executor` still accept the driver's edits.

### Structured step artifacts

A skill can ask Satelle to own a dispatched step's final artifact by declaring a
generic contract in its frontmatter:

```yaml
output_name: plan
output_type: plan
output_required: true
output_schema: body
output_ac_coverage: true
```

The isolated agent returns one canonical final object:

```json
{"artifact":{"name":"plan","type":"plan","body":"# Plan\n\n## AC1\n..."}}
```

An optional `output_criteria_section: <heading>` also checks the shape of one
section of the body: under the exact, case-sensitive line `## <heading>` (to the
next `## ` line), every non-blank line must be a numbered criterion (`N. text`
or `N) text`), starting at column 0. A tool line-number prefix (`30|3. x`), a
bullet, indentation or prose is named in the refusal by its line within the
section, so a malformed section fails before any reviewer runs. A body without
the section is not refused by this check.

A reviewer skill that judges the story as a whole (a scope or architecture axis)
returns a verdict whose `criteria[]` holds only the general entry (criterion 0):
that entry then decides. A skill that declares `verdict_criteria: required` must
itemise the judged criteria instead, and a lone general entry is rejected with
notes naming the numbering mismatch (`expected AC1–ACn, got 0`).

A reviewer skill may also declare `payload_citations: <doc type>` (for example
`plan`). Its gate payload then carries a `citations` block (a `citations.json`
material file): every `path:line` or `path:start-end` in the attached documents of
that type, resolved to the lines it names — the lines plus three of context either
side, each numbered — read from the working tree the gate judges at the time of the
review, which needs no engagement baseline. A citation whose file or line is gone
is listed as `unresolved` with the reason (`file not found`, `line N beyond end of
file (K lines)`, `outside the judged tree`, `invalid span`, `judged tree
unavailable: …`), never dropped. The block is bounded (a span is cut at 40 lines,
at most 60 excerpts and 24 KiB in total); a citation past a bound stays listed as
`omitted` and the block says `truncated`. Every other gate's payload is unchanged,
and `satelle validate`'s injected-context report adds the 24 KiB ceiling to a seat
whose skill declares the key. A reviewer may still open any file to check beyond
the excerpts.

Satelle decodes command and ACP results through the same seam, validates the
declared fields and optional acceptance-criterion coverage, and attaches the
typed document before committing the workflow transition. A decode, validation,
or attachment failure refuses the transition and releases its in-flight lease.
Because Satelle owns the write, a contracted planner can use only read-only
repository tools.

An output-contract skill can opt into a bounded validate–repair–escalate policy:

```yaml
attempt_repair_max: 1
attempt_escalate_max: 1
attempt_max_total: 3
attempt_token_budget: 12000
attempt_time_budget: 8m
attempt_on_exhaust: fail
attempt_initial_effort: low
attempt_repair_effort: medium
attempt_escalate_effort: high
attempt_escalate_binding: stronger-planner
```

All keys are optional. With no `attempt_*` keys, dispatch remains a single
validate-or-fail call. Binding names refer to ordinary provider-neutral
`agents.toml` sections; effort overrides use the same command/ACP effort seam as
the binding itself. Quality escalation is deliberately separate from
`secondary=`, which remains reserved for rate-limit or service-unavailable
failover.

After each candidate, Satelle runs the declared deterministic output validators.
A valid initial result attaches immediately. An invalid result can receive a
targeted repair containing the prior draft and all validator findings; only
after configured repair attempts fail can the stronger binding or effort run.
Attempt count, token (when reported), and wall-time budgets bound the loop.
Cancellation and invocation timeouts stop immediately. Exhaustion always fails
the transition: no policy can attach an invalid artifact or bypass a workflow
gate.

Each policy attempt records an `agent-attempt` telemetry event with phase,
binding, model, effort, elapsed time, validator findings, escalation reason, and
`usage_available`. Token fields are omitted when the transport did not report
usage, so unavailable cost is never represented as measured zero.

Legacy self-attaching steps remain supported. To migrate one:

1. Add the `output_*` contract to its skill.
2. Change its rubric to return the JSON artifact instead of running
   `satelle story attach`.
3. Remove `Bash(satelle:*)` from its binding when no other Satelle verb is
   needed.
4. Keep the workflow's exit review responsible for semantic artifact quality.

### Reporting configuration changes — `apply_changes` (sty_af0b1761)

A performer that cannot write — no shell, no `satelle` CLI, no edit tool — can
still repair the stories it is judging. It **reports** the changes it wants and
satelle **applies** them on the record, the way the driving session applies a
planner's criteria. A step opts in beside `apply_criteria` in `step.toml`:

```toml
[ready]
propose = true
apply_changes = { ops = ["add-tags", "create-child"], tags = ["order:", "surface:", "lane:", "depends-on:"] }
```

- **It requires `propose = true`.** The performer must run before the step's
  gates, so they judge the repaired record. `apply_changes` on a step without
  `propose`, a declaration missing `ops` or `tags`, or an op other than
  `add-tags` and `create-child` is refused when the route is loaded.
- **`ops`** names what may be reported: `add-tags` (add tags to a story) and
  `create-child` (file a new story under the container). **`tags`** lists the tag
  prefixes a reported tag may carry; a tag under no listed prefix is refused.

The performer reports a `changes` list beside its verdict or artifact — in the
same final object, or standing alone, and on a reject line as well as an
accept:

```json
{"decision":"accept","notes":"","changes":[
  {"op":"add-tags","story":"sty_aaaa1111","tags":["order:2","lane:trunk"]},
  {"op":"create-child","title":"Add the missing check","body":"…","acceptance":"1. …","category":"feature","tags":["surface:cli"]}
]}
```

Every adapter hands its final text to the same decode, so the report reads the
same from a claude command or stream, a grok command or ACP session, or pi. An
unknown field in a change is an error, not ignored.

Satelle applies a report before the step's gates run, on the engaged transition
and under `satelle story ready` alike, within these limits, and
**all or nothing**: a batch with any violation applies none of it, and the round
fails with notes naming the offending change (`changes[N]`) and the limit.

- the op must be declared; a change targets only the dispatched container or one
  of its members (the `epic:<theme>` set for an epic-parent, the stories whose
  `parent_id` is the container otherwise);
- tags are only **added**, never removed, under a declared prefix and in the tag
  vocabulary; `depends-on:` tags pass the checks `satelle story set` runs, with
  a cycle check across the whole batch;
- no `title`, `body`, `acceptance` or `category` of an existing story is changed;
- a created child names no other story as its target, repeats no member's
  title, passes the same create rules as `satelle story create` (vocabulary, the
  create review, the workflow stamp, closed-epic tags) and is a member of the
  container. Its recorded creator is the performer — its model, or an explicit
  unknown — never the driving session.

Each applied change leaves exactly one row that records it: a `story_updated`
row for added tags, a `story_created` row for a created child, whose actor is the
performer and whose payload names the source step (`performer:<step>`). A step
that declares no `apply_changes` refuses a performer that reports `changes`,
naming the missing declaration, and applies nothing.

A performer that **rejects** the premise may carry `changes` on its reject line.
They are applied within the same limits and the reject is recorded: the engaged
story then parks blocked with the notes, while under `satelle story ready` the
status does not change.

### Answering a re-presented edge's findings — `findings_to_answer` (sty_f948d345)

A performer dispatched for an edge whose latest gate round rejected receives, in
its payload, `findings_to_answer`: each gate seat's latest verdict on that edge
(a panel's seats listed individually) as `{id, skill, seat, decision, blocking,
notes, attempt}`. An `id` is `<skill>[/<seat>]#<attempt>`. Notes longer than the
payload bound are cut with a `[truncated]` marker and a
`satelle ledger list --story <id>` fetch command. Nothing is sent on a first
presentation or when the latest round accepted. The newest directed messages are
kept in the payload window, not the oldest.

The performer answers each blocking finding beside its artifact (or `changes`),
in the same final object:

```json
{"artifact":{"name":"plan","type":"plan","body":"…"},"answers":[
  {"finding":"arch-review/opus#1","answer":"adopted","evidence":"moved the owner to the existing seam"},
  {"finding":"plan-review#1","answer":"rebutted","evidence":"engine.go:1336 shows performerPayload never fills it"}
]}
```

`answer` is `adopted` or `rebutted`; an unknown value or field is an error, not
ignored. Satelle records the offered ids (`findings_offered`) and the answers
(`finding_answers`) on the dispatch's `agent_invocation` row, and tells the gates
of that edge, as a fact in their payload (`finding_answers.missing`), which
offered findings went unanswered. It never rejects on that fact: how strictly an
answer must carry evidence is the gate's rubric.

### Planner transport evidence

For this repository the default `[planner]` remains Claude's non-interactive
command transport. The comparison is reproducible with `make planner-bench`,
which runs the same planning fixtures through that binding shape and Grok ACP
and writes versioned per-run records plus redacted raw and attached-artifact
sidecars under `tests/plannerbench/out/`. The schema and interpretation guide is
`tests/plannerbench/EVIDENCE.md`.

Artifact quality failures remain inspectable benchmark outcomes with explicit
per-criterion reasons. Infrastructure failures or an under-sampled selected
cell fail the target. Usage that a transport does not report is `n/a` with
provenance, never numeric zero.

Changing the binding requires ACP to preserve 100% artifact correctness and
policy fidelity, introduce no reliability regression, and win at least two of:
20% lower median wall time, lower median tokens, or strictly better failure
diagnostics. An ambiguous or under-powered result retains Claude command.
Operators can use Grok ACP as a temporary planner fallback by copying its
benchmark binding into `[planner]`; the documented default is not rewritten
automatically.

### Reasoning effort (`effort=`)

Optional per-binding thinking/reasoning level (e.g. `low` | `medium` | `high`).
Empty means the peer default.

| Transport | How effort is applied |
| --- | --- |
| **command** | Substitutes into `{effort}` (flag dropped when empty, like `{model}`). Also supports fused forms such as `model_reasoning_effort="{effort}"` (empty drops the whole token and a preceding `-`flag). Default Claude/Grok templates include the flag. |
| **ACP** | Session path: `session/set_config_option` for `reasoning_effort` / `effort` (failure-tolerant). **Grok-shaped** ACP spawns also receive argv `--reasoning-effort` (Grok CLI flag). Other non-Grok peers never get that argv flag — it is not ACP (sty_aa726901). |

### Model list (`model = "a,b"`)

`model=` takes one model or an **ordered comma-separated list**. A dispatch runs on
the first model; when that model **does not answer**, satelle re-runs the same
payload **once** on the next listed model — the same binding (command, tools,
effort, role, grant), only the model changing:

```toml
[story-create-review]
role  = "reviewer"
model = "openai/gpt-6.1-terra,moonshotai/kimi-k3.1-pro"
```

"Does not answer" is: the agent settles or exits with no final message, an empty
or unparseable verdict (after the binding's `attempts`), its `timeout` or
`idle_timeout`, or a classified rate-limit or unavailability error. Anything else
— a refused tool grant, a broken command, a cancel, a turn cap — stays on the
current model and fails as it would with one model. Each move is recorded as an
`agent_invocation` row (`phase = "model_failover"`, the model it ran,
`next_model`, and why it moved on); `satelle story route` and `satelle story cost`
show the model that gave the verdict. `satelle agent validate` prints the list on
the binding's `GRANT` line.

Once the list is spent, `secondary=` (below) applies unchanged. A single model
behaves exactly as before. Only the first model is checked against the catalog at
validate time — a typo in a later entry shows when it runs — and the worst-case
wall time is one full dispatch per listed model.

### Rate-limit secondary (`secondary=` / `[defaults]`)

When an isolated dispatch fails with a **classified rate-limit or unavailability**
error (429, overloaded, quota, 503, …), satelle retries **once** on a secondary
binding — without rewriting agents.toml mid-incident:

```toml
[defaults]
secondary = "fallback-grok"   # used when a binding omits secondary=

[planner]
# secondary = "fallback-grok"  # per-binding override of [defaults]
effort = "high"

[fallback-grok]
interface = "acp"
command   = "grok agent stdio"
tools     = "read_file,grep,list_dir"
model     = "grok-4.5"
```

Non-rate-limit failures still refuse the transition (no silent swallow). Unconfigured
secondary preserves single-binding behaviour. Failover is per-dispatch only (not a
sticky rewrite of the primary binding).

Example ACP binding (optional; defaults stay command):

```toml
[reviewer-acp]
role      = "reviewer"
interface = "acp"
command   = "grok agent stdio"
tools     = "read_file,grep,list_dir"
model     = "grok-4.5"
principles = "session"
```

### Stream-json (Claude live session, sty_d244fe1b)

`interface = "stream"` speaks Claude Code's native bidirectional NDJSON
(`--input-format stream-json --output-format stream-json`). Satelle writes the
system prompt and payload as the first user message, answers `control_request`
permission asks with the same mutator policy as ACP, and unwraps the terminal
`{"type":"result","result":"…"}` so `parseDecision` sees the verdict JSON.

Opt-in preset for Claude (never an implicit default), `DefaultClaudeStreamCommand`:

```
claude -p --input-format stream-json --output-format stream-json --verbose --disallowedTools Write,Edit,NotebookEdit,Bash --allowedTools {tools} --model {model} --effort {effort}
```

`{system}` and `{payload}` are rejected on the spawn line; `{tools}` /
`{model}` / `{effort}` stay argv (empty drops the preceding flag). **Reviewers
stay `command`** — a live channel does not help a cold one-shot verdict.
`stream` exists for live seats such as a rework relay's coder or consult binding.

#### Who performs a step

- The **driving session is the in-loop executor**. The shipped `[coded]` step is
  `agent = "executor"`, so the session that engaged the story performs
  `in_progress`, integration, release and the close itself.
- A **one-shot coder** (`interface = "command"`, a `claude -p` style line with
  no `interface = "stream"`) runs only when the route names it on a step's
  `agent =`. It is optional; nothing requires one.
- **`satelle story rework` is the only live relay.** It is off unless the step
  declares `rework = { consult, rounds }`, and its consult binding must be
  live-capable (`acp` or `stream`). A consulting reviewer is
  **consulting, not judging** — its reply is context, not a verdict; the gate
  that judges the edge still runs cold and one-shot over the payload satelle
  builds. See
  `satelle doc get principles satelle-agent-consultation`.

A live session's permission channel is the same policy as PreToolUse (see
**PreToolUse deny channels** below): a mutator tool ask is denied by satelle
without prompting the human when the story is not in an executor-owned
performing state (or a transition is in flight). Otherwise the human is asked
allow/deny.

### Converge then gate

Two loops, contrasted. The **expensive** loop is present-edge → gate rejects →
re-present until the gate accepts. The **cheap** loop is warm convergence
between a coder session and a consulting reviewer (the rework relay, when the
step sets `rework`), then **one** cold gate. The
second does not replace the first — it precedes it. Authority stays with the
gate: a consultant's `READY` is a signal to the orchestrator, never a verdict
(see the READY contract under **Rework relay** below).

**Blocked fallback** (authored in the orchestrator skill, not compiled): after
the round budget without `READY`, or after a declared number of gate rejections
on the same edge, the orchestrator parks to `blocked` quoting the last objection
and messages the developer. The binary carries only the relay and its
termination rule. See `satelle doc get principles satelle-agent-consultation`.

#### Rework relay — `satelle story rework` (sty_8e0b29a0)

The reject → re-present cycle is the expensive loop. The cheaper shape is **warm
convergence then a cold gate**: a coder session and a *consulting* reviewer
session converse about the slice until the consultant says ready or a round
budget is spent; then the orchestrator presents the edge and the edge's reviewer
runs cold and one-shot with the transcript in its payload. Neither live session
moves status. Authority stays with the gate.

**Declared as configuration, on the step.** A performing step opts in; a step
without the key has no loop and behaves exactly as before.

```toml
[coded]
status = "in_progress"
agent  = "coder"                                  # the performer — the CODER side
rework = { consult = "reviewer", rounds = 3 }     # who to converse with, for how long
```

`satelle story route <id>` and `satelle workflow show <category>` print the
line; `satelle agent validate` **warns** when `consult` names a missing or
non-live-capable binding (warn, never a refusal — a repo may author the loop
before wiring the binding, and the relay is opened by hand). Both `agent` and
`consult` must be live-capable bindings: a `command = "in-loop"` performer
cannot be relayed, so a step that opts in must allocate a live-capable one.

**Who opens it.** The orchestrator or the in-repo agent runs
`satelle story rework <id>`, at the story's *current* status. Nothing dispatches
it — entering a state fires no agent (flat dispatch), and satelle relays rather
than anyone monitoring. `--rounds N` may only **lower** the authored budget:
the budget is configuration.

**Turn protocol.** The consultant speaks first (it reviews the slice as it
stands against the ACs). Then each round is consultant → coder → consultant.
Every turn is ledgered as an `agent_message` with its real `from`/`to` roles —
the binding names — plus `cc = "*"`, so `satelle story messages <id>` reads as
the conversation *and* the transcript reaches whoever later judges the edge
through the payload's `messages[]` (within the existing message budget). `cc` is
an additional address a row is readable under; `to` stays who it is for.

**Termination is a rule, not a decision.** The consultant's reply must end with
a FINAL non-empty line that is exactly:

```
READY
NOT READY: <the single most important thing still wrong>
```

`READY` ends the relay with `converged=true`. Anything else — including a reply
that ignores the contract — counts as **NOT READY** with the objection captured
and **consumes a round**, so a consultant that cannot follow the contract cannot
hang the loop. When the budget is spent the relay ends with `converged=false`
and the last objection.

The relay prints its result and records the same as a ledger row:

```json
{"converged": true, "outcome": "converged", "rounds": 2}
{"converged": false, "outcome": "budget", "rounds": 3, "last_objection": "AC4 has no test"}
{"converged": false, "outcome": "no_progress", "rounds": 2, "last_objection": "AC4 has no test"}
```

`no_progress` ends the relay before the budget: the coder's turn changed no
evidence (working tree, attachments, ledger rows — the relay's own transcript
does not count) and the consultant repeated its objection. The ledger row also
carries `stopped_reason`. Park or re-present per rule 4 of
`satelle-agent-consultation`.

**Deferrals.** The consultant's first-turn payload carries a `rework` object:
the coder binding, its authored tool grant verbatim (`coder_tools`), and every
reviewer gate on a later route step (`later_gates`, step + skill). An objection
whose only remedy is outside that grant, or whose evidence a later gate owns,
is written on its own line as `DEFERRED <owner>: <objection>` — `<owner>` being
`grant` or a skill in `later_gates` (a final `NOT READY: DEFERRED <owner>: …`
counts too). A deferral is never handed to the coder and does not count as the final
NOT READY; a relay with only deferrals left ends
`{"converged": true, "outcome": "converged_with_deferrals", "deferred": [...]}`,
and the ledger row carries the same list. The deferred item reaches the owning
gate's payload as `deferred` (including inside a bundled session). A `DEFERRED`
line naming any other owner is an ordinary objection: it spends a round and is
relayed.

It **never** sets status. `READY` is a signal to the orchestrator, never a
verdict: the orchestrator presents the edge, or — after the budget without
ready, or after a declared number of gate rejections on the same edge — parks to
`blocked` quoting the last objection. It never lowers an AC to converge. See
`satelle doc get principles satelle-agent-consultation`.

**Permission policy, per side.** The two sessions are *not* symmetric:

| Side | Charter | Mutator tools |
| --- | --- | --- |
| coder (step's `agent`) | executor — it is driving its own edits | allowed **iff** its own binding's `tools` grant admits mutators **and** the seat is live, not mid-transition, and its committed status is a performing state the route allocates to **that binding** |
| consultant (`consult`) | consulting — reply is context, not a verdict | **denied by policy**, whatever the seat says |

The coder's rule is the route's own allocation, read from the seat — not a state
name compiled into the binary. The consultant's grant is already read-only; the
policy is the second, non-negotiable refusal, because a consultant that can edit
is a reviewer marking its own work. PreToolUse `gate` / `commitgate` honour the
same allocated-binding rule when the rework verb marks the coder spawn with
`SATELLE_RELAY_BINDING` / `SATELLE_RELAY_ITEM` (and exports the lease's session
id so the hook binds that seat); unmarked sessions stay on the driving-session
and in-flight dispatch branches.

#### Headless drive — `satelle story drive` (sty_c0c795df)

`satelle story drive <id>` walks a story along its route with no one at a
prompt, for a repo whose bindings are all headless CLIs (claude, grok, pi, …).
It presents each next edge through the same verb `story set --status` uses, so
every edge is judged by its configured gates and ledgered as a local drive
ledgers it. `drive.Story` (internal/drive) is the mechanism; it takes no
harness, so a node's story command calls the same function.

**Who performs a step** is the route's allocation, asked of the engine's one
rule (`StepPerformer`): a step allocated to a named agent is dispatched by the
move that enters it; a step allocated to `agent = "executor"` is performed, once
the story is in it, by the `[executor]` binding run **one-shot** with that
step's executor skills. Before any move the drive refuses (exit 1, naming the
step and the `agents.toml` key) a route it cannot walk unattended: an
executor-allocated step while `[executor] command` is `in-loop`, or a step whose
named agent is bound in-loop.

**A rejected edge.** The reviewers' notes go to the performer the engine names
for the step (a `story message` from `orchestrator`; an executor step is
re-run with them appended to its prompt), the step's declared
`rework = { consult, rounds }` relay runs within its budget, and the edge is
presented again. The story **parks** — a comment with the last objection, then
the move to the workflow's blocked step — when the relay ends `budget` or
`no_progress`, when the step's `reject_budget` is spent, when an edge whose step
declares no `reject_budget` has been rejected ten times, or when the executor
rejects its step. A performer reject and a broken spend bound park through the
mechanisms that already do; the drive adds its own park comment to those.

**Every end is a record.** After its first presentation a drive never ends short
of the route's exit without one: a refused presentation, a performer that failed,
a context that ended (a timeout, a cancel) or a session that ended without its
verdict also park the story. Every park the drive writes is a comment row with
payload `source=drive`, `step`, `performer`, `reason` (`step <s>, performer <p>:
<what was missing>`), `last_message` (the tail, at most 2000 bytes, of the newest
performer `agent_invocation` row's `last_message`) and `location_id`, which is
what satelle-server alerts the developer on. A workflow with no park state, or a
park the blocked gate refuses, leaves nowhere to put the question: the drive
writes one `drive-stopped` row with the same payload and `park_error`, releases
its own hold, and exits 1. A refusal before the first presentation (preflight, no
route) parks and records nothing.

**Exit status.** `0` the story reached a terminal step; `3` it is parked (the
status and the recorded reason are printed); `1` the drive refused before it
began, or stopped and could not park (`<id> stopped at <status>: <reason>`). An agent caller's drive is
handed off as one gated run, like `story rework`; inside that process every gate
runs in the foreground, and the seat lease and ledger rows carry the resolved
session.

### Workspace bindings layer — `satelle sync bindings` (sty_01949949)

The agents layer can be decided centrally and executed locally. Two verbs and
one file:

- `satelle sync bindings push [--dry-run]` publishes this repo's
  `.satelle/workflows/agents.toml` into the bound TEAM workspace's publish
  catalog (kind `agents`), exactly as it is on disk.
- `satelle sync bindings pull` applies that catalog entry as
  `.satelle/workflows/agents.workspace.toml` — a separate file, so sync never
  rewrites authored bytes. The bare `satelle sync` aggregate pulls it too when
  the `agents` area is opted in.
- The workspace file is a **layer under** the repo's `agents.toml` in the
  precedence ladder: `repo → workspace → profile / global-role → embedded`. A
  repo field wins; a workspace field fills a repo blank; a workspace-only table
  applies whole. Role is identity on this tier as on a profile — a disagreement
  is refused. `satelle agent validate` prints the layer's presence and names
  the source of every effective field (`source: tools = "…" (workspace)`).

**A push sends the file as written and refuses what belongs to one machine.**
Every push of an agents layer — `sync bindings push`, `publish push` of
`workflows/agents.toml` (whatever `--kind` was typed) and the `agents` area of
`sync config push` (`--snapshot-only` and `--dry-run` included) — checks the
file first, before any network call, and refuses when it carries: a literal
`env` value (in a binding's `env` or under `settings.env`), a literal value
under a secret-shaped `settings` key, an absolute path (anywhere in `settings`,
or a token of `command`), `profile=` or `harness=`, or
`[defaults] use_global_roles = true`. A pure `${VAR}` reference is a variable
name, not a value, so it travels; so does a blank ("declared, unsatisfied").
Each refusal names the file, section and field and points at
`.satelle/workflows/agents.local.toml`, which never leaves the machine —
`satelle doctor` and `satelle init` warn on the same rules. Nothing is
rewritten on the way out. A body that does not parse is an error, never sent.

A push also runs the validation `satelle init` runs, judged as another machine
would load the configuration: `agents.local.toml` and `agents.workspace.toml`
are left out, so an allocation that resolves only through them refuses the
push. `sync.*` findings stay warnings, as in init. One thing is still withheld
from `satelle.toml`: `[sync] project` and `[hosted] project`, each named in the
push output, so a deploy cannot rebind another repo.

**Ingest writes the hosted layer as it is, or refuses it.** `sync bindings
pull`, `sync config deploy`, `publish adopt` and `publish check --update` check
the hosted agents layer with the same rules; one an older push left carrying
machine-specific content is refused with the pointer above rather than altered
or landed as live bindings. `agents.toml` is written exactly as hosted and
`agents.local.toml` is never touched. The workspace tier drops blank env keys
and blank secret settings before it is merged, so a pulled layer can never lay
`KEY=` over the machine's real environment or a blank secret over
`settings.local.json`.

**Executables and `${VAR}` resolve on the machine that runs the binding.** The
workspace says `claude -p …`; this machine's PATH decides whether `claude`
exists. A binding whose program is missing is a hard refusal at dispatch — the
same posture as a missing binding, never a silent in-loop fallback — and a
`WARN` in `satelle agent validate`. `${VAR}` references expand from the local
`[vars]` at wiring time, as they always have.

A repo with no hosted server or no team workspace does nothing on either verb
and contacts nothing; a repo that never pulled a layer resolves byte-identically
to before.

### Supported adapters — claude and grok

Satelle supports two agent adapters: **claude** (command and stream) and **grok**
(command and acp). An adapter satelle does not handle records an adapter-named
unavailable rather than a default. Both run on the transports above; `stream` is
Claude's live channel.

**`satelle agents` vs `satelle agent`:** `satelle agents install|remove` provisions
two satelle-owned surfaces per target (`claude` / `grok` / `all`):

1. **Launchers** under `$SATELLE_HOME/agents/bin/` (e.g. `satelle-grok`).
   Generated ACP bindings use `command = "sh <launcher>"` (multi-token) so
   `interface=acp` accepts them.
2. **Harness compliance scaffolds** in the repo: `.claude/settings.json` and
   `.grok/hooks/satelle.json` — blocking PreToolUse hooks that deny governed
   code-changing actions unless a satelle story is engaged.

Ownership: only marker-bearing launchers and satelle hook entries are written or
removed; user harness keys/hooks are preserved. Install/remove are idempotent.
Neither path changes the default reviewer or `[agent] cli`.

`satelle agent` (singular) selects and validates the headless CLI / agents.toml.

#### PreToolUse deny channels

The installed `satelle-hook.sh` uses one coherent structured-deny contract:

| Harness | Structured deny emitted by Satelle | Handler exit |
| --- | --- | --- |
| Claude | `hookSpecificOutput.permissionDecision=deny` with a non-empty `permissionDecisionReason` | `0` |
| Grok | top-level `decision=deny` with a non-empty `reason` | `0` |
| Cursor | top-level `permission=deny` with non-empty `user_message` and `agent_message` (`user_message` is the reason the model sees; a bare `reason` blocks the tool but hides why) | `0` |

Exit `0` means the hook handler ran successfully; the JSON decision still blocks
the tool. Claude and Cursor also honour a separate fallback contract for exit
`2`: the blocking reason must be non-empty on stderr and structured stdout is
not the authoritative channel. Do not mix exit `2` with JSON-only stdout and
empty stderr. Satelle's wrapper prefers the structured path for policy and
infrastructure denials, emits a static safe infrastructure reason when the
binary is absent or unusable, and keeps irrelevant/read-only Bash fail-open so
the operator can diagnose the installation.

#### Install compliance

```bash
# Launchers + .claude/.grok blocking-hook scaffolds (repo cwd)
satelle agents install all
# Or a single harness:
satelle agents install grok
```

#### Live dogfood vs CI

- **Hermetic tests** (in `make integration` / unit suites) use fake ACP peers and
  validate/buildArgs only — they never require a provider CLI or API keys.
- **Live dogfood** is optional: the operator authenticates the agent CLI itself
  (Claude login, Grok session). Satelle never stores or injects agent API keys.
  Point a named binding at the CLI, run `satelle agent validate`, then drive a
  cheap gate (e.g. step-summary). Optional live probes must never be required by
  CI.

Claude remains the default init `[reviewer]` preset until an operator opts in.

## The command template — full templates and placeholders

Each binding's **`command`** says *how* the agent runs. With **`interface =
"command"`** (the default), an **isolated** binding requires a **full multi-token command template** — the real argv is literal in the file so the operator can
read exactly what will run. Bare single-token CLI names (`claude` /
`grok`) are **rejected** by `satelle agent validate` (and refuse engage); run
`satelle init` to expand a legacy bare preset, or write the full template
yourself. The only bare single-token value that remains valid is:

- `command = "in-loop"` — no subprocess; the driving session performs the step.

An omitted `[reviewer] command` resolves to the default full claude template
(read-only denylist on Write/Edit/NotebookEdit/Bash). Example full templates
seeded by init and the canonical consts:

- Claude (stdin-first): `claude -p --output-format json --disallowedTools Write,Edit,NotebookEdit,Bash --append-system-prompt {system} --allowedTools {tools} --model {model}`
- Grok (argv-first): `grok -p {payload} --system-prompt-override {system} --tools read_file,grep,list_dir -m {model} --deny Write --deny Edit …`

The first token is the binary; the rest are argv tokens carrying the placeholders
(each one argv token): **`{system}`** (the rubric), **`{tools}`** (the grant),
**`{model}`**, **`{settings}`**, **`{max_turns}`** (the repo's `turn_budget`, see
*Spend budgets*), **`{payload}`**. Empty `{model}`/`{settings}`/`{max_turns}` drop
that flag; empty `{payload}` does not. The work item is **always also on stdin**
(dual delivery), so stdin-first CLIs (claude) omit `{payload}` and argv-first
CLIs (grok) include it.

With **`interface = "acp"`**, `command` must **not** contain those placeholders —
satelle sends system/payload over the ACP session instead.

> The field is **`command`**; the older key **`harness`** is a **deprecated alias**
> — a pre-rename `agents.toml` still parses (`command` wins when both are set).

The agent's output is captured to the executor log (and, for a task execution, a
run-output document), so an isolated step's work stays reviewable.

## The pull contract — reconstruct context by id, don't wait to be told

Because a dispatched agent starts fresh, satelle does **not** cram documents or
history into the payload. The payload is a **handle** — the item and its id — and
the agent **pulls** everything else itself, by id, with the read-only satelle CLI:

- `satelle story get <id>` — the full current record.
- `satelle story docs <id>`, then `satelle story doc <id> <name>` — the attached
  documents: the implementation **plan** and every prior **step summary** (each
  gated transition deposits one), which narrate the work so far.
- `satelle ledger list --story <id>` — the evidence ledger (transitions, review
  verdicts, summaries).
- `satelle story messages <id>` — directed agent messages (kind `agent_message`),
  oldest first, each with its state (waiting, delivered, answered, undeliverable)
  and an overdue flag; `--to` keeps that role plus `*`; `--since` is RFC3339.
  Read-only. Write with `satelle story message <id> --from <role> [--to
  story|role:<r>|session:<id>|*] --body <text>`; `satelle board` lists every
  story's messages. A performer's dispatch marks the role messages it injects
  delivered.

A read-only reviewer whose grant excludes Bash judges attachments from the
transition payload's `docs` array (injected by the engine, sty_58fa970e) and
the engagement slice from the payload's `diff` object (files, stat, patch;
sty_a125b440) — no disk path required. Both are enumeration, not verdict.
The payload also carries `messages[]` (sty_2db624d0): engagement-windowed
`agent_message` rows whose `to` is the dispatched binding name, the protocol
role (`reviewer` on a gate, `executor` on a named performer), or `*`, oldest
first, at most 20, each body capped at 8 KiB (a longer body is cut, marked
`truncated`, and ends with the `satelle story messages` command that fetches
it whole). A story still at its start state (backlog) has no engagement yet;
its window opens at its latest entry into that state, so a performer that
proposes from backlog (`propose = true`) receives the messages sent to it there
(sty_36ac4319). A message is **context**, never a verdict input a reviewer must
obey. Absent when none qualify (`omitempty`).
Shell-granted agents may also pull more via the satelle CLI.
Do **not** use in-repo `.satelle/stories/` — that path is obsolete
post-relocation. **Fetch before concluding a document or a prior step is
missing** (payload first, then CLI when available).

`story docs`, `story messages`, and `ledger list` may render a compact
`[N]{col:kind,...}` table instead of a JSON array — this repo's `[output]`
config defaults compact mode on for a dispatched or in-loop caller (see
`satelle help compact-output`); parse it the same way, or pass `--json` for
the plain form. `story diff --patch` similarly loses its `index` lines and
offloads noisy/whitespace-only hunks behind a `<<ccr:HASH,KIND,SIZE>>` marker
that `satelle retrieve <hash>` resolves exactly.

## Scratch directory, attaching without a file, and leftover files (sty_e7aaf8b1)

A dispatched or live agent session never has to be told where scratch work
goes, and never has to write evidence into the repo tree to hand it to
satelle:

- **Every dispatch gets a scratch directory by mechanism.** A one-shot
  `Invoke` (reviewer, named executor, retrospective), the step-summary
  dispatch (`Summarise`, its own `buildRequest`/`runOnce` call outside
  `Invoke`), and every live session (the rework relay's
  coder and consult seats) each get their own
  `<tmp>/satelle/<repo-key>/<story>/<dispatch-id>/`, mode `0700`.
  It is exported as both `TMPDIR` and `SATELLE_SCRATCH` — RESERVED keys that
  override any binding-authored `[env]` value of the same name — and named in
  the generated charter every binding receives, so no skill or principle file
  carries the instruction.
- **Success removes it; failure keeps it.** A dispatch or live session that
  ends cleanly has its scratch directory removed. One that errors (a failed
  run, a session whose `Close()` returns an error) keeps the directory and
  records a `scratch_kept` ledger row naming its path, so a failure stays
  inspectable.
- **Attach without a file.** `satelle story attach <id> --name <n> --type <t>
  --body "..."` takes the document body inline — no pipe, no file, works
  under a `Bash(satelle:*)`-only grant. `--body`/`--file` are mutually
  exclusive; `--file -` reads stdin, and `--file $SATELLE_SCRATCH/<f>` is the
  alternative for a very large document (the scratch path sits outside the
  repo tree and is already edit-gate exempt under `/tmp/`). `satelle story
  log <id> --kind <k> --data key=value` takes typed telemetry the same way —
  a `--data` value may itself be a multi-line string.
- **Leftover files are swept before the next gate.** A DRIVING-role session
  (a named coder's one-shot perform dispatch, or a live coder/orchestrator
  session — never a read-only consult/reviewer session) that leaves an
  untracked file matching `[dispatch.leftovers]` is caught at the dispatch's
  end, before the next gate sees the diff:

  ```toml
  [dispatch.leftovers]
  patterns = [".ac-evidence*", "*_debug_test.go"]  # globs, path or basename
  content_regex = '^\s*package \w+\s*$'            # optional: full-content match
  max_bytes = 64                                    # bounds the content_regex read
  action = "move"                                   # "move" (default) | "flag"
  ```

  Only files **this session created** count — a snapshot of untracked files is
  taken before the session runs, so a pre-existing untracked file is never
  touched. A match is moved to `<scratch>/leftovers/<relpath>` (kept there,
  inspectable, even though the run itself succeeded) or, with `action =
  "flag"`, left in place and only reported. Either way a `leftovers` ledger
  row names the files and the scratch directory. Empty `patterns` and empty
  `content_regex` (the default — the binary ships no opinion, not even a Go
  test-file rule) disable the sweep entirely, with no extra `git` cost.

- **Clearing debris the sweep did not catch (sty_d74e9b1b).** A coder can create
  files but has no grant to delete them, and the scope gate rejects untracked
  debris before anyone may delete. `satelle story tidy <id> <path>...` MOVES
  such paths into a story-level tidy area (`<tmp>/satelle/<repo-key>/<story>/tidy/`,
  a sibling of the per-dispatch scratch dirs, so it outlives them) and writes one
  `tidy` ledger row per path; `satelle story untidy <id> [<path>...|--all]` moves
  them back and writes `tidy_restore` rows. It works at any performing step, from
  the driver or a dispatched session, and is all-or-nothing: a path that is
  tracked, exists in HEAD or at the engagement baseline, predates the engagement
  baseline, is missing, or lies outside the worktree refuses the whole call with
  a reason per path, and untidy never overwrites. The scope-review skill
  ends a debris rejection with the ready-to-run command.

## What makes a step safe to dispatch (sufficiency)

- **Give the step a rubric.** A dispatched step needs `skills: <name>`.
  A rubric-less dispatched step (`agent: <name>` with no `skills:`) receives only
  the charter and the item — rarely enough to perform a real step.
- **Make the item self-sufficient.** An isolated agent never sees the
  conversation, so the story's body, acceptance criteria, and attached docs must
  **stand alone**. Anything the step needs that lives only in the chat is lost.
  The plan and step-summary documents (pulled by id) are the sanctioned channel
  for carrying context forward — not the conversation.

## Proving a step's skills were delivered and used (`skill_evidence`, sty_23ceeca2)

A step names skills; satelle records that the performer got them and checks that
its work shows they were followed. Nothing here needs an LLM.

- **Delivery.** Every dispatched performer's `agent_invocation` row carries
  `skills`: each step skill delivered, by name and the sha256 `hash` of the body
  the substrate index holds. A step the driving session performs (`agent =
  executor`, or a named agent bound in-loop) records the same list on an
  `in-loop` row when the story enters the step, and `satelle story set` shows the
  session the composed skills once. A skill that does not resolve is in the list
  as `delivered: false` with a reason — never skipped.
- **Declared evidence.** A skill may declare what its use leaves behind, in its
  frontmatter, as comma-separated lists: `evidence_sections` (markdown headings in
  the step's output document), `evidence_keys` (top-level keys of a dispatched
  performer's final JSON), `evidence_commands` (shell-command prefixes among the
  step's commands, matched on the command text) and `evidence_tools` (tool names
  among its tool events). `satelle validate` rejects a malformed declaration and
  warns on a step skill that declares none, which then gets the delivery check
  only.
- **The check.** Always on. When a dispatched performer returns, and when a story
  leaves a step the driving session performed, satelle compares the output and
  the run trace with each delivered skill's declaration and names every missing
  item (`<skill>: missing section "X"`). A dispatched run's trace is its transport's
  tool events; a headless executor run's is the same stream, recorded as one
  `tool_trace` row with its performance; the driving session's is the `tool_trace`
  rows the gate hooks write, which see only the tools each harness's hook matcher
  lists (`agentcli.HarnessHooks`). A hook writes a row only while the engaged
  step's skills declare `evidence_commands` or `evidence_tools` (the step's entry
  row says so), so a step that declares nothing costs its edits and commands no
  ledger write. An item the trace cannot show is reported **unverifiable**, naming
  the adapter, and never counts as a gap.
- **What the in-loop check reads.** Only what the step itself produced: documents
  attached and tool calls recorded since the story entered the step, so an earlier
  step's document (an old plan) never satisfies a later step's declared section.
  It judges only the step's **forward exit**; a back-edge (rework to an earlier
  step), a park and a cancel are never refused for evidence.
- **Cloud sessions are delivery-only.** A step performed by a cloud session
  (`interface = cloud`) records each skill delivered, and an undelivered skill
  refuses under `reject` or is recorded under `record`; its declared evidence is
  not compared, because the session's tool calls are not returned.
- **Strictness is configuration.** `skill_evidence` in the route source's
  `[meta]` (`done.toml`) is `record` or `reject`; the default is `record`. Under
  `record` a gap or undelivered skill is written on the row and shown to the
  driver, and the transition proceeds. Under `reject` a gap refuses the
  transition (a dispatched step is refused before it runs when a declared skill
  does not resolve). Any other value fails `satelle validate`.

## Gate/dispatch sequencing — judge the EXIT edge

Dispatch fires **on entry to the target state, after the entry gate accepts** —
so a dispatched step's work must be judged by its **exit edge's** gates. An
entry-gated state followed by an ungated commit/push ships the dispatched agent's
mutations **unjudged**. When you allocate a step to a named agent, make sure the
edge *out* of that state carries the review that vets what the agent did.

## Custom agents — a worked example

To add a custom agent (say an `architect` that runs on a stronger model), you
define it as a **binding** and **allocate a step to it** — both in satelle's own
substrate, never in a harness's agent directory.

1. **Define the binding** in `.satelle/workflows/agents.toml`:

   ```toml
   [architect]
   command = "claude -p --append-system-prompt {system} --allowedTools {tools} --model {model}"
   tools   = "Read,Grep,Glob,Bash(satelle:*)"   # read-only + the pull-context CLI
   model   = "opus"                              # per-step model selection is the model key
   ```

   Argv-first CLI example (payload on `-p` **and** still on stdin). Grok's
   single-turn flag is `-p`/`--single` — put **`{payload}`** there (not only on
   stdin). Prefer `--output-format json`: satelle unwraps Grok's
   `{ "text": "…" }` envelope the same way it unwraps Claude's
   `{ "result": "…" }`, and the envelope carries the run's `usage` and
   `num_turns`, so its tokens and turns are recorded. `--output-format plain`
   also parses, but records neither. Use enough `--max-turns` for tool-using
   gates (1 is often too low).

   ```toml
   [architect]
   command = "grok -p {payload} --system-prompt-override {system} --tools {tools} --always-approve --output-format json --max-turns 8 --no-subagents"
   tools   = "read_file,grep,list_dir,run_terminal_command"  # Grok-native tool ids
   # `read_file` IS a context channel (disk reads under ~/.satelle/<repo-key>/
   # stories/<id>/), so a Grok-native grant needs no Bash(satelle:*) to dispatch.
   ```

2. **Allocate a route step** to it in `step.toml` (`agent: architect`):

   ```toml
   [designed]
   status = "design"
   agent = "architect"
   skills = ["architect"]
   ```

3. **satelle dispatches it** on entry to `design`: the item on stdin, the
   `architect` rubric (+ charter + pull-context call-to-action) as the system
   prompt, the binding's tools/model as the grant — on whatever CLI the harness
   names.

**Anti-pattern:** defining that agent in a harness-specific agent directory (e.g.
`.claude/agents/architect.md`) works *for that one harness*, but hides the process
configuration from satelle — it cannot see, validate, dispatch, or carry it
repo-agnostically, and it silently pins the repo to one CLI vendor. Keep process
agents in `.satelle/workflows/agents.toml` + the route's two halves.

## Mixing model backends — per-binding env + `${VAR}` (sty_001558ce)

`model` selects the model *within* the harness's CLI; to point one step at a
*different backend* (a non-default API), a binding may also set **`env`** —
environment variables layered onto that dispatched agent's process, binding keys
winning. Each value may reference the **`[vars]` KV** via `${NAME}`, resolved at
load; an unknown `${VAR}` refuses the command (naming the binding + var) rather
than dispatching with a blank credential. `${...}` appears only in env values, so
it never collides with the `{system}/{tools}/{model}` argv placeholders.

Put the KV in `[vars]`: NON-secret values may sit in the committed `satelle.toml`;
**secrets go in the gitignored `satelle.local.toml`**, whose keys win per-key. The
KV is file-only (no DB) and `satelle.local.toml` is excluded from the substrate
push, so a key never leaves the machine.

Worked example — run one step on **GLM** through z.ai's Anthropic-compatible
endpoint (the *same* `claude` CLI, no wrapper binary), while the in-loop session
stays on its own model:

```toml
# .satelle/workflows/agents.toml
[planner]
command = "claude -p --append-system-prompt {system} --allowedTools {tools} --model {model}"
tools   = "Read,Grep,Glob,Bash(satelle:*)"
model   = "glm-4.6"   # the model id the endpoint expects (glm-5.2 is the newest)
env     = { ANTHROPIC_BASE_URL = "https://api.z.ai/api/anthropic", ANTHROPIC_AUTH_TOKEN = "${GLM_API_KEY}" }
```
```toml
# satelle.local.toml  (gitignored — never commit the key)
[vars]
GLM_API_KEY = "sk-…"
```

Keep the **committed default** on your always-available backend so a clone with no
key still runs; make the alternate backend an **opt-in** the operator switches on.
An exit gate that re-judges the step (e.g. `plan → in_progress`) on the default
backend keeps a weak alternate-model output from reaching the build.

See also: `satelle help workflows` (choosing a lifecycle) and
`satelle help reviewer-checks` (gate skills).

## Model selection (sty_7069bced)

`model` is optional in agents.toml. When a binding leaves it unset, satelle
selects one deliberately instead of silently taking the CLI's own default, and
records WHY next to the alias and the resolved id (`satelle story cost`, the
web timeline). Precedence, first match wins:

1. **binding** — the binding's own `model =` in `.satelle/workflows/agents.toml`.
2. **step** or **agent** — a model named for THIS ONE dispatch: a workflow
   step's `model =` in `step.toml` (a spine performer node only — a gate/edge
   has no step tier of its own), or a dispatching agent's `--model` flag
   (`satelle story rework --model`, `satelle
   story retrospect --model`). An agent flag wins over a step default when
   both would apply to the same dispatch.
3. **inherited-in-loop** — the in-loop engaging session's model on this
   story. It is guarded so a Claude session's model id can never reach a
   Grok dispatch — see "cross-provider guard" below.
4. **creator** — the model of the session that created the story, same guard.
5. **order** — the first entry of the `[model_order]` list for THIS binding's
   executable (see "The model order" below). Applied only when the binding can
   accept a model, and never from another executable's list.
6. **cli-default** — none of the above resolved: the `{model}` placeholder is
   dropped and the CLI's own default runs. Recorded as `cli-default`, not left
   silent.

Every dispatch — a gate, a named-agent step, a live rework session, a
retrospective, quality escalation — records BOTH the resolved model and which
of these seven values (`binding`, `step`, `agent`,
`inherited-in-loop`, `creator`, `order`, `cli-default`) picked it, on the same
`agent_invocation` / `review_accept` / `review_reject` row that already
carries `model_resolved`. `satelle agent validate` reports a step's
`model =` as the node's effective model (source `step`) whenever the
allocated binding itself has no `model =` (source `binding` wins outright).

### The model order

An empty `model =` stays a real choice: it lets a dispatch follow the driving
session of the same CLI. When no same-CLI session model applies, the order tier
uses `[model_order]` in agents.toml — one list per executable, first entry
first. An entry is a name, or an array of names that are one rank (an alias and
its resolved id); the first name is applied. `satelle init` writes these lists
into a fresh agents.toml and appends them to an existing one that has no
`[model_order]` table:

| executable | order (first applies) |
| --- | --- |
| claude | opus, sonnet, haiku |
| grok | grok-4.7 |

The grok id comes from a captured ACP `modelId`; claude uses the CLI's own
aliases. An executable
with no list falls through to `cli-default`, recorded as such.

### The cross-provider guard

An inherited or creator model is applied only when the dispatch binding's own
command template carries a `{model}` placeholder (a slot to fill) AND the
captured session's executable matches the binding's own command executable
(e.g. both `claude`). A Grok-native session's model id never rides
into a Claude dispatch, or the reverse — a guard failure simply falls through
to the next precedence tier instead of erroring.

### Per-adapter capabilities

What each adapter gives satelle. An unavailable is recorded explicitly — an
adapter-named reason on the ledger row, never a silent zero or a Claude default
(`satelle-agent-agnostic`). `internal/agentcli/capabilities.go` is the source of
this table and a test checks every cell against the code that produces it.

| adapter | usage | cache split | resolved model | model inheritance | live session | tool trim | offered tools | turn budget | cloud launch |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| claude command | yes | yes | yes | yes | unavailable: interface=command is one-shot only | yes | yes | yes | yes |
| claude stream | yes | yes | yes | yes | yes | yes | yes | yes | yes |
| grok command | yes | yes | yes | unavailable: grok's hook payload carries no model, so the in-loop tier is unknown | unavailable: interface=command is one-shot only | yes | yes | yes | unavailable: grok command has no cloud runner |
| grok acp | yes | yes | yes | unavailable: grok's hook payload carries no model, so the in-loop tier is unknown | yes | unavailable: grok agent stdio has no tool-list flag and reports no permission mode, so a grok acp reviewer runs with a warning that its tools are not held to the grant | unavailable: grok agent stdio neither trims nor reports offered tools | unavailable: grok agent stdio has no turn-limit argv or session option, so a turn_budget is recorded and checked against reported turns only | unavailable: grok acp has no cloud runner |
| pi pi-rpc | yes | yes | yes | unavailable: pi's hook payload carries no model, so the in-loop tier is unknown | unavailable: interface=pi-rpc runs one-shot dispatches only | yes | yes | yes | unavailable: pi pi-rpc has no cloud runner |

**turn budget** means the harness itself is handed the repo's `turn_budget`
through the `{max_turns}` placeholder in the binding's command template and
stops the run; it is only "yes" for a binding whose template carries the
placeholder, or for `interface = "pi-rpc"`, where satelle itself aborts the run
when it starts a turn beyond the budget (see *Spend budgets* below).

**cloud launch** means a binding with `interface = "cloud"` can run its step in
a cloud session of the harness (see *Cloud performer* below). Only claude has a
runner: it runs `claude --cloud` under a pty provided by `script`, so it needs
linux or darwin and reports unavailable on any other host when asked to launch.
A harness with no runner fails the dispatch with `cloud session: unavailable for
adapter <name>` and launches nothing.

### Cloud performer — `interface = "cloud"` (sty_82cffd60)

A step's performer can run in a provider's cloud session instead of a local
process. Bind it in `agents.toml`:

```toml
[coder]
role        = "agent"
interface   = "cloud"
command     = "claude -p {system}"   # names the harness; the cloud runner is its own
collect_doc = "ac-evidence"          # optional: attach the final commit body under this name
timeout     = "90m"                  # optional: how long to wait for the branch
```

`cloud` is a fourth value of `interface`, valid only on a `role = "agent"`
performer. A reviewer, an advisor or a rework consult cannot use it, and it is
never a live transport, so `story rework` and consult refuse it by name.
`collect_doc` is valid only here. A cloud performer is handed the whole payload
in its prompt and pulls nothing locally, so — like an in-loop binding — it needs
no `tools` grant and the context-channel check does not apply to it; the example
above validates as written.

When the step is entered, satelle:

1. refuses unless the story worktree's branch is **pushed** (HEAD equals its
   upstream) — it never pushes for you — and unless the step's skill declares no
   `output_*` contract and no `attempt_*` policy, which a cloud session cannot
   return;
2. launches the session from the worktree with a prompt of the step's skill, then
   the `satelle-cloud-performer` skill (embedded; a repo overrides it to say what
   its sessions leave in the final commit), then a data-only block: the payload
   (story, criteria, plan, relayed messages), the **branch** and a **nonce**. The
   session has no satelle CLI, store or token and is never told of the hosted
   service;
3. waits for the branch the adapter named (claude: `claude/satelle-<story>-<nonce>`)
   to carry a tip commit whose message ends with the trailer
   `Satelle-Nonce: <nonce>`. That trailer is the **only** completion marker. It
   waits for the binding's `timeout`, or **60 minutes** when none is set;
4. fetches the branch and fast-forwards the worktree to it, or merges it when
   that is conflict-free; a merge that is not clean is aborted and the dispatch
   fails with the session URL, the worktree untouched;
5. when `collect_doc` is set, attaches the tip commit's message body (the trailer
   excluded) to the story under that name. What the body holds is the step
   skill's business; satelle never inspects it. With `collect_doc` unset nothing
   is attached.

The session id and URL, branch and collected commit go on the ledger
(`cloud_dispatch`) and the dispatch result; usage is recorded as unavailable,
naming the adapter. The step's exit gates then judge the collected work locally,
exactly as for a headless performer. What a repo does to hold the session to its
branch while it runs (a tracked hook that refuses edits and commits off the
dispatch branch, for instance) is that repo's own configuration, not satelle's.

#### Placing an epic's parallel children remote — `remote_agent`, `local_tags` (sty_dde8b6a4)

A spine performer step in `step.toml` may name a cloud binding for the children
of a parallel epic, and the tags that keep a child local:

```toml
[coded]
status       = "in_progress"
agent        = "coder"              # performs the step by default (local)
remote_agent = "coder-cloud"        # a role=agent, interface=cloud binding
local_tags   = ["lane:trunk"]       # a child carrying any of these stays local
```

`satelle agent validate` accepts the keys when `remote_agent` names a
`role = "agent"`, `interface = "cloud"` binding (and counts that binding as
used), and rejects them, naming the step, when the binding is missing or not
cloud, or when the step is a container step (`waits_on_children` or
`after_children`). The placement rule, the not-signed-in fallback, the push the
driver owes before presenting a remote step, the refusal of `story rework` for a
remote child and the failure behaviour are all in `satelle help epic-wave`
("Where a child runs"). No embedded workflow declares `remote_agent`, so a repo
that does not opt in behaves as before.

### Reviewer tool isolation

A reviewer is read-only, and satelle keeps it inside its `tools` grant whatever
permission mode the harness runs in:

- **Every role=reviewer dispatch, one path.** A gate verdict, the step summariser
  (`[reviewer-summary]`) and the rework consultant's live session
  (`[reviewer-consult]`) all pass the same preflight, the same read-only trim and
  the same `agent_invocation` recording; a binding satelle cannot hold to its
  grant runs with a warning on any of them (see *Warned, never blocked*). The
  consultant keeps its own mutator ceiling as well — the grant policy narrows it,
  never widens it.

- **Tool classes.** Every tool a harness can offer is classed `read`, `write`,
  `edit`, `shell`, `subprocess`, `network`, `mcp` or `unknown` by the adapter
  (`internal/agentcli/isolation.go`). A tool outside the grant is denied by class;
  a tool no table names is `unknown` and denied unless the grant lists that exact
  name. A scoped grant entry such as `Bash(satelle:*)` is enforced by claude
  itself; grok cannot enforce a specifier, so it admits nothing there.
- **Trimmed where the harness can.** Claude reviewers are spawned with
  `--tools <the grant's base tool names>` (the AVAILABLE built-in tools;
  `--allowedTools` only pre-approves), `--strict-mcp-config` (no MCP servers,
  so no `mcp__*` tool is offered) and, when the template sets no
  `--permission-mode`, `--permission-mode default` — so a user-settings
  `defaultMode` of `bypassPermissions` cannot switch off a scoped grant such as
  `Bash(satelle:*)` (under `-p`, an ask `--allowedTools` does not pre-approve is
  denied). A template-set mode is left alone. A grok command reviewer must carry its own
  `--tools` allow-list (the stock preset does: `--always-approve` is safe there
  because only the granted tools are offered). Grok ACP (`grok agent stdio`) has
  no tool-list flag and no way to force a permission ask, so a grok ACP reviewer
  runs with a warning (next bullet).
- **Warned, never blocked** (sty_2d5e583a). A configuration satelle cannot hold to
  its grant is not refused: the process starts and its verdict stands. Every
  such dispatch **warns** — one line on stderr naming the binding, the adapter,
  the gap and the fix — records the gap as `isolation_limitation` (and
  `offered_tools_source`) on the `agent_invocation` row plus a
  `reviewer-isolation-warned` ledger event, and `satelle doctor` reports the
  binding. These are all the warned configurations:

  | configuration | gap | fix |
  | --- | --- | --- |
  | a **grok ACP** reviewer (`grok agent stdio`) | tools not held to the grant (grok 1.0.41's `session/new` reports no permission mode and has no ask flag, and the user's own grok config may set `always-approve`, so satelle can neither confirm nor force ask mode); usage accounting not to standard (this transport reports no offered-tool figure and may report no usage) | the grok command transport with `--tools` equal to the grant, or `isolation = "operator-attested"` |
  | a harness **no adapter recognises** (a script, another CLI) | tools not held to the grant (satelle can neither trim nor deny tools) | a claude or grok binding, or `isolation = "operator-attested"` |
  | **grok** that skips permissions (`--always-approve`, `--yolo`) without a `--tools` allow-list equal to its grant (absent, blank — `--tools=`, `--tools {tools}` rendered from an empty grant — or wider) | tools not held to the grant | add a `--tools` allow-list equal to the grant, or drop always-approve, or `isolation = "operator-attested"` |
  | **claude** that skips permissions (`--dangerously-skip-permissions`, `--permission-mode bypassPermissions` or `acceptEdits`) while its grant scopes a tool (`Bash` under `Bash(satelle:*)`), or with no `--tools` allow-list | scoped grant not enforced / tools not held to the grant (the specifier is not applied when permissions are skipped) | drop the skip flag, add `--tools`, or `isolation = "operator-attested"` |
  | an ACP reviewer peer that offers **no ask mode** (or refuses the switch to one) at session open | the peer offers no ask mode, so a tool permission request is not guaranteed | none in satelle; the warning is per dispatch |

  An **observed breach** — an ACP tool that ran outside the grant without a
  permission request, or after a deny, or a switch into a never-ask mode — is
  recorded on the ledger (`reviewer-isolation-breach`) and warned; it never aborts
  the run or cancels the session. A breach is behaviour satelle saw, not a
  configured gap, so `isolation = "operator-attested"` does not silence it.
- **`isolation = "operator-attested"`** is a binding key in `agents.toml` (and in
  a machine-wide `[profiles.<name>]`): the operator's acknowledgement of every
  configured gap in the table above, adapter-decided or not. With it the
  dispatch warning is suppressed and `satelle doctor` reports the binding as INFO
  (acknowledged) instead of WARN; the ledger still records the limitation — for an
  unrecognised harness with `offered_tools_source = "operator-attested"` and an
  `isolation_limitation` naming the binding, never a count. Nothing is silent. It
  is the only accepted value; any other value fails at load.
  ```toml
  [judge]
  role      = "reviewer"
  command   = "./scripts/verdict.sh {system} {tools} {model}"
  tools     = "Read,Grep,Glob"
  isolation = "operator-attested"
  ```
- **`auth = "subscription" | "api_key" | "org"`** is a binding key in `agents.toml`
  (and in a machine-wide `[profiles.<name>]`): how the binding is paid for and
  authorised — the user's logged-in CLI, a per-call API key, or an organisation
  account. It is declared, never detected, and any other value fails at load. Each
  gate verdict row and each performer `agent-attempt` or dispatch row records it as
  `auth_mode` (`unrecorded` for a binding that declares none, and for a row written
  before it existed); `satelle story cost` shows it in an `auth` column beside the
  seat, separate from the pricing mode (`[pricing]`), which alone decides what a
  token costs.
- **`untrusted_input = true`** on a `step.toml` step declares that the step reads
  text from an external raiser. `satelle validate` then refuses a seat on it — its
  `agent`, and its `reviewer_agent` or each `panel` seat — whose grant admits a
  shell, subprocess, network, mcp, write, edit or unknown tool, whose adapter
  cannot hold it read-only (an unrecognised harness, a pi command with no
  `--tools`, a cursor performer not asking for `--mode ask`, an in-loop or cloud
  seat), or, for a performer, whose read-only grant leaves it no context channel.
  One finding per cause, naming the binding and the step. A pi seat with
  `--tools {tools}` and `tools = "read,grep,find,ls"` passes, and so does a claude performer granted
  `Read,Grep,Glob`; a claude performer with neither `Read` nor a satelle shell cannot.
- **Recorded.** Each reviewer `agent_invocation` row carries
  `system_prompt_bytes`, `offered_tool_count` and `offered_tools_source`
  (`flag`, `harness` — claude stream's own `system/init` tool list — or
  `operator-attested` — no count — or `unavailable: <adapter reason>`, in which
  case there is no count), and, for
  grok ACP and operator-attested bindings, `isolation_limitation`. The grant
  length is never written in place of a count.
- **`satelle doctor`** WARNs, naming the binding, the gap and the fix, for each
  configured reviewer binding in the table above — including every grok ACP
  reviewer binding; with `isolation = "operator-attested"` it reports INFO
  (acknowledged) instead. The stock claude and grok command presets report
  nothing.

### What each harness reports

- **Claude Code** — the in-loop hook payload's `model` field (or the
  transcript's last assistant entry) captures the in-loop tier.
- **Grok** — its hook payload carries no model, so the in-loop tier is recorded
  `unknown`.

Model inheritance in the capability table is "available" when the in-loop tier
can apply; it needs a hook that carries a model, so without one the dispatch
falls through to `[model_order]` and then `cli-default`, recorded explicitly.

An explicit `model =` on a binding is unaffected by any of this — it wins
outright, unchanged from before this story, and no session capture or ranking
lookup ever runs for that dispatch.

## Waiting on a gate — agent-facing calls (sty_c4b92c9e)

A command that runs a reviewer — `story set` to a gated status, `story create`
under `gate_create`, `story amend`, `story resummarise`, `story retrospect`,
`story rework` (and the `task`/`execution` forms of set and create) — can hold
its caller for minutes. A driving harness backgrounds any command that runs
past its own cutoff, and the driver then re-enters the model to learn how the
command ended: every re-entry is a full model call. So an **agent-facing call**
(no terminal on stderr, or a session a harness identifies) does not wait out the
gate:

1. It starts the real run detached and waits a bounded time — the shortest
   background cutoff of any harness configured for the repo, less a margin, so
   it always returns before that harness can background it.
2. If the gate finished inside the bound it prints the finished verdict block —
   each gate's decision and notes, plus the id a create made, and nothing else:
   not the record the command would have printed and not its advisory notes —
   with no reviewer progress lines.
3. Otherwise it prints one JSON object, `{"gate":"pending","handle":"gw_…",…}`,
   and exits 0. The gate keeps running.

The verdict is then **delivered into this session as a notification**, by the
harness's own hooks — the driver never calls back to learn it. The Stop hook
(`satelle hook stopcheck`) waits for the running gate when the driver ends its
turn and answers with the verdict, which the harness feeds back as the
session's next input; the UserPromptSubmit hook puts a gate that finished
between turns in front of the model with the next prompt. What is delivered is
the handle, the command it ran, and the verdict lines — the same block an inline
return carries. Each finished run is delivered once. **Do not poll, sleep-loop, or re-run a backgrounded command** —
there is no status verb to poll, a re-run starts a second gate, and the only
thing to do with a pending handle is end the turn.

A person at an interactive terminal is unaffected: the gate runs in the
foreground and the reviewer's progress prints on stderr as before.

### Whether a repo hands off — `[gate] handoff`

Handing off is a per-repo choice, `handoff` in the `[gate]` table of
`.satelle/satelle.toml`:

| value | behaviour |
| --- | --- |
| `auto` (default) | an agent-facing call hands off only when a harness configured for the repo has a background cutoff under 1 minute — the shortest a gate-running command was measured to hold its caller (grok's 15s qualifies, claude's 120s and pi's 10m do not); otherwise the gate runs in the foreground |
| `on` | every agent-facing call hands off |
| `off` | every call runs in the foreground |

**Why `auto`.** A hand-off exists to beat a harness's background cutoff. Where
no configured harness has one, a handle only adds a notification the driver may
check too early, and it changes behaviour for scripts and operators with no
terminal. The 1-minute line is measured, not a typical gate time: the driver
windows around gate-running commands in this repo's ledger run from 53s to
6m55s (a lone reviewer, 9s–50s). Under a minute, no gate can finish inside the
cutoff, so a hand-off is unavoidable. Above it, `auto` is not a guarantee: a
multi-gate edge can outlast claude's 120s, and a claude repo whose edges run
that long should set `handoff = "on"`. A harness with no facts row takes the conservative floor, so an
unrecognised harness counts as needing the hand-off. A harness WITH a row but
no notification path is recorded with that limitation quoted in the pending
line — never a polling fallback. The decision is the adapter's fact table
(`agentcli.HandoffNeeded`), not a harness name in the CLI.

`SATELLE_GATE_MODE` overrides the repo setting: `interactive` gives a script or
an operator the foreground behaviour, `agent` forces the hand-off.
`SATELLE_GATE_WAIT=<duration>` overrides the bound.

What each harness can do is a fact about the harness, recorded in
`internal/agentcli/harness_facts.go`. A harness with no notification path is
recorded with a harness-named limitation — the pending line quotes it — and is
never given a polling fallback; a limitation is not a claim that the adapter is
fixed.

| harness | background cutoff | completion notification | in-turn wake / budget | prompt additionalContext | PreToolUse warn | session context channel |
| --- | --- | --- | --- | --- | --- | --- |
| claude | 2m0s — the Bash tool's default timeout (120s) | yes | a Stop-block wakes the session; cap 8 (CLAUDE_CODE_STOP_HOOK_BLOCK_CAP default) — measured on claude 2.1.288: a live always-block Stop probe was overridden after 9 consecutive blocks (session db658341-256f-413c-ac4e-c6c48922fbf5, 2026-10-03). Past the cap, or after a StopFailure/SessionEnd, a gate is delivered by resuming the same session with the verdict as the prompt (sty_7e4393fc) | yes — delivered, and the scaffold's engaged reminder rides it | yes | unavailable: claude: no session-context channel is recorded |
| grok | 15s — grok backgrounds any command past 15s (sty_c4b92c9e) | yes | cap 8 continuations (a Stop-block and a non-error Stop feedback each count); after 8 the gate is overridden, hooks are not consulted and the turn ends; the counter resets on the next user prompt — measured from grok's stop_gate.rs notes and porting list. Past the cap a gate is delivered by resuming the same session with the verdict as the prompt (sty_eac9b28d) | unavailable: grok: SessionStart stdout is ignored; UserPromptSubmit additionalContext is discarded | unavailable: grok: PreToolUse additionalContext is a documented Claude-compat channel but unverified for PreToolUse by any probe | unavailable: grok: no session-context channel is recorded |
| pi | 10m0s — a live pi 0.87.1 foreground call held 25m with an explicit timeout and 45s with none; pi does not auto-background a long command | yes | Stop cannot veto: the extension turns a verdict into a user message that starts another turn, sent once — one bounded wait, then the stop is allowed, so there is no re-prompt budget to spend; a non-interactive run that exits on settle is not held and records a limitation for the undelivered gate | yes — the extension injects it into the system prompt via before_agent_start | unavailable: pi: a tool_call handler returns only {block, reason} or undefined; notify() reaches only the UI; a non-interactive run cannot send a message | unavailable: pi: no session-context channel is recorded |
| cursor | 30s — cursor: Shell tool calls carry timeout=30000 with timeoutBehavior=TIMEOUT_BEHAVIOR_BACKGROUND (hardTimeout 86400000) — cursor backgrounds a shell call at 30s (testdata/cursor/{2e-hooks,3-abs,3-user,4}.out; tool_input.timeout in {3-abs,3-user,4,7-deny,7-stop}-hooks.log) | yes | a stop hook's followup_message re-prompts the session; cap 4 (loop_count 0-3 take a followup, a followup at 4 produces no further turn) — measured on the captured cursor-agent build (CapturedVersions), testdata/cursor/20-cap. Past the cap the verdict is left pending and delivered at the stop of the session's next turn, where the count restarts. unavailable: cursor: print mode (-p) dispatches no stop event (testdata/cursor/4-hooks.log), so a gate outlasting cursor's 30s shell foreground is not delivered to a print-mode driver, which holds the foreground instead | unavailable: cursor: the prompt reminder (beforeSubmitPrompt) is unavailable — print mode does not dispatch it and interactive delivery is unproven (sty_7d098d50); session context rides sessionStart | unavailable: cursor: native deny carries agent_message; no allow-with-message field is verified for preToolUse | unavailable: cursor: no session-context channel is recorded |


The session-context column is the event `[harness.<name>] session_context_event` names, recorded into the adapter row by the config-seeing call site (not a compiled event or clip). The installed hook command is whatever that key resolved to at the last `satelle init` (or heal). Changing the key without re-running init leaves the old event in the hooks file.

The wake is not unlimited. Where a harness caps in-turn continuations (grok: 8),
a verdict that arrives after the cap is spent has no in-turn wake; the catch-up
channel is the next prompt's `additionalContext`, and only claude and pi deliver
it. Grok discards it, so grok's verdict past the cap is delivered by resuming the
same session with the verdict as the prompt (`grok -p "<verdict>" --resume <id>`),
which the harness documents as the next user prompt: a fresh turn with a fresh
count, in the same session. The Stop hook counts every emission grok counts — a
block and a note alike — and says nothing at all about a gate that is still
running, since a still-running note is a continuation spent on nothing. That gate
is handed to one detached watcher, which claims the handle once it has finished
and resumes the session once; there is no status verb to poll. Claude has the
same path (cap 8, overridable by `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP`, resumed with
`claude -p "<verdict>" --resume <id>`), and a claude turn that ends through its
`StopFailure` or `SessionEnd` hook (`satelle hook turnend`) is recorded as closed
and its owed gates are resumed. The Stop hook delivers as soon as any gate the
session owns has finished — it never holds a finished verdict behind a slower
gate — and the next Stop waits for the rest. A turn that is still open and under
its cap is never resumed because it went quiet. A harness with no row is recorded
with an adapter-named "unavailable" for both, never claude's row.

What a wait cost the driver is read from `driver_usage` rows, not asserted:
`satelle story cost <id>` carries a CALLS column (the driving session's model
requests in each row's window; `—` where the harness reports none) and a GATE
WAIT table with the driver's model calls across each delivered hand-off. A wait
is bounded by a row taken as the command is issued, a row taken when the hook
delivers the verdict, and a row that has read the turn the wait happened in: grok
records a turn's usage only when the turn ends, so its wait reads `open` until a
later read (`story cost` settles it) sees that turn. The count is an upper bound
on the wait's own requests, never a zero for one that was not measured.

The Stop hook the scaffold writes carries a timeout longer than its wait
(`satelle init` raises an older, shorter one), so it answers instead of being
killed by the harness's default.

## Waiving one gate check — `satelle story waive` (sty_bef97244)

A gate that cannot be satisfied after the fact (evidence that can no longer be
produced) is closed by a **developer decision on record**, not by an agent's
say-so. `satelle story waive <id> --gate <skill> --check <name> --reason <text>`
appends one `gate_waiver` ledger row, actor `developer`, only after the developer
retypes the story id on the controlling terminal. It is refused, with no row,
for an agent session (`SATELLE_SCRATCH`, `SATELLE_SESSION` or a harness marker)
and when no terminal can be opened — there is no stdin fallback. `satelle ledger
append` refuses the `gate_waiver` kind, so this command is the only writer;
`satelle ledger list --kind gate_waiver` reads them.

A waiver reaches only that story's payload for the gate whose skill it names, as
`waivers` (absent when none). The gate's **rubric** decides whether to honour it
for the named check, and lists what it honoured in its verdict's `waived`, which
the `review_accept`/`review_reject` row records; a `waived` entry naming a check
with no payload waiver is dropped. A waiver covers one check, never the gate.

Honest posture: this raises the cost of a forged waiver and leaves a trail; it is
not a sandbox. An agent with a pty-spawning tool, or with write access to the
ledger store itself, could still fake the confirmation.
