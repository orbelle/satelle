// Package ledger is satelle's append-only event log — the "evidence"
// dynamic primitive. It records what happened to stories and tasks (created,
// updated, status changes, comments) as immutable rows.
//
// Ported from satellites' internal/ledger, reimplemented against sqlite
// (modernc.org/sqlite, no cgo) with the Postgres-specific surface dropped:
// no append-only triggers (no UPDATE method exists and the one deletion is
// Store.RemoveEvents, a guarded repair of a seeded fork that was never published,
// so the table is append-only by construction), no AppendMany/EngagementSeqExists (server-sync
// concerns, off in the MVP), and `?` placeholders instead of `$N`. SQL is kept
// libSQL-compatible so a future driver swap is mechanical.
package ledger

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"uuid"
)

// Kind discriminators for the canonical entry shapes the work layer emits.
// Callers may append entries of any kind; these are the names satelle uses.
const (
	KindStoryCreated = "story_created"
	KindStoryUpdated = "story_updated"
	KindTaskCreated  = "task_created"
	KindTaskUpdated  = "task_updated"
	KindComment      = "comment"
	// Quality-management spine: a gated transition records the request and its
	// verdict; every enacted status change records a transition. These feed the
	// progress (stages) column and the status gate badge.
	KindStatusTransition = "status_transition"
	// KindStatusReconcile records that a story row's status was repaired from
	// the last status_transition (the row is a projection of the ledger).
	KindStatusReconcile = "status_reconcile"
	// KindChainMerge records that `sync workstate rechain` merged a story's
	// forked local chain onto its hosted chain (sty_f321c29e). Payload:
	// {fork_base, hosted_latest, merged, excluded:[{id,from,to}], dropped}.
	// The fold ignores it.
	KindChainMerge   = "chain_merge"
	KindReviewAccept = "review_accept"
	KindReviewReject = "review_reject"
	// KindPerformerReject is a readiness round's performer verdict when it did not
	// accept: the performer rejected the premise, or failed to run. It is written
	// beside the review rows, under the same `attempt` id, so a round whose gate
	// never ran is still a round the counters see (sty_a32bd6a9).
	KindPerformerReject = "performer_reject"
	// KindReadinessAccept records that a readiness round was accepted — the
	// performer did not reject and every reviewer the step declares accepted. It
	// names the attempt, so a later presentation can reuse it (KindReadinessReuse)
	// and a reader can ask whether readiness still holds (sty_a32bd6a9).
	KindReadinessAccept = "readiness_accept"
	// KindReadinessReuse records that a presented readiness edge did not run its
	// performer or reviewers because the readiness accepted in the attempt it
	// names still holds.
	KindReadinessReuse = "readiness_reuse"
	// KindStoryDocAttached records a document attached to a story or task. Its
	// payload names the document, which is how a reader tells a plan attached
	// after an accepted readiness round.
	KindStoryDocAttached = "story_doc_attached"
	// KindGateSkipped records that an edge DECLARED a gate whose skill does not
	// resolve, so the transition advanced with no reviewer and no verdict. It is
	// deliberately its own kind: folding it into a comment would bury it, and
	// folding it into an accept would assert a judgement that never happened.
	// A trail carrying this row says "this advance was not judged" (sty_d59ec6a9).
	KindGateSkipped = "gate_skipped"
	// KindStepSummary is the summariser's prose recap of an enacted transition.
	KindStepSummary = "step_summary"
	// KindDriveStopped records that a headless story drive ended short of its
	// route's exit and could not park the story: the reason, the step and
	// performer it stopped on, and the performer's last message. A park the drive
	// did set is a comment row (source=drive) instead; the server alerts on both.
	KindDriveStopped = "drive-stopped"
	// KindAgentInvocation records HOW an isolated agent was invoked for a step —
	// the agent role, the resolved command/harness, and the injected-context source
	// (the skill/rubric file) — so the timeline shows what command ran with what
	// context, not just the verdict (sty_fb3e0873).
	KindAgentInvocation = "agent_invocation"
	// KindOverlap records that a sync pass found two holders on one story: its
	// local and hosted copies disagreed, or a push was refused by a hold another
	// location or user owns. Payload {local_holder, remote_holder, remote_user,
	// reason, source: conflict|hold}. A record of the overlap, never an arbiter of
	// it (sty_9914df10).
	KindOverlap = "overlap"
	// Estimate/actual: an agent self-reports a plan estimate at begin-work and the
	// actual cost at close. Recorded as story tags (estimate-*/actual-*) and as
	// these ledger rows so the per-story close-out can compare estimate vs actual.
	KindEstimateRecorded = "estimate_recorded"
	KindActualRecorded   = "actual_recorded"
	// KindStepCost records a single STEP's self-reported actual token cost and/or
	// its per-step estimate (`satelle story step-cost`) — the only way to attribute
	// cost to an IN-LOOP step, whose tokens the CLI (a subprocess of the driving
	// session) cannot introspect. Per-step wall-time is derived from transition
	// timestamps, not stored here. Payload carries numbers + the step name only —
	// never env/secrets (sty_3b2e55f5).
	KindStepCost = "step_cost"
	// KindWorkflowStamped records the workflow chosen to govern a story at create
	// (sty_3800ac23) — the choice satelle's gates read thereafter.
	KindWorkflowStamped = "workflow_stamped"
	// KindTelemetryEvent records a generic, typed quality/telemetry event: an
	// orchestrator self-report (e.g. an in-loop step's cost, retiring the narrow
	// KindStepCost verb) or a dispatch-engine outcome only the binary observes — a
	// reviewer/executor retry, failure, or timeout on a killed/timed-out
	// subprocess. Payload shape: {"kind": "<event-kind>", "data": {...}} — data
	// carries typed fields only, never env/secrets (sty_b73c3236).
	KindTelemetryEvent = "telemetry_event"
	// KindEngagementBaseline records the git HEAD (and dirty-worktree flag) at
	// first entry into a performing/engaging state so gates can enumerate
	// diff-since-engagement (sty_da169e03 / epic:scope-integrity). Enumeration
	// only — no pass/fail in Go.
	KindEngagementBaseline = "engagement_baseline"
	// KindWorkingOn records that a location took a fresh engagement seat on a
	// story (sty_40853d02): Payload {location, label, session, worktree, state}.
	// It is visibility only — shown on `story get`/`story list` and the
	// session-start advisory so other machines and team members see who is on a
	// story. Hosted-mode cache retention also reads it, to keep the history of
	// the stories this machine engaged (sty_0dc600e1). No code path reads it to
	// refuse anything.
	KindWorkingOn = "working_on"
	// KindTrunkCheck records what the start-of-work trunk check found when a
	// story first entered an engaging state (sty_9f3e51d1): body is the line
	// shown to the operator, payload the trunk report. Never a verdict.
	KindTrunkCheck = "trunk_check"
	// KindTrunkPublish records what `satelle trunk publish --story` did
	// (sty_6af229f1): body is the published line, payload the publish report
	// with the pushed and combined heads. Never a verdict.
	KindTrunkPublish = "trunk_publish"
	// KindReleaseRecord records that a release shipped a story (`satelle story
	// release-record`): body is "release <release> @ <commit>", payload the
	// commit, release, configured fields and recording session. Written against
	// any story, done included, and never changes its status. Never a verdict.
	KindReleaseRecord = "release_record"
	// KindSuiteRun records ONE run of an expensive verification suite as
	// SHA-keyed evidence: {sha, command, outcome, started_at, finished_at} in
	// Payload, StoryID being the recording story (sty_183a0510). Sibling stories
	// cite it rather than re-running the suite. Enumeration only — no pass/fail
	// in Go; a gate's check block decides whether a cited run is acceptable.
	KindSuiteRun = "suite_run"
	// KindSuiteCitation records that a story RIDES a suite_run recorded elsewhere.
	// Refs carries {"suite_run": "<entry id>"} — the first use of Entry.Refs
	// (sty_183a0510). Enumeration only — no pass/fail in Go.
	KindSuiteCitation = "suite_citation"
	// KindChangeRecord records the set of files changed during a closed step at
	// the enacted transition (sty_948ad5df). Payload is paths and counts only —
	// never file content (the patch rides a type:change story attachment, local
	// only). Enumeration only — no pass/fail in Go.
	KindChangeRecord = "change_record"
	// KindDefinitionAmended records a gate-approved amendment of a story's frozen
	// definition fields (sty_81aa4d8f): Payload carries {reason, skill, fields:
	// [{field, old, new}]} so the trail shows WHAT the definition was before the
	// correction, not merely that one happened. Enumeration only — the accept/
	// reject decision belongs to the authored amend_review gate.
	KindDefinitionAmended = "definition_amended"
	// KindDefinitionEdited records one field of a story's definition (title, body,
	// acceptance_criteria, category) edited while the route still leaves it
	// editable (sty_5262592e): Payload {field, before, after} with the row's Actor
	// as the editor. One row per changed field. Unlike KindDefinitionAmended no
	// gate judged the edit at the time — the intent review reads these rows on the
	// next presentation, and a definition-unchanged check reads them after
	// readiness accepted. Enumeration only.
	KindDefinitionEdited = "definition_edited"
	// KindSessionModel records one session role's captured model for a story —
	// the in-loop engaging session or the story-creating session
	// (an old row under the retired orchestrator role is ignored) (sty_7069bced /
	// epic:model-selection order:3). Payload: {role, model, executable}. model
	// is "unknown" when the harness reported none. Enumeration only: the
	// model-selection resolver reads the latest row per role and falls through
	// on "unknown"/absent — no pass/fail in Go.
	KindSessionModel = "session_model"
	// KindAgentMessage is a directed, role-addressed agent message on a story
	// (sty_2db624d0 / epic:agent-messaging). Payload: {from, to, body,
	// engagement_sha}. Not KindComment — a directed engagement-scoped row is
	// not a human comment.
	KindAgentMessage = "agent_message"
	// KindMessageMark records one state change of an agent_message
	// (sty_73c9aa10): Refs {message: msg_id}, Payload {state, answer_id,
	// by_session_id}. Append-only — the message's current state is the fold of
	// its marks, the latest winning (internal/board). A bookkeeping row about
	// delivery, never evidence: verb.isSelfLedgerKind excludes it.
	KindMessageMark = "message_mark"
	// KindTidy / KindTidyRestore record one file `satelle story tidy` moved out
	// of the tree, and its later restore (sty_d74e9b1b). Payload: src, dst,
	// action — both absolute, so a restore needs nothing else.
	KindTidy        = "tidy"
	KindTidyRestore = "tidy_restore"
	// KindDriverUsage records the driving (in-loop) session's measured
	// token/dollar usage at engage, at every transition, and at park/close
	// (sty_81caa41b) — captured from that harness's OWN session record
	// (agentcli.SessionUsageSnapshot), unlike KindAgentInvocation which records
	// a dispatched one-shot's usage. Payload is verb.DriverUsagePayload: the
	// delta since the previous driver_usage row for the same session, or an
	// adapter-named unavailable reason. Append-only, never rewritten — a
	// row's WindowKey plus Cumulative decide whether a later snapshot is a
	// no-op (AC7). Enumeration only — no pass/fail in Go.
	KindDriverUsage = "driver_usage"
	// KindToolPermission records one tool-permission decision (tool/kind/
	// decision/decided_by) for a live session — moved off KindAgentInvocation
	// (sty_8eae81ac AC4), which it used to inflate: a permission event carries
	// no usage, so counting it as an invocation made every cost/unmeasured-row
	// view over-count. See IsToolPermissionRow for the read-time filter that
	// also excludes the legacy rows written before this kind existed.
	KindToolPermission = "tool_permission"
	// KindSkillDelivery records the step skills satelle delivered to the DRIVING
	// session when a story entered a step that session performs (sty_23ceeca2):
	// Payload {from, to, attempt?, kind, skills:[{name, hash, delivered, reason?}]}.
	// It is deliberately not a KindAgentInvocation row: nothing was invoked and no
	// usage was measured, so the cost and unmeasured-row views must not count it
	// (the lesson KindToolPermission already records). A dispatched performer's
	// delivery rides its own agent_invocation row instead. Enumeration only.
	KindSkillDelivery = "skill_delivery"
	// KindToolTrace records one tool call the in-loop driving session made while a
	// story was engaged, as the gate hooks saw it (sty_23ceeca2): Payload
	// {harness, tool, command?} — the command's head only. It is the in-loop
	// run trace the skill-evidence check reads; a harness whose hooks do not
	// see a tool writes no row for it, which the check reports as unverifiable.
	// Not an invocation and not a verdict: enumeration only.
	KindToolTrace = "tool_trace"
	// KindFixClaim records one in-loop-fix claim (sty_4b694872) — the typed
	// exception the fix lane grants — BEFORE the edit it licenses. Payload is
	// fixlane.ClaimPayload: path, reason, bound_lines, proving_test and the
	// decision (recorded | refused, with the refused class). A refused claim is a
	// row too, so refusal is analysable, not merely silent. Enumeration only —
	// the class decision is the configured bound's, applied by internal/fixlane.
	KindFixClaim = "fix_claim"
	// KindFixClaimUse records the ONE edit a recorded claim licensed. Refs carries
	// {"claim": "<fix_claim entry id>"}; a claim with a use row is consumed.
	KindFixClaimUse = "fix_claim_used"
	// KindGateWaiver records a developer's waiver of ONE named check of ONE gate on
	// ONE story (sty_bef97244). Actor is "developer". It is a reserved kind
	// (IsReservedKind): only internal/waiver writes it, after the developer
	// retypes the story id on the controlling terminal, so no agent session can
	// produce one through `satelle ledger append`. Payload is waiver.Payload.
	// Enumeration only — whether a gate honours a waiver is that gate's rubric.
	KindGateWaiver = "gate_waiver"
	// KindSubstrateLockDeny records one edit the edit gate refused under the
	// substrate lock (sty_992cffc6): a story outside the substrate lane tried to
	// change locked substrate while it held a performing seat. Payload is
	// {path, status, lane}; nothing in it is harness-specific. Enumeration only —
	// the lock list is configuration.
	KindSubstrateLockDeny = "substrate_lock_deny"
	// KindSubstrateChangeNote records, on a story that held a performing seat, one
	// locked-substrate change a substrate-lane story made while it performed: the
	// lock exists so a story cannot rewrite its OWN judge, and a sibling's judge
	// changing mid-step is noted on the sibling rather than blocking the change.
	// StoryID is the noted story; Payload is {path, substrate_story, status}.
	// Enumeration only — the lock list is configuration.
	KindSubstrateChangeNote = "substrate_change_note"
	// KindSubstrateWritten records one locked-substrate write the edit gate
	// allowed for an engaged substrate-lane story, before the write runs: StoryID
	// is the writing story; Payload is verb.SubstrateWrittenPayload {root, path,
	// before_sha256}. It is what lets another story's change set leave out a file
	// that story provably did not write. Enumeration only.
	KindSubstrateWritten = "substrate_written"
	// KindSubstrateSeeded records the substrate files one `satelle init` run
	// created or changed, in the target repo's own ledger. It has no StoryID: init
	// runs under no story. Payload is verb.SubstrateSeedPayload {verb, root,
	// files:[{path, before_sha256, sha256, before_mtime}]}. A story's change set
	// leaves out bytes init wrote that the story did not touch. Enumeration only.
	KindSubstrateSeeded = "substrate_seeded"
	// KindSubstrateBaseline records the content hash of every substrate file at a
	// story's first engagement: Payload is verb.SubstrateBaselinePayload {root,
	// files:{path: sha256}}. A gate-recorded write by another story is measured
	// against it, so an unrecorded change made before that write keeps the file in
	// this story's change set. Enumeration only.
	KindSubstrateBaseline = "substrate_baseline"
	// KindRemoteRun records one step a remote ran on its host (sty_2f3baf0a): the
	// join between a story and whatever the step changed. StoryID is the story;
	// Refs is {fire_id, story, run_id}; Payload is {remote, version, host, label,
	// step, run_id, started, ended} plus error when the run failed. Never an
	// environment value, argv or credential. Enumeration only.
	KindRemoteRun = "remote_run"
	// KindExecutionLink joins a story to an execution a remote node ran about it:
	// StoryID is the subject the wake named; Refs is {execution}; Payload is
	// {execution, task, remote, outcome}. It is written once, when the run
	// concludes, so it carries the outcome. Enumeration only.
	KindExecutionLink = "execution_link"
	// KindQueueEnqueued opens a story's queue entry: written when the story enters
	// a route wait step, in the same commit as the transition. StoryID is the
	// story; Payload is QueueEnqueuedPayload. The queue is these events, in the
	// order they were recorded; there is no other store.
	KindQueueEnqueued = "queue_enqueued"
	// KindQueueWithdrawn closes a story's open queue entry with a reason. StoryID
	// is the story; Payload is QueueWithdrawnPayload. Status is untouched.
	KindQueueWithdrawn = "queue_withdrawn"
	// KindExecutionStopped is the alert a drive writes on an execution's subject
	// story when the execution ended short of its route's exit (refused, failed,
	// timed out, or a performer that ended on a question): StoryID is the subject
	// the execution's story_id input names; Payload is the execution's
	// drive-stopped payload plus {execution, task}. It is written for
	// satelle-server to raise to the developer; the execution itself carries the
	// drive-stopped row.
	KindExecutionStopped = "execution_stopped"
	// KindRemoteWaitActed records that the local driver took the edge a step's
	// remote_wait maps a team remote's concluded run to, so a later drive does not
	// act on the same conclusion again. StoryID is the waiting story; Payload is
	// {remote, step, run_key, outcome, to}. Enumeration only.
	KindRemoteWaitActed = "remote_wait_acted"
	// KindCommandRule records one agent command a repo-authored
	// [[gate.command_rule]] matched and let run, or refused (sty_a74551d6).
	// Payload is {command, rule_index, pattern, action, decision, delivered,
	// status, step}. It is not a KindToolPermission row: that records a harness's
	// per-session permission decision, this records a repo rule judged against a
	// story's status. Enumeration only — the rules are configuration.
	KindCommandRule = "command_rule"
	// KindRecoveryChoice records the driver's decision after a dispatch ended
	// without a completion (sty_f0ed2473): Payload is {choice, reason, state, log,
	// last_event, file_count} with choice one of redispatch | finish | park. The
	// row records the decision only — it never dispatches, transitions or parks.
	// Enumeration only.
	KindRecoveryChoice = "recovery_choice"
	// KindRemoteEnrollRefused records a remote that tried to enroll on the
	// sealed channel and was refused (sty_b20b3956): Payload is {session, reason}
	// with reason one of token_used | token_expired | token_unknown |
	// key_mismatch. The token itself is never recorded. Enumeration only.
	KindRemoteEnrollRefused = "remote_enroll_refused"
	// KindRemoteChannelResumed records that an end of the sealed channel redialled
	// the relay after it dropped the connection and the channel resumed
	// (sty_ee78148f): Payload is {session, role, attempts}. Enumeration only.
	KindRemoteChannelResumed = "remote_channel_resumed"
	// KindRemoteChannelResumeFailed records that an end of the sealed channel gave
	// up redialling (sty_ee78148f): Payload is {session, role, reason, attempts}
	// with reason one of refused | expired | failed. Enumeration only.
	KindRemoteChannelResumeFailed = "remote_channel_resume_failed"
	// KindChainFork records that two or more events of one story deliberately
	// share a base (sty_41f61669): Refs is the JSON array of the forking event
	// ids. VerifyChain accepts a shared base only when such a record names every
	// event sharing it. Recognition only — nothing here writes one.
	KindChainFork = "chain_fork"
	// KindDefinitionSnapshot records a story's whole folded state — status,
	// definition, tags, park origin — as its row held it, for a story whose chain
	// predates the fold (sty_d84be8bc). It is appended once per story, linked into
	// the chain like any event, so the fold of an existing story equals its row.
	// Payload is storyfold.SnapshotPayload.
	KindDefinitionSnapshot = "definition_snapshot"
	// KindWorkflowVersion carries the workflow a story's events were made under
	// (sty_d84be8bc): the hash and the source that hashes to it — both route halves,
	// or the legacy workflow body — so a chain replays against the version each
	// event recorded without the developer's repo. Appended once per hash per
	// story, ahead of the first event that records the hash.
	KindWorkflowVersion = "workflow_version"
)

// RemoteRunPayload is the payload of a KindRemoteRun row: the one shape the
// recorder writes and the route reads. Fixed fields only; the times are RFC 3339.
type RemoteRunPayload struct {
	Remote  string `json:"remote"`
	Version string `json:"version"`
	Host    string `json:"host"`
	Label   string `json:"label"`
	Step    string `json:"step"`
	RunID   string `json:"run_id"`
	Started string `json:"started"`
	Ended   string `json:"ended"`
	Error   string `json:"error,omitempty"`
}

// QueueEnqueuedPayload is the payload of a KindQueueEnqueued row: the wait step
// entered, and the branch and head sha of the story's engagement tree at entry.
type QueueEnqueuedPayload struct {
	Story   string `json:"story"`
	State   string `json:"state"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
}

// QueueWithdrawnPayload is the payload of a KindQueueWithdrawn row.
type QueueWithdrawnPayload struct {
	Story  string `json:"story"`
	Reason string `json:"reason"`
}

// ExecutionLinkPayload is the payload of a KindExecutionLink row: the one shape the
// node writes and the route reads.
type ExecutionLinkPayload struct {
	Execution string `json:"execution"`
	Task      string `json:"task"`
	Remote    string `json:"remote"`
	Outcome   string `json:"outcome"`
}

// Entry is one row of the evidence ledger. StoryID/ProjectID are optional
// correlation ids — a row may be scoped to either, both, or neither.
type Entry struct {
	ID        string          `json:"id"`
	StoryID   string          `json:"story_id,omitempty"`
	ProjectID string          `json:"project_id,omitempty"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor,omitempty"`
	Body      string          `json:"body,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Refs      json.RawMessage `json:"refs,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	// BaseID names the story event this one builds on (the story's previous
	// entry when it was written); empty for a story's first event and for an
	// entry with no story. A story's events form one chain through it (sty_41f61669).
	BaseID string `json:"base_id,omitempty"`
	// WorkflowHash is the content hash (docindex Doc.Hash) of the workflow that
	// governed the story when the event was made, so a verdict can be re-checked
	// against the exact workflow that produced it. Empty when none applied or when
	// the event predates the field — an unknown version is never invented.
	WorkflowHash string `json:"workflow_hash,omitempty"`
}

// SortEntries orders es oldest-first in place: by CreatedAt, then by ID for rows
// stamped at the same instant — the order List returns, for rows read from
// anywhere else.
func SortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].CreatedAt.Equal(es[j].CreatedAt) {
			return es[i].ID < es[j].ID
		}
		return es[i].CreatedAt.Before(es[j].CreatedAt)
	})
}

// EXEMPTION (sty_7db2ed7d): the ledger's Actor field is the event-AUTHOR (who
// recorded the event — "executor"/"reviewer"), a distinct concept from the workflow
// node's performer keyword that the actor→agent rename targeted. It is persisted
// state — the `actor` SQLite column (internal/ledger/store.go) and JSON tag — read
// by every released binary, so it is deliberately kept as "actor" rather than
// migrated. This is a recorded internal exemption, not an oversight.

// NewID returns a fresh ledger-entry id in the evt_<8hex> form, visually
// distinct from sty_/tsk_ ids in tool output. NewV4 explicitly — see
// workitem.Kind.newID for why a truncated id may not ride on New's current
// algorithm (sty_5515036d).
func NewID() string { return fmt.Sprintf("evt_%s", uuid.NewV4().String()[:8]) }

// IsReservedKind reports whether kind is written only by its owning package and
// so refused by the generic `ledger append` verb (sty_bef97244). Reading it
// (`ledger list --kind`) is unaffected.
func IsReservedKind(kind string) bool {
	return kind == KindGateWaiver || kind == KindQueueEnqueued || kind == KindQueueWithdrawn
}

// AppendInput is the typed shape of one ledger insert. Kind is required; every
// correlation id and payload is optional.
type AppendInput struct {
	StoryID   string
	ProjectID string
	Kind      string
	Actor     string
	Body      string
	Payload   json.RawMessage
	Refs      json.RawMessage
	// WorkflowHash, when set, is kept as given; otherwise the store resolves the
	// governing workflow's hash for StoryID (SetWorkflowVersion).
	WorkflowHash string
}

// ListFilter parameterises List. At least one selectable field (StoryID,
// ProjectID, Kind) must be set — an unfiltered full-table scan is refused.
type ListFilter struct {
	StoryID   string
	ProjectID string
	Kind      string
	Limit     int // <=0 ⇒ DefaultListLimit, capped at MaxListLimit
	// AfterID pages: only rows after the row with this id in (created_at, id)
	// order, so a reader of a story with more than 2000 rows can walk all of it.
	AfterID string
}
