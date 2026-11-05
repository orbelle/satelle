package verb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Story and task are the same primitive (workitem) distinguished by kind, so
// the create/list handlers are produced per-kind by a factory and the
// kind-agnostic get/set handlers are shared. Each is registered under both the
// "story-" and "task-" name prefixes so the CLI groups map 1:1.
func init() {
	for _, kind := range []workitem.Kind{workitem.KindStory, workitem.KindTask, workitem.KindExecution} {
		group := string(kind)
		Register(&Verb{Name: group + "-create", Description: "Create a " + group, Invoke: workItemCreate(kind), DispatchesReviewer: dispatchesOnGatedCreate})
		Register(&Verb{Name: group + "-list", Description: "List " + group + "s", Invoke: workItemList(kind)})
		Register(&Verb{Name: group + "-get", Description: "Get a " + group + " by id", Invoke: workItemGet})
		Register(&Verb{Name: group + "-set", Description: "Update a " + group, Invoke: workItemSet, DispatchesReviewer: dispatchesOnStatusChange})
	}
	// Estimate/actual are story-only: an agent records the plan estimate at
	// begin-work and the actual cost at close, scoped to the story.
	// Restamp is story-only too: tasks/executions are unstamped by design
	// (sty_3800ac23 / sty_ef08ce2a) — they resolve their workflow at gate time.
}

// createReq is the request body for story-create / task-create.
type createReq struct {
	Title              string   `json:"title"`
	Body               string   `json:"body,omitempty"`
	Status             string   `json:"status,omitempty"`
	Priority           string   `json:"priority,omitempty"`
	Category           string   `json:"category,omitempty"`
	ParentID           string   `json:"parent_id,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	// Inputs are the raw name=value strings of `execution create --input`; they
	// are checked against the parent task's declarations (workitem.CoerceInputs).
	Inputs map[string]string `json:"inputs,omitempty"`
}

// workflowStamp returns the workflow:<name> stamp carried in tags, or "" when
// un-stamped.
func workflowStamp(tags []string) string {
	for _, t := range tags {
		if strings.HasPrefix(t, "workflow:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "workflow:"))
		}
	}
	return ""
}

// hasWorkflowStamp reports whether tags already carry a workflow:<name> stamp, so
// an explicit caller-supplied workflow tag is never duplicated.
func hasWorkflowStamp(tags []string) bool { return workflowStamp(tags) != "" }

// createOrigin says who a created work item is recorded as created by: the
// actor and source of its story_created row and the creator model, executable,
// session and unknown-reason of its creator session_model row. The CLI create
// leaves Actor and Source empty (its row is actor-less with no payload) and
// records the driving session; a child a readiness performer reported records
// the performer, never the driving session (sty_af0b1761).
type createOrigin struct {
	Actor     string
	Source    string
	Model     string
	ModelExe  string
	SessionID string
	Reason    string
}

// sessionCreateOrigin is the origin of a work item created by the driving
// session (sty_7069bced): config.SelectModel's creator tier reads the latest
// session_model{creator} row for a story. There is no separate "creator"
// publish: the creating session IS the in-loop session at creation time, so
// this reads its already-published in-loop model, which createWorkItem
// re-records under the creator role for THIS story. An unresolved session or
// model records "unknown" via RecordSessionModel rather than skipping the write.
func sessionCreateOrigin() createOrigin {
	sid := config.ResolveSession()
	model, exe, reason := config.ResolveSessionModel(sid, SessionModelRoleInLoop)
	return createOrigin{Model: model, ModelExe: exe, SessionID: sid, Reason: reason}
}

func workItemCreate(kind workitem.Kind) func(context.Context, json.RawMessage) (json.RawMessage, error) {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var req createReq
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		return createWorkItem(ctx, kind, req, sessionCreateOrigin())
	}
}

// createWorkItem is the one create path: the CLI create and a child a readiness
// performer reported both pass through it, so a story created either way meets
// the same rules (tag and category vocabulary, the create review, the workflow
// stamp, closed-epic tags, depends-on, the start-of-work check).
// It returns the created item in the verb's wire form (JSON).
func createWorkItem(ctx context.Context, kind workitem.Kind, req createReq, origin createOrigin) (json.RawMessage, error) {
	p, err := prepareCreate(ctx, kind, req, origin)
	if err != nil {
		return nil, err
	}
	it, err := p.store(ctx)
	if err != nil {
		return nil, err
	}
	return p.finish(ctx, it)
}

// preparedCreate is a create that passed every rule that can refuse it and has
// written nothing to the work-item store yet. Splitting the create here lets a
// batch (the children a performer reported) validate and review every draft
// before the first one is stored, so one refused child leaves none behind.
type preparedCreate struct {
	kind       workitem.Kind
	req        createReq
	origin     createOrigin
	tags       []string
	createDec  GateDecision // recorded on the ledger once the story has an id
	createTo   string
	stamped    string
	ledgerKind string
	at         time.Time
	inputs     map[string]any // an execution's typed input values, checked against its task
}

// prepareCreate runs every check a create makes before the store write: the tag
// and category vocabulary, the create review, the workflow stamp, closed-epic
// tags, depends-on, and the single-story and start-of-work trunk checks.
func prepareCreate(ctx context.Context, kind workitem.Kind, req createReq, origin createOrigin) (*preparedCreate, error) {
	p := &preparedCreate{kind: kind, origin: origin, ledgerKind: ledger.KindStoryCreated, at: time.Now()}
	if kind == workitem.KindTask || kind == workitem.KindExecution {
		p.ledgerKind = ledger.KindTaskCreated
	}
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	now := p.at

	// Typed inputs: only an execution carries values, and they must satisfy what
	// its task declares. Refused before anything is reviewed or stored.
	if kind == workitem.KindExecution {
		var decls []workitem.InputDecl
		if task, terr := store.Get(ctx, req.ParentID); terr == nil {
			decls = task.Inputs
		}
		if p.inputs, err = workitem.CoerceInputs(decls, req.Inputs); err != nil {
			return nil, err
		}
	} else if len(req.Inputs) > 0 {
		return nil, fmt.Errorf("--input applies only to an execution")
	}

	// Stamp the GOVERNING workflow on the story at create (sty_3800ac23): the
	// chosen workflow is recorded as a workflow:<name> tag so gating reads the
	// choice thereafter rather than re-deriving it by category. Independent of
	// create-gating — a story is stamped whenever a workflow governs it.
	// Only STORIES are stamped. A task is an authored header with no running
	// lifecycle, and an execution resolves its workflow KIND-awarely at gate
	// time (sty_ef08ce2a) — stamping either risks pinning the wildcard story
	// workflow onto a task/execution, exactly what this epic forbids.
	// Resolved before the create review so the readiness-mark refusal below can
	// judge the draft against the route that will govern it.
	tags := req.Tags
	if kind == workitem.KindStory && workflowResolver != nil && !hasWorkflowStamp(tags) {
		wf, werr := workflowResolver.WorkflowNameFor(ctx, req.Category)
		if werr != nil {
			return nil, werr
		}
		if wf != "" {
			p.stamped = wf
			tags = append(tags, "workflow:"+wf)
		}
	}

	// Required-structure gate: when a repo opts in, an isolated reviewer
	// judges the draft before it is persisted (the deterministic
	// structure.Story check — including the category rule, sty_af239840 —
	// runs first inside it). A bare create stays legal by design: stories
	// start as stubs and the WORKFLOW gates enforce structure progressively;
	// `satelle reindex` reports non-conformant open stories as warnings.
	p.createTo = req.Status
	if p.createTo == "" {
		p.createTo = workitem.StatusBacklog
	}
	if createReviewer != nil {
		dec, gerr := createReviewer.ReviewCreate(ctx, CreateDraft{
			Kind:               string(kind),
			Title:              req.Title,
			Body:               req.Body,
			AcceptanceCriteria: req.AcceptanceCriteria,
			Priority:           req.Priority,
			Category:           req.Category,
			Tags:               req.Tags,
		})
		if gerr != nil {
			return nil, gerr
		}
		if dec.Gated && !dec.Accept {
			// A rejected create persists no story, so its run is recorded
			// repo-scoped (empty story id) where the gate-value view reads it.
			recordLifecycleReview(ctx, "", "create_review", "create", p.createTo, dec, now)
			return nil, fmt.Errorf("%s rejected by %s: %s", kind, dec.Skill, dec.Notes)
		}
		p.createDec = dec
		if dec.Gated {
			RecordVerdict(fmt.Sprintf("accepted %s create by %s: decision=accept notes=%s", kind, dec.Skill, dec.Notes))
		}
	}

	// Single-story process rule (sty_c7149f8a): refuse creating a story already
	// in an engaging status when another story occupies that seat. Default
	// create status is backlog (not engaging) — no-op unless create opts a
	// non-default status.
	if kind == workitem.KindStory {
		status := req.Status
		if status == "" {
			status = workitem.StatusBacklog
		}
		// ParentID rides along because the seat arbitration key may be
		// derived from it (sty_c098dc2d) — a provisional without it would
		// probe under the wrong key.
		provisional := workitem.Item{
			Kind: workitem.KindStory, Status: status, Category: req.Category, Tags: tags,
			Title: req.Title, ParentID: req.ParentID,
		}
		if err := refuseSecondEngagingStory(ctx, "", status, provisional); err != nil {
			return nil, err
		}
	}
	p.req = req
	p.tags = tags
	return p, nil
}

// store writes the prepared item to the work-item store and nothing else.
func (p *preparedCreate) store(ctx context.Context) (workitem.Item, error) {
	store, err := requireWorkItem()
	if err != nil {
		return workitem.Item{}, err
	}
	it, err := store.Create(ctx, workitem.CreateInput{
		Kind:               p.kind,
		Title:              p.req.Title,
		Body:               p.req.Body,
		Status:             p.req.Status,
		Priority:           p.req.Priority,
		Category:           p.req.Category,
		ParentID:           p.req.ParentID,
		AcceptanceCriteria: p.req.AcceptanceCriteria,
		Tags:               p.tags,
		Assignee:           resolveAssignee(),
		InputValues:        p.inputs,
	}, p.at)
	if err != nil {
		// The reviewer ran and was paid for; keep its spend repo-scoped.
		recordLifecycleReview(ctx, "", "create_review", "create", p.createTo, p.createDec, p.at)
		return workitem.Item{}, err
	}
	return it, nil
}

// finish records the created item's ledger rows and returns it in wire form.
func (p *preparedCreate) finish(ctx context.Context, it workitem.Item) (json.RawMessage, error) {
	now := p.at
	kind := p.kind
	recordLifecycleReview(ctx, it.ID, "create_review", "create", it.Status, p.createDec, now)
	// Create-into-engaging: claim the seat now that the id exists (sty_8426b9c0).
	_, _, aerr := acquireEngagementLease(ctx, it, it.Status)
	if aerr != nil {
		// Best-effort: item was created; surface the seat error so the operator
		// knows the create raced — rare (pre-check already refused when possible).
		return nil, aerr
	}
	if kind == workitem.KindStory {
		// A story's creation carries its whole definition and initial status: the
		// fold starts from it (sty_d84be8bc).
		_ = AppendStoryCreated(ctx, ledgerStore, it, p.origin.Actor, p.origin.Source, now)
		// Created straight into a performing state is a move into it (sty_9c782f0e).
		recordWorkingOn(ctx, it, it.Status, now)
	} else {
		var createdPayload json.RawMessage
		if p.origin.Source != "" {
			createdPayload, _ = json.Marshal(map[string]string{"source": p.origin.Source})
		}
		appendLedgerEntry(ctx, it.ID, p.ledgerKind, p.origin.Actor, fmt.Sprintf("created %s %q", kind, it.Title), createdPayload, now)
	}
	if kind == workitem.KindStory {
		RecordSessionModel(ctx, it.ID, "", SessionModelRoleCreator, p.origin.Model, p.origin.ModelExe, p.origin.SessionID, p.origin.Reason)
	}
	if p.stamped != "" {
		appendLedger(ctx, it.ID, ledger.KindWorkflowStamped,
			fmt.Sprintf("governing workflow: %s", p.stamped), now)
	}
	// A task/execution is authored substrate — materialise its file (the source
	// of truth): a task header flat, an execution run under its task's folder
	// (sty_c1f9e74c, sty_ef08ce2a). A no-op for stories.
	if err := writeItemFile(it); err != nil {
		return nil, fmt.Errorf("write item file: %w", err)
	}
	notifyChange(panelTopic(kind))
	return json.Marshal(it)
}

// panelTopic maps a work-item kind to its realtime panel topic.
func panelTopic(kind workitem.Kind) string {
	if kind == workitem.KindTask || kind == workitem.KindExecution {
		return TopicTasks
	}
	return TopicStories
}

// listReq is the request body for story-list / task-list.
type listReq struct {
	Status   string `json:"status,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Tag      string `json:"tag,omitempty"` // exact tag; multi-value ANY-match (sty_f7115cd2)
	Limit    int    `json:"limit,omitempty"`
	// Archived lists only archived items (sty_28665836); the default list hides them.
	Archived bool `json:"archived,omitempty"`
}

func workItemList(kind workitem.Kind) func(context.Context, json.RawMessage) (json.RawMessage, error) {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		store, err := requireWorkItem()
		if err != nil {
			return nil, err
		}
		var req listReq
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		items, err := store.List(ctx, workitem.ListFilter{
			Kind:     kind,
			Status:   req.Status,
			ParentID: req.ParentID,
			Tag:      req.Tag,
			Limit:    req.Limit,

			ArchivedOnly: req.Archived,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(items)
	}
}

// idReq is the request body for verbs addressing a single item by id.
type idReq struct {
	ID string `json:"id"`
}

func workItemGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if _, err := requireWorkItem(); err != nil {
		return nil, err
	}
	var req idReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, fmt.Errorf("verb: id required")
	}
	// A story's status, definition and park origin are the fold of its chain; the
	// row is only the cache listing reads (sty_d84be8bc).
	it, err := storyState(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(it)
}

// setReq is the request body for story-set / task-set. Pointer fields give
// partial-update semantics: a field absent from the JSON stays unchanged.
// Tags is full-replace. AddTags/RemoveTags are additive (sty_033d4611) and must
// not be combined with Tags in one call.
type setReq struct {
	ID                 string    `json:"id"`
	Title              *string   `json:"title,omitempty"`
	Body               *string   `json:"body,omitempty"`
	Status             *string   `json:"status,omitempty"`
	Priority           *string   `json:"priority,omitempty"`
	Category           *string   `json:"category,omitempty"`
	ParentID           *string   `json:"parent_id,omitempty"`
	AcceptanceCriteria *string   `json:"acceptance_criteria,omitempty"`
	Tags               *[]string `json:"tags,omitempty"`
	AddTags            []string  `json:"add_tags,omitempty"`
	RemoveTags         []string  `json:"remove_tags,omitempty"`
}

// workItemSet is the update verb. A refused status change is annotated with the
// worktree-divergence report (sty_d6e209aa) here, at the one seam every refusal
// of the transition passes through, so no refusal site needs its own copy.
func workItemSet(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return workItemSetCore(ctx, raw)
}

func workItemSetCore(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req setReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, fmt.Errorf("verb: id required")
	}
	now := time.Now()

	// Resolve the current item so we can detect a status transition and gate it
	// before anything is enacted.
	//
	// Write ordering (sty_2c71eff6). This snapshot is read HERE, but the write it
	// feeds lands far below — after the reviewer gate (minutes) and after the
	// named-agent dispatch (minutes more) have run. In that window the dispatched
	// agent holds the PRE-transition row (status still the FROM state) and may
	// itself shell `satelle story set`; its write, derived from that stale
	// snapshot, would otherwise land unconditionally and drag status back to the
	// FROM state while the ledger already records the transition. Every write
	// below therefore carries ExpectStatus = current.Status: a compare-and-set, so
	// the loser of the race is refused loudly instead of silently winning.
	current, err := storyStateHealed(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	// Tag mutation algebra (sty_033d4611): full-replace (--tags) is exclusive of
	// additive add/remove. Combined add+remove applies removes first, then adds.
	if req.Tags != nil && (len(req.AddTags) > 0 || len(req.RemoveTags) > 0) {
		return nil, fmt.Errorf("verb: tags (full replace) cannot be combined with add_tags/remove_tags — use one mode")
	}
	if len(req.AddTags) > 0 || len(req.RemoveTags) > 0 {
		merged := applyTagMutation(current.Tags, req.AddTags, req.RemoveTags)
		req.Tags = &merged
	}
	// Definition freeze (sty_b572537f): once a STORY passes its workflow's freeze
	// point, title/body/acceptance_criteria/category are immutable. The point is
	// route configuration — the step that declares `freeze`, else the entry state
	// (Mdiamond / Spec.Start), never a hardcoded status name (sty_5262592e).
	// Status, tags, priority, estimate/actual, and attachments still flow.
	// Fail-closed when the rule cannot be resolved so a broken deployment cannot
	// silently permit an anti-gaming definition edit. An edit that IS still
	// permitted is recorded on the ledger once it commits (recordDefinitionEdits).
	var definitionEdits []AmendField
	if current.Kind == workitem.KindStory {
		if frozen := definitionFieldsChanged(current, req); len(frozen) > 0 {
			editable, boundary, ok := storyDefinitionEditable(ctx, current)
			if !ok {
				return nil, fmt.Errorf(
					"satelle: refusing to change definition fields [%s] on %s — cannot resolve the story's workflow entry state (fix config and retry)",
					strings.Join(frozen, ", "), current.ID)
			}
			if !editable {
				return nil, fmt.Errorf(
					"satelle: refusing to change frozen definition field(s) [%s] on engaged story %s (status %q; %s) — title/body/acceptance_criteria/category are immutable once a story passes that point; status/estimate/actual/tags/priority/attachments are unaffected.",
					strings.Join(frozen, ", "), current.ID, current.Status, boundary)
			}
		}
	}

	transitioning := req.Status != nil && *req.Status != current.Status
	// Same-status re-engage (sty_4f74d01f): when the story is already performing
	// but its seat lease was dropped, `story set --status <same>` must claim a
	// lease (or refuse loudly) — never report plain success with no seat. A task
	// execution re-engages the same way (sty_7f93ae45): the edit gate's dropped-
	// seat advice names this command for every kind.
	sameStatusReengage := false
	if !transitioning && req.Status != nil && current.Kind.Leasable() {
		eng, ok := storyStatusIsEngaging(ctx, current, *req.Status)
		if ok && eng {
			sameStatusReengage = true
		} else if current.Kind == workitem.KindExecution {
			return nil, unseatableExecutionError(current, *req.Status, ok)
		}
	}

	// Edge fence (sty_ebd3d666): refuse status jumps that skip a required DOT
	// step (e.g. backlog→in_progress when only backlog→plan exists). Runs before
	// engage/lease so a skip is never partially acquired. Declared recovery,
	// park, and cancel edges still pass.
	if transitioning {
		if err := refuseReopen(ctx, current, *req.Status); err != nil {
			return nil, err
		}
		if err := refuseSkippedStep(ctx, current, *req.Status); err != nil {
			return nil, err
		}
	}

	// Engage precondition (sty_93eec36d): when a story leaves its workflow entry
	// state for a non-cancel target, refuse if agents.toml / workflow agent=
	// bindings fail agentvalidate — catch a broken deployment before the first
	// dispatched step, not mid-transition. Cancel stays open.
	if transitioning && current.Kind == workitem.KindStory {
		if err := refuseBrokenAgentsOnEngage(ctx, current, *req.Status); err != nil {
			return nil, err
		}
	}

	// Engagement lease (sty_8426b9c0 / sty_1738f973): acquire the seat BEFORE
	// gate+dispatch so concurrent engages and edit/commit gates see the holder
	// during the whole window. Shape still decides engaging; the lease is WHO
	// holds it. A deferred abort guard releases a NEWLY acquired seat (or clears
	// in_flight on sequential continuation) on ANY return path that does not
	// settle — gate reject, dispatch abort, panic-recoverable exit, Update error.
	// SIGKILL still cannot run defers; HeartbeatTTL + lease.Alive covers that residual.
	// settled is set only after force-release (exit) or Confirm (engaging commit).
	// sameStatusReengage also acquires (sty_4f74d01f) without running gate/dispatch.
	acquiredThisCall := false
	settled := true // non-transitioning / non-lease paths never need abort release
	// Every leasable kind leases on its moves, an execution included: its lease
	// is how the edit and commit gates see a run at its performing state as
	// engaged (sty_931026b3).
	leaseOnMove := transitioning && current.Kind.Leasable()
	if leaseOnMove || sameStatusReengage {
		// Assignee notice (sty_8ccaa906, sty_40853d02): an assignment says who is
		// expected to work the story; it never blocks an engage. Non-engaging moves
		// (park, cancel, field edits) say nothing.
		if eng, ok := storyStatusIsEngaging(ctx, current, *req.Status); ok && eng {
			if err := runEngageGuard(ctx); err != nil {
				return nil, err
			}
			noticeOtherAssignee(os.Stderr, current, resolveAssignee())
		}
		acq, inFlight, aerr := acquireEngagementLease(ctx, current, *req.Status)
		if aerr != nil {
			return nil, aerr
		}
		if sameStatusReengage {
			// Re-seat path: grant lease (or keep existing) and settle immediately —
			// no gate, no dispatch. Other field updates may still apply below.
			if acq {
				confirmEngagementLease(ctx, current.ID, *req.Status)
				// A re-seat is no move; it is presence only when it took a seat.
				recordWorkingOn(ctx, current, *req.Status, now)
			}
			// inFlight / AlreadyHeld: seat already held — good enough for AC2.
			// An execution that came away with neither, and holds no live lease,
			// was not seated: say so rather than print success (sty_7f93ae45).
			if !acq && !inFlight && current.Kind == workitem.KindExecution && !hasLiveLease(ctx, current.ID) {
				return nil, fmt.Errorf("satelle: could not seat %s at %q: no engagement lease was granted (the lease store is unavailable, or the item waits on open children) — `satelle story seat` lists no holder for it, so a commit or push from its tree stays refused", current.ID, *req.Status)
			}
			// A named one-shot performer owed a run — its step's exit was rejected,
			// or a directed message to it waits, since it last ran — is run once
			// more, with the messages and findings, before anything else applies.
			if reDispatchOwed(ctx, current) {
				park := func(state, notes string, res DispatchResult) (json.RawMessage, error, bool) {
					return parkStory(ctx, current, raw, "", state, notes, res, now)
				}
				if out, derr := redispatchStep(ctx, current, now, park); out != nil || derr != nil {
					return out, derr
				}
			}
			// Fall through so title/tags/etc. updates still apply when combined.
			if !transitioning && req.Title == nil && req.Body == nil && req.Priority == nil &&
				req.Category == nil && req.ParentID == nil && req.AcceptanceCriteria == nil &&
				req.Tags == nil {
				// Pure re-engage: return updated record after optional no-op Update.
				it, uerr := store.Update(ctx, req.ID, workitem.UpdateInput{
					Status: req.Status, Assignee: stampEmptyAssignee(ctx, current, req.Status),
					ExpectStatus: &current.Status,
				}, now)
				if uerr != nil {
					return nil, uerr
				}
				return json.Marshal(it)
			}
		} else if inFlight {
			// Same-owner already leased at this target (AC3): return current
			// record without re-running gate+dispatch.
			return json.Marshal(current)
		} else {
			acquiredThisCall = acq
			settled = false
			// The gate and any dispatched performer fire no hook of their own, so
			// nothing else refreshes the heartbeat while they run.
			defer keepEngagementAlive(ctx, current.ID)()
		}
	}
	// releaseOnAbort: newly acquired seats are deleted; sequential continuations
	// only clear in_flight so a retry can re-enter (state stays last-committed).
	// Idempotent with Release/ClearInFlight no-ops on a missing row.
	releaseOnAbort := func() {
		if acquiredThisCall {
			releaseEngagementLease(ctx, current.ID)
		} else {
			clearEngagementInFlight(ctx, current.ID)
		}
	}
	defer func() {
		if !settled {
			releaseOnAbort()
		}
	}()

	// Gate the transition through the isolated reviewer, if one is wired and the
	// edge is governed by a reviewer skill. A reject blocks the whole set and
	// pushes the reviewer's notes back to the executor; an ungated edge enacts.
	//
	// Propose (sty_5262592e): a destination step that declares `propose` runs
	// its performer BEFORE the entry gates, on the source status, so the gates
	// judge what the performer produced. The route declares the order; this only
	// sequences the two calls below.
	proposeFirst := false
	// One attempt id per presentation, stamped on every verdict row a gate run
	// writes, so a rejected ROUND is countable however many reviewers ran. A
	// readiness (propose) round creates it up front so its performer rows carry it
	// too (sty_a32bd6a9); any other edge creates it when its gate runs.
	attempt := ""
	if proposeFirst {
		attempt = ledger.NewAttemptID()
	}
	runGate := func() error {
		if !(transitioning && transitionGater != nil) {
			return nil
		}
		if attempt == "" {
			attempt = ledger.NewAttemptID()
		}
		_, gerr := recordGateRound(ctx, current, *req.Status, attempt, now)
		return gerr
	}

	// A workflow node may allocate the TARGET state to a NAMED isolated agent
	// (agent=<name> — sty_fd427546). The binding's harness performs the step
	// synchronously here, after the edge's gates accepted and BEFORE the status is
	// enacted (or before them, when the step declares `propose`): a dispatch
	// failure refuses the whole transition (status unchanged), and the spawned
	// agent never advances status itself — the state's exit gate still governs the
	// next edge. agent=executor and agent-less states dispatch nothing (the
	// in-loop orchestrator performs, today's behaviour).
	// parkWith is parkStory under this presentation's attempt.
	parkWith := func(park, notes string, res DispatchResult) (out json.RawMessage, err error, handled bool) {
		return parkStory(ctx, current, raw, attempt, park, notes, res, now)
	}
	performStep := func() (json.RawMessage, error) {
		if !(transitioning && executorDispatcher != nil) {
			return nil, nil
		}
		run := dispatchPerformer(ctx, current, *req.Status, attempt, proposeFirst, now)
		// Changes the performer reported are already on the record: the gates and
		// the commit below read it as repaired (sty_af0b1761).
		current = run.rebind(current)
		if run.Err != nil {
			// A performer that judged the premise wrong parks the story: the
			// notes go on the timeline and the set re-enters for the park edge.
			// Any other failure is a plain refusal.
			if run.Reject != nil {
				if park := ResumeParkName(ctx, current); park != "" && park != *req.Status {
					if out, perr, handled := parkWith(park, run.Reject.Notes, run.Res); handled {
						return out, perr
					}
				}
			}
			recordDispatchFailure(ctx, current, *req.Status, attempt, run.Res, run.Err, now)
			return nil, fmt.Errorf("transition %s→%s refused: %v", current.Status, *req.Status, run.Err)
		}
		if run.Refuse != nil {
			notifyChange(panelTopic(current.Kind))
			return nil, fmt.Errorf("transition %s→%s refused: %v", current.Status, *req.Status, run.Refuse)
		}
		return nil, nil
	}
	// Order is the step's declaration: gates then performer (the default), or —
	// for a `propose` step — performer then gates, so the gate payload carries the
	// artifact the performer just attached. A failure at either call leaves the
	// story at its source status, and the deferred guard frees the seat.
	if gerr := runGate(); gerr != nil {
		return nil, gerr
	}
	if out, perr := performStep(); out != nil || perr != nil {
		return out, perr
	}

	// Park origin (sty_f75286dc): stamp authoritative resume-to origin when
	// entering a park node; clear on leave. Not ledger-derived.
	var parkOrigin *string
	if transitioning {
		parkOrigin = parkOriginForTransition(ctx, current, *req.Status)
	}
	// Status only on a real transition or same-status re-engage (sty_38915987).
	// A planner body write (or any field edit) must not replay a FROM snapshot's
	// status into the row after TO is ledgered — even when the request still
	// carries it. Reverse FROM after TO is refuseSkippedStep above.
	var statusForUpdate *string
	if transitioning || sameStatusReengage {
		statusForUpdate = req.Status
	}
	upd := workitem.UpdateInput{
		Title:              req.Title,
		Body:               req.Body,
		Status:             statusForUpdate,
		Priority:           req.Priority,
		Category:           req.Category,
		ParentID:           req.ParentID,
		AcceptanceCriteria: req.AcceptanceCriteria,
		Tags:               req.Tags,
		ParkOrigin:         parkOrigin,
		Assignee:           stampEmptyAssignee(ctx, current, req.Status),
		// Compare-and-set against the snapshot this call gated and dispatched on
		// (sty_2c71eff6). A transition enacts only from the status the reviewers
		// judged; a late write built on a stale read loses instead of clobbering.
		ExpectStatus: &current.Status,
	}
	// Everything below is the commit. The gates and the dispatched step ran for
	// minutes since `now` was taken, and rows attached live in that window
	// (documents, session invocations) carry real wall-clock time. Stamp the
	// transition's rows and the story's updated_at at the commit, so a
	// timestamp-ordered reader never finds them behind rows written during the
	// gate window (sty_4a31e1ed).
	commitNow := time.Now()
	transitionInTx := false
	var it workitem.Item
	if transitioning && ledgerStore != nil && txRunner == nil {
		return nil, fmt.Errorf("verb: ledger store is wired without a tx runner — refusing a non-atomic status transition")
	}
	if transitioning && txRunner != nil {
		err = txRunner(ctx, func(tx *sql.Tx) error {
			var uerr error
			it, uerr = store.WithTx(tx).Update(ctx, req.ID, upd, commitNow)
			if uerr != nil {
				return uerr
			}
			_, aerr := ledgerStore.WithTx(tx).Append(ctx, ledger.AppendInput{
				StoryID: it.ID,
				Kind:    ledger.KindStatusTransition,
				Actor:   "executor",
				Body:    fmt.Sprintf("%s → %s", current.Status, *req.Status),
				Payload: attemptPayload(current.Status, *req.Status, "", parkAttemptFrom(ctx)),
			}, commitNow)
			return aerr
		})
		if err != nil {
			return nil, staleWriteError(current, req.Status, err)
		}
		transitionInTx = true
	} else {
		it, err = store.Update(ctx, req.ID, upd, commitNow)
		if err != nil {
			return nil, staleWriteError(current, req.Status, err)
		}
	}
	editor := resolveEditorIdentity()
	// The events that carry the committed write land before anything below that
	// re-enters this verb (a ready-mark sync, a readiness mark): that call folds the
	// chain first, and a write the chain does not yet hold would be rebuilt away
	// (sty_d84be8bc).
	if foldable(it) {
		if transitioning {
			// The transition event carries the status; tags that changed beside it
			// are a story_updated of their own.
			appendTagChange(ctx, it.ID, current.Tags, it.Tags, "story-set", "executor", commitNow)
		} else {
			// A story's tag change rides this same story_updated event, in the
			// shape every tag writer records.
			var tagEdit *storyfold.TagsChange
			if !tagsEqual(current.Tags, it.Tags) {
				tagEdit = &storyfold.TagsChange{Before: nonNil(current.Tags), After: nonNil(it.Tags)}
			}
			actor := resolveActor()
			if actor == "" {
				actor = "executor"
			}
			appendLedgerEntry(ctx, it.ID, ledger.KindStoryUpdated, actor, fmt.Sprintf("updated %s", it.Kind),
				storyUpdatedPayloadFor(editor, definitionEdits, tagEdit), commitNow)
		}
	}
	// Release the seat on transition into terminal (Msquare) or park (agent=reviewer).
	// Force-release: exit is config-driven and must free the seat even when the
	// exit CLI process is not the acquire-time owner (default owner is stable
	// per host, but ForceRelease covers any residual owner mismatch).
	if transitioning && targetIsExitState(ctx, it, *req.Status) {
		forceReleaseEngagementLease(ctx, it.ID)
		settled = true
	} else if transitioning {
		// Engaging (or other non-exit) commit: settle lease state + clear in_flight.
		engaging, ok := storyStatusIsEngaging(ctx, it, *req.Status)
		if ok && engaging {
			confirmEngagementLease(ctx, it.ID, *req.Status)
			settled = true
		} else {
			// Non-engaging, non-exit target (a declared move back out of the
			// performing states, e.g. plan → backlog): the engagement is over,
			// so free the seat the story held. ForceRelease for the same
			// owner-mismatch reason as the exit branch. When governance did not
			// resolve (!ok) nothing was classed, so keep the fail-open: settle
			// without releasing. Either way the committed status is not rolled back.
			if ok {
				forceReleaseEngagementLease(ctx, it.ID)
			}
			settled = true
		}
	} else {
		settled = true
	}
	if transitioning {
		// An enacted status change records a transition row (feeds the progress
		// column), regardless of whether the edge was gated. When the write ran
		// inside the tx above, the row is already there.
		if !transitionInTx {
			appendLedgerEntry(ctx, it.ID, ledger.KindStatusTransition, "executor",
				fmt.Sprintf("%s → %s", current.Status, *req.Status),
				attemptPayload(current.Status, *req.Status, "", parkAttemptFrom(ctx)), commitNow)
		}
		// Every move into a performing state is visible on the ledger
		// (sty_40853d02, sty_9c782f0e): one working_on row, after the transition
		// that made it, whether or not the move took a seat. A move that did not
		// commit writes none.
		recordWorkingOn(ctx, it, *req.Status, commitNow)
		// The engaging session's model is the in-loop tier config.SelectModel's
		// inherited resolution reads (sty_7069bced). Recorded on every engaging
		// transition, not just the first — a later engage (e.g. blocked→in_progress)
		// may be driven by a different session than the one before it.
		if engaging, ok := storyStatusIsEngaging(ctx, it, *req.Status); ok && engaging {
			recordEngageSessionModel(ctx, it.ID)
		}
	} else if !foldable(it) {
		ledgerKind := ledger.KindStoryUpdated
		if it.Kind == workitem.KindTask || it.Kind == workitem.KindExecution {
			ledgerKind = ledger.KindTaskUpdated
		}
		actor := resolveActor()
		if actor == "" {
			actor = "executor"
		}
		appendLedgerEntry(ctx, it.ID, ledgerKind, actor, fmt.Sprintf("updated %s", it.Kind),
			storyUpdatedPayloadFor(editor, definitionEdits, nil), commitNow)
	}
	// The row is a cache of the fold: bring it to what the chain now says.
	if foldable(it) {
		if rebuilt, rerr := rebuildCache(ctx, it.ID); rerr == nil {
			it = rebuilt
		}
	}
	// Keep the task/execution file (the source of truth) current; a no-op for
	// stories (sty_c1f9e74c, sty_ef08ce2a).
	if err := writeItemFile(it); err != nil {
		return nil, fmt.Errorf("write item file: %w", err)
	}
	notifyChange(panelTopic(it.Kind))
	return json.Marshal(it)
}

// SetResult is a story-set response that carries advice: the record, plus the
// advisor skills the step declares when the transition was the repo's first
// accepted epic readiness round. Without advice the response is the record alone.
type SetResult struct {
	workitem.Item
	Advise []string `json:"advise,omitempty"`
}

// staleWriteError turns a losing compare-and-set into an actionable refusal
// (sty_2c71eff6). The write was built on a snapshot read before the reviewer
// gate and the named-agent dispatch; by the time it reached the row, somebody
// else had moved the status. Nothing was written — say what was attempted, what
// the row holds now, and that the fix is to re-read, never to force the write.
// Non-conflict errors pass through untouched.
func staleWriteError(current workitem.Item, target *string, err error) error {
	if !errors.Is(err, workitem.ErrStatusConflict) {
		return err
	}
	attempt := "field update"
	if target != nil && *target != current.Status {
		attempt = fmt.Sprintf("transition %s→%s", current.Status, *target)
	}
	return fmt.Errorf("%s refused on %s: %w — nothing was written; re-read the story (satelle story get %s) and retry from its current status",
		attempt, current.ID, err, current.ID)
}

// afterTagCASGetHook runs after a tags-only writer (storyEstimate / recordActual
// / storyRestamp) has Get'd the row and before it Update's with ExpectStatus. Tests inject a
// SetStatus here to prove the CAS refuses when the row moved under the verb
// (sty_38915987 AC3). Production leaves it nil.
var afterTagCASGetHook func(ctx context.Context, id, statusAtGet string)

// SetAfterTagCASGetHook wires the Get→Update race seam for tags-only writers.
// Pass nil to clear (tests).
func SetAfterTagCASGetHook(fn func(ctx context.Context, id, statusAtGet string)) {
	afterTagCASGetHook = fn
}

func runAfterTagCASGet(ctx context.Context, id, statusAtGet string) {
	if afterTagCASGetHook != nil {
		afterTagCASGetHook(ctx, id, statusAtGet)
	}
}

// applyTagMutation applies remove then add against existing tags (sty_033d4611).
// Remove entries may be exact tags or a namespace group: "sprint:" or "sprint:*"
// drops every tag with that axis. Adds that already exist are no-ops (set
// semantics). Order: survivors keep relative order; new adds append in the
// order given. Result is duplicate-free.
func applyTagMutation(existing, add, remove []string) []string {
	// Build remove predicates.
	exact := map[string]bool{}
	var axes []string // namespace axes to strip entirely
	for _, r := range remove {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.HasSuffix(r, ":*") {
			axes = append(axes, strings.TrimSuffix(r, ":*"))
			continue
		}
		if strings.HasSuffix(r, ":") && !strings.Contains(r[:len(r)-1], ":") {
			// "sprint:" with empty value → group remove
			axes = append(axes, strings.TrimSuffix(r, ":"))
			continue
		}
		exact[r] = true
	}
	dropAxis := func(tag string) bool {
		axis, _, ok := strings.Cut(tag, ":")
		if !ok {
			return false
		}
		for _, a := range axes {
			if a == axis {
				return true
			}
		}
		return false
	}

	out := make([]string, 0, len(existing)+len(add))
	seen := map[string]bool{}
	for _, t := range existing {
		if exact[t] || dropAxis(t) {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range add {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// dropKeyedTag removes every tag whose key (text before the first colon) is
// key. A tag that merely shares a prefix (actual-cost-unavailable-rows vs
// actual-cost) is kept.
func dropKeyedTag(tags []string, key string) []string {
	out := make([]string, 0, len(tags))
	prefix := key + ":"
	for _, t := range tags {
		if t == key || strings.HasPrefix(t, prefix) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// upsertKeyedTags returns existing with any tag whose `key:` matches a key in kv
// removed, then the kv pairs appended in key order — so re-recording an estimate
// replaces the old value rather than duplicating it, and unrelated tags survive.
func upsertKeyedTags(existing []string, kv map[string]string) []string {
	out := make([]string, 0, len(existing)+len(kv))
	for _, t := range existing {
		key := t
		if i := strings.IndexByte(t, ':'); i >= 0 {
			key = t[:i]
		}
		if _, replacing := kv[key]; replacing {
			continue
		}
		out = append(out, t)
	}
	out = append(out, joinedKeys(kv)...)
	return out
}

// joinedKeys renders kv as sorted "key:value" tags.
func joinedKeys(kv map[string]string) []string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(kv))
	for _, k := range keys {
		out = append(out, k+":"+kv[k])
	}
	return out
}

// joinKeyedTags renders kv as a comma-separated "key:value" list for a ledger body.
func joinKeyedTags(kv map[string]string) string {
	return strings.Join(joinedKeys(kv), ", ")
}

// appendLedger records a work-item lifecycle event, best-effort: a ledger
// failure must not fail the work mutation that already committed. Skipped when
// no ledger store is wired.
func appendLedger(ctx context.Context, storyID, kind, body string, now time.Time) {
	appendLedgerEntry(ctx, storyID, kind, "", body, nil, now)
}

// AppendAgentInvocation records one agent_invocation ledger row for itemID
// (sty_7069bced 4.2) — the seam agentstep.Engine.SetInvocationRecorder wires
// so a LIVE session (the rework relay's coder seat) can ledger its
// own open/close directly, the same way a one-shot dispatch's transition-gated
// appendLedgerEntry does, so it shows up in `satelle story cost` and the web
// timeline identically. actor is payload's own "agent" key — the binding the
// live session was opened with. Best-effort: nil-safe when no ledger is wired.
func AppendAgentInvocation(ctx context.Context, itemID string, payload map[string]any) error {
	if ledgerStore == nil {
		return nil
	}
	agent, _ := payload["agent"].(string)
	phase, _ := payload["phase"].(string)
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	appendLedgerEntry(ctx, itemID, ledger.KindAgentInvocation, agent,
		fmt.Sprintf("live session %s %s", phase, agent), raw, time.Now())
	return nil
}

// appendLedgerEntry is appendLedger with an actor and structured payload — used
// by the transition gate (review verdicts, status transitions). Best-effort.
func appendLedgerEntry(ctx context.Context, storyID, kind, actor, body string, payload json.RawMessage, now time.Time) {
	if ledgerStore == nil {
		return
	}
	_, _ = ledgerStore.Append(ctx, ledger.AppendInput{
		StoryID: storyID,
		Kind:    kind,
		Actor:   actor,
		Body:    body,
		Payload: payload,
	}, now)
}

// verdictFromDecision lifts a single-reviewer GateDecision into the
// ReviewerVerdict the ledger writers take, so an edge gate and a lifecycle hook
// (create_review, amend_review) record one decision identically.
func verdictFromDecision(dec GateDecision) ReviewerVerdict {
	return ReviewerVerdict{Skill: dec.Skill, Accept: dec.Accept, Notes: dec.Notes, Reasoning: dec.Reasoning,
		Reviewed: dec.Reviewed, ReviewedTruncated: dec.ReviewedTruncated, Criteria: dec.Criteria,
		ReviewMode: dec.ReviewMode, Coverage: dec.Coverage, Waived: dec.Waived,
		Command: dec.Command, Context: dec.Context, Model: dec.Model,
		ModelResolved: dec.ModelResolved, Models: dec.Models, ModelSource: dec.ModelSource,
		TokensIn: dec.TokensIn, TokensOut: dec.TokensOut, TokensTotal: dec.TokensTotal, DurationMs: dec.DurationMs,
		UsageAvailable: dec.UsageAvailable,
		TokensInFresh:  dec.TokensInFresh, TokensCacheWrite: dec.TokensCacheWrite, TokensCacheRead: dec.TokensCacheRead, UsageNote: dec.UsageNote,
		RunCounts: dec.RunCounts,
		CostUSD:   dec.CostUSD, CostUnavailableReason: dec.CostUnavailableReason,
		SystemPromptBytes: dec.SystemPromptBytes, PayloadBytes: dec.PayloadBytes,
		ToolIsolation: dec.ToolIsolation}
}

// withOperation stamps a lifecycle operation (create_review, amend_review) on a
// ledger payload so the timeline can tell a lifecycle-hook row from an edge row.
func withOperation(payload json.RawMessage, operation string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) != nil || m == nil {
		return payload
	}
	op, err := json.Marshal(operation)
	if err != nil {
		return payload
	}
	m["operation"] = op
	b, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return b
}

// recordLifecycleReview writes a lifecycle-hook reviewer run (create_review,
// amend_review) to the ledger the way an edge gate is recorded: an
// agent_invocation row carrying command, model, usage (or its adapter-named
// unavailable) and duration, then the review_accept / review_reject row with the
// reviewer's notes. storyID is empty for a rejected create — it persists no
// story, so its rows are repo-scoped (empty story id), which the repo-wide
// gate-value view reads. from→to is the edge the hook guards: create→<status>
// for a create, <status>→<status> for an amend.
//
// A deterministic ACCEPT (no Command: the structure check, or a repo that
// declares no hook) invoked no agent and judged nothing worth a row, so it is
// not recorded; a deterministic REJECT still is, since it explains a refusal.
// Best-effort, like every ledger write.
func recordLifecycleReview(ctx context.Context, storyID, operation, from, to string, dec GateDecision, now time.Time) {
	if !dec.Gated {
		return
	}
	rv := verdictFromDecision(dec)
	if rv.Command == "" && rv.Accept {
		return
	}
	attempt := ledger.NewAttemptID()
	if rv.Command != "" {
		appendLedgerEntry(ctx, storyID, ledger.KindAgentInvocation, "reviewer",
			fmt.Sprintf("invoked reviewer (%s) for %s with @skill:%s", rv.Command, operation, rv.Context),
			withOperation(invocationPayload(from, to, attempt, rv), operation), now)
	}
	if !rv.Accept {
		appendLedgerEntry(ctx, storyID, ledger.KindReviewReject, "reviewer",
			fmt.Sprintf("rejected %s by %s: %s", operation, rv.Skill, rv.Notes),
			withOperation(reviewerPayload(from, to, rv, attempt), operation), now)
		return
	}
	appendLedgerEntry(ctx, storyID, ledger.KindReviewAccept, "reviewer",
		fmt.Sprintf("accepted %s by %s: decision=accept notes=%s%s", operation, rv.Skill, rv.Notes, formatReasoningSuffix(rv.Reasoning)),
		withOperation(reviewerPayload(from, to, rv, attempt), operation), now)
}

// definitionFieldsChanged lists which of the four definition fields the request
// actually changes (non-nil pointer whose value differs from current). Order is
// fixed: title, body, acceptance_criteria, category. A resubmitted-identical
// value does not count as a change.
func definitionFieldsChanged(current workitem.Item, req setReq) []string {
	var out []string
	if req.Title != nil && *req.Title != current.Title {
		out = append(out, "title")
	}
	if req.Body != nil && *req.Body != current.Body {
		out = append(out, "body")
	}
	if req.AcceptanceCriteria != nil && *req.AcceptanceCriteria != current.AcceptanceCriteria {
		out = append(out, "acceptance_criteria")
	}
	if req.Category != nil && *req.Category != current.Category {
		out = append(out, "category")
	}
	return out
}

// storyDefinitionEditable reports whether item's definition may still be edited
// under its governing route, and the sentence naming where the route freezes it
// (Spec.DefinitionEditable). ok is false when the route cannot be resolved, so
// the caller fails closed. The answer is derived from route order, never a
// status literal (sty_5262592e).
func storyDefinitionEditable(ctx context.Context, item workitem.Item) (editable bool, boundary string, ok bool) {
	spec, _, _, resolved := governingSpec(ctx, item)
	if !resolved {
		return false, "", false
	}
	editable, ok = spec.DefinitionEditable(item.Status)
	if !ok {
		return false, "", false
	}
	for _, st := range spec.States {
		if st.Freeze {
			return editable, fmt.Sprintf("the route freezes the definition on entry to %q", st.Name), true
		}
	}
	return editable, fmt.Sprintf("entry state is %q", spec.Start()), true
}

// storyEntryState returns the governing workflow's entry state (Spec.Start —
// conventionally the Mdiamond node) for item. ok is false when the doc index is
// unwired, the workflow list fails, no workflow governs the item, the body is
// not DOT, or Start is empty. Pure governance path via wfgovern + wfdot so the
// freeze holds even with no agent CLI configured.
func storyEntryState(ctx context.Context, item workitem.Item) (string, bool) {
	idx, err := requireDocIndex()
	if err != nil {
		return "", false
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return "", false
	}
	spec, _, _, serr := wfgovern.SpecFor(wfs, item)
	ok := serr == nil
	if !ok {
		return "", false
	}
	start := spec.Start()
	if start == "" {
		return "", false
	}
	return start, true
}

// tagsChanged reports the before/after tag sets as a one-line detail when they
// differ, else "" — so a tag reconciliation (sprint/order) is greppable in the
// operation log. Order-insensitive comparison; the rendered lists keep input order.
func tagsChanged(before, after []string) string {
	if equalStringSet(before, after) {
		return ""
	}
	return fmt.Sprintf("tags: [%s] -> [%s]", strings.Join(before, ","), strings.Join(after, ","))
}

// equalStringSet reports whether two tag slices hold the same set of values.
func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// transitionPayload is the {from,to,skill} JSON stamped on review/transition
// ledger rows so the progress column can reconstruct the workflow trail.
func transitionPayload(from, to, skill string) json.RawMessage {
	return attemptPayload(from, to, skill, "")
}

// attemptPayload is transitionPayload stamped with the attempt id of the
// readiness round the row belongs to, under the same `attempt` key the review
// rows carry (sty_a32bd6a9). An empty attempt writes the plain payload.
func attemptPayload(from, to, skill, attempt string) json.RawMessage {
	p := struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Skill   string `json:"skill,omitempty"`
		Attempt string `json:"attempt,omitempty"`
	}{From: from, To: to, Skill: skill, Attempt: attempt}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// reviewerPayload is transitionPayload enriched with a single reviewer's order
// and system-layer flag — stamped on each per-reviewer review row so the trail
// preserves who judged the edge, in what order, and whether from the always-on
// system layer. attempt is the id of the ONE gate run that produced the verdict
// (ledger.NewAttemptID), shared by every row that run writes, so a rejected
// presentation is countable however many reviewers judged it.
func reviewerPayload(from, to string, rv ReviewerVerdict, attempt string) json.RawMessage {
	p := struct {
		From              string             `json:"from"`
		To                string             `json:"to"`
		Attempt           string             `json:"attempt,omitempty"`
		Skill             string             `json:"skill,omitempty"`
		Order             int                `json:"order"`
		System            bool               `json:"system,omitempty"`
		Notes             string             `json:"notes,omitempty"`
		Reasoning         string             `json:"reasoning,omitempty"`
		Reviewed          string             `json:"reviewed,omitempty"`
		ReviewedTruncated bool               `json:"reviewed_truncated,omitempty"`
		Criteria          []CriterionVerdict `json:"criteria,omitempty"`
		ReviewMode        string             `json:"review_mode,omitempty"`
		Coverage          *Coverage          `json:"coverage,omitempty"`
		Waived            []string           `json:"waived,omitempty"`
		Accept            bool               `json:"accept"`
		CarriedFrom       int                `json:"carried_from,omitempty"`
		Model             string             `json:"model,omitempty"`
		ModelResolved     string             `json:"model_resolved,omitempty"`
		ModelSource       string             `json:"model_source,omitempty"`
		Models            []ModelUsage       `json:"model_usage,omitempty"`
		// BundleID/BundleSize: this verdict came from one bundled session judging
		// BundleSize rubrics (sty_23e10d92). The verdict row carries no usage — the
		// bundle's single agent_invocation row does.
		BundleID   string      `json:"bundle_id,omitempty"`
		BundleSize int         `json:"bundle_size,omitempty"`
		Seats      []PanelSeat `json:"seats,omitempty"`
		Combine    string      `json:"combine,omitempty"`
		// RecordedAt is when the row was appended — after the gate run finished,
		// unlike the row's CreatedAt (the transition's start). PriorVerdicts reads
		// it as the end of the review cycle (sty_0225fc2f).
		RecordedAt string `json:"recorded_at,omitempty"`
	}{From: from, To: to, Attempt: attempt, Skill: rv.Skill, Order: rv.Order, System: rv.System,
		Notes: rv.Notes, Reasoning: rv.Reasoning, Reviewed: rv.Reviewed, ReviewedTruncated: rv.ReviewedTruncated, Criteria: rv.Criteria,
		ReviewMode: rv.ReviewMode, Coverage: rv.Coverage, Waived: rv.Waived, Accept: rv.Accept, CarriedFrom: rv.CarriedFrom,
		Model: rv.Model, ModelResolved: rv.ModelResolved, ModelSource: rv.ModelSource, Models: rv.Models,
		BundleID: rv.BundleID, BundleSize: len(rv.BundleSkills),
		Seats: rv.Seats, Combine: rv.Combine, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// parkStory parks current at park with notes on the timeline: the set request raw
// re-enters for the park edge, so blocked-review, park origin and the seat run as
// for any other park (sty_b8a0d062). attempt is the readiness round the park ends,
// "" for none. handled is false when the request could not be re-issued, leaving
// the caller to refuse.
func parkStory(ctx context.Context, current workitem.Item, raw json.RawMessage, attempt, park, notes string, res DispatchResult, now time.Time) (out json.RawMessage, err error, handled bool) {
	author := res.Agent
	if author == "" {
		author = "executor"
	}
	appendLedgerEntry(ctx, current.ID, ledger.KindComment, author,
		notes, attemptPayload(current.Status, park, res.Skill, attempt), now)
	var parked map[string]json.RawMessage
	if json.Unmarshal(raw, &parked) != nil {
		return nil, nil, false
	}
	parked["status"], _ = json.Marshal(park)
	reraw, merr := json.Marshal(parked)
	if merr != nil {
		return nil, nil, false
	}
	// The park transition names the readiness round it ends (sty_a32bd6a9).
	out, err = workItemSetCore(withParkAttempt(ctx, attempt), reraw) // the outer workItemSet annotates once
	return out, err, true
}

// dispatchPayload stamps a named-executor agent_invocation row with its model and
// token/wall-time cost (sty_a699ad14), so the per-gate cost view can roll up a
// dispatched step (planner, coder) the same way it does a reviewer gate.
func dispatchPayload(from, to string, res DispatchResult, attempt string) json.RawMessage {
	p := struct {
		From           string       `json:"from"`
		To             string       `json:"to"`
		Attempt        string       `json:"attempt,omitempty"`
		Agent          string       `json:"agent"`
		Skill          string       `json:"skill,omitempty"`
		Command        string       `json:"command,omitempty"`
		Model          string       `json:"model,omitempty"`
		ModelResolved  string       `json:"model_resolved,omitempty"`
		ModelSource    string       `json:"model_source,omitempty"`
		Models         []ModelUsage `json:"model_usage,omitempty"`
		TokensIn       int          `json:"tokens_in,omitempty"`
		TokensOut      int          `json:"tokens_out,omitempty"`
		TokensTotal    int          `json:"tokens_total,omitempty"`
		DurationMs     int64        `json:"duration_ms,omitempty"`
		UsageAvailable bool         `json:"usage_available"` // unconditional — tri-state (sty_56aae77a)
		// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn (sty_363eaf55).
		TokensInFresh    int `json:"tokens_in_fresh,omitempty"`
		TokensCacheWrite int `json:"tokens_cache_write,omitempty"`
		TokensCacheRead  int `json:"tokens_cache_read,omitempty"`
		UsageNote
		CostUSD               *float64 `json:"cost_usd,omitempty"`
		CostUnavailableReason string   `json:"cost_unavailable_reason,omitempty"`
		// SystemPromptBytes/PayloadBytes: byte lengths satelle sent, never content.
		SystemPromptBytes int    `json:"system_prompt_bytes,omitempty"`
		PayloadBytes      int    `json:"payload_bytes,omitempty"`
		ArtifactName      string `json:"artifact_name,omitempty"`
		ArtifactType      string `json:"artifact_type,omitempty"`
		// Turns is what the run reported, or the adapter-named reason it did not.
		Turns                  *int   `json:"turns,omitempty"`
		TurnsUnavailableReason string `json:"turns_unavailable_reason,omitempty"`
		// Cloud records a cloud-performed step's session, branch and collected commit.
		Cloud *CloudDispatch `json:"cloud,omitempty"`
		// LastMessage is the tail of the run's output, present on every performer
		// row (empty when the run printed nothing) so a drive that stops can quote
		// what the performer last said.
		LastMessage string `json:"last_message"`
		// FindingsOffered/FindingAnswers record the blocking findings the
		// performer was asked to answer and what it answered (sty_f948d345); the
		// gate payload reads them back as a fact.
		FindingsOffered []string               `json:"findings_offered,omitempty"`
		FindingAnswers  []agentartifact.Answer `json:"finding_answers,omitempty"`
		FindingsEdgeTo  string                 `json:"findings_edge_to,omitempty"`
		// Skills is the manifest of the step skills delivered to the performer —
		// name and content hash, or undelivered with the reason (sty_23ceeca2).
		Skills []SkillDelivery `json:"skills,omitempty"`
		// SkillEvidence is the skill-evidence check's answer for the run.
		SkillEvidence *EvidenceReport `json:"skill_evidence,omitempty"`
	}{From: from, To: to, Attempt: attempt, Agent: res.Agent, Skill: res.Skill, Command: res.Command, Model: res.Model,
		Skills: res.Skills, SkillEvidence: res.Evidence,
		ModelResolved: res.ModelResolved, ModelSource: res.ModelSource, Models: res.Models,
		TokensIn: res.TokensIn, TokensOut: res.TokensOut, TokensTotal: res.TokensTotal, DurationMs: res.DurationMs,
		UsageAvailable: res.UsageAvailable,
		TokensInFresh:  res.TokensInFresh, TokensCacheWrite: res.TokensCacheWrite, TokensCacheRead: res.TokensCacheRead, UsageNote: res.UsageNote,
		CostUSD: res.CostUSD, CostUnavailableReason: res.CostUnavailableReason,
		SystemPromptBytes: res.SystemPromptBytes, PayloadBytes: res.PayloadBytes,
		ArtifactName: res.ArtifactName, ArtifactType: res.ArtifactType, Cloud: res.Cloud,
		Turns: res.Turns, TurnsUnavailableReason: res.TurnsUnavailableReason,
		LastMessage: LastMessage(res.LastMessage), FindingsOffered: res.FindingsOffered, FindingAnswers: res.Answers,
		FindingsEdgeTo: res.FindingsEdgeTo}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// LastMessageMax bounds the output tail a performer row carries as last_message.
const LastMessageMax = 2000

// LastMessage is the tail of a performer's output kept on its agent_invocation
// row: at most LastMessageMax bytes, cut on a UTF-8 boundary so the stored text
// is always valid.
func LastMessage(out string) string {
	if len(out) <= LastMessageMax {
		return out
	}
	tail := out[len(out)-LastMessageMax:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return tail
}

// RedactMinLen is the shortest resolved env value OutputTail scrubs. A value
// under it (a flag like "1", a two-letter region) would shred ordinary prose and
// is not a credential; every longer non-empty value is scrubbed.
const RedactMinLen = 4

// RedactEnv replaces every occurrence in text of a value in env (at least
// RedactMinLen bytes) with [redacted:KEY], naming the key and never the value.
// Longer values go first so one that contains another is not left half-scrubbed;
// two keys holding the same value are named by the first in key order.
func RedactEnv(text string, env map[string]string) string {
	if text == "" || len(env) == 0 {
		return text
	}
	keys := make([]string, 0, len(env))
	for k, v := range env {
		if len(v) >= RedactMinLen {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if li, lj := len(env[keys[i]]), len(env[keys[j]]); li != lj {
			return li > lj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		text = strings.ReplaceAll(text, env[k], "[redacted:"+k+"]")
	}
	return text
}

// OutputTail is what a performer's output may leave in the ledger: the
// LastMessage tail of out once every value satelle resolved into the performer's
// environment (env) is scrubbed. The scrub runs before the cap, so a value the
// cap would have cut in half is never left as a fragment.
func OutputTail(out string, env map[string]string) string {
	return LastMessage(RedactEnv(out, env))
}

// performerFailurePayload is attemptPayload for a performer's failed run, plus
// the tail of whatever it printed before failing (already scrubbed, see
// DispatchResult.LastMessage). It is separate from attemptPayload, the
// transition rows' payload, which never carries output.
func performerFailurePayload(from, to, skill, attempt, output string, skills []SkillDelivery) json.RawMessage {
	p := struct {
		From        string `json:"from"`
		To          string `json:"to"`
		Skill       string `json:"skill,omitempty"`
		Attempt     string `json:"attempt,omitempty"`
		LastMessage string `json:"last_message"`
		// Skills is the delivery manifest, recorded even when the run failed — a
		// refusal for an undelivered skill leaves exactly this fact (sty_23ceeca2).
		Skills []SkillDelivery `json:"skills,omitempty"`
	}{From: from, To: to, Skill: skill, Attempt: attempt, LastMessage: LastMessage(output), Skills: skills}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// formatReasoningSuffix appends " reasoning=<text>" when non-empty so accept and
// reject session output share one shape (design §6.2).
func formatReasoningSuffix(reasoning string) string {
	if strings.TrimSpace(reasoning) == "" {
		return ""
	}
	return " reasoning=" + reasoning
}

// multiRejectError builds the transition refuse message for one or more
// rejecting reviewers (sty_4f0a15db). Single-reject keeps the historical format
// so serial short-circuit prose and tests stay stable; multi-reject joins each
// rejecter with notes (and optional reasoning).
func multiRejectError(from, to string, rejects []ReviewerVerdict) error {
	if len(rejects) == 1 {
		rv := rejects[0]
		return &GateRejection{From: from, To: to, Rejects: rejects, msg: fmt.Sprintf(
			"transition %s→%s rejected by %s: decision=reject notes=%s%s",
			from, to, rv.Skill, rv.Notes, formatReasoningSuffix(rv.Reasoning))}
	}
	var parts []string
	for _, rv := range rejects {
		parts = append(parts, fmt.Sprintf("rejected by %s: notes=%s%s",
			rv.Skill, rv.Notes, formatReasoningSuffix(rv.Reasoning)))
	}
	return &GateRejection{From: from, To: to, Rejects: rejects,
		msg: fmt.Sprintf("transition %s→%s %s", from, to, strings.Join(parts, "; "))}
}

// GateRejection is the refusal a presented edge returns when its reviewers
// rejected it. The message is the historical prose; Rejects carries the verdicts
// so a caller that relays the objection (the headless drive) reads the notes
// rather than parsing the message.
type GateRejection struct {
	From, To string
	Rejects  []ReviewerVerdict
	msg      string
}

func (e *GateRejection) Error() string { return e.msg }

// Notes joins every rejecting reviewer's notes, each led by the skill that
// raised it, in the order the reviewers ran.
func (e *GateRejection) Notes() string {
	parts := make([]string, 0, len(e.Rejects))
	for _, rv := range e.Rejects {
		n := strings.TrimSpace(rv.Notes + formatReasoningSuffix(rv.Reasoning))
		parts = append(parts, fmt.Sprintf("%s: %s", rv.Skill, n))
	}
	return strings.Join(parts, "\n")
}

// invocationPayload stamps an agent_invocation row with the resolved command and
// the injected-context source (skill/rubric file), correlated to the edge, so the
// timeline can show HOW the agent was invoked alongside its verdict (sty_fb3e0873).
// attempt is the id of the ONE gate run the row belongs to — the id its verdict
// rows carry — so a presentation's seat attempts are joinable to it.
func invocationPayload(from, to, attempt string, rv ReviewerVerdict) json.RawMessage {
	rv.RunCounts = rv.RunCounts.named(rv.Command)
	p := struct {
		From           string       `json:"from"`
		To             string       `json:"to"`
		Attempt        string       `json:"attempt,omitempty"`
		Agent          string       `json:"agent"`
		Skill          string       `json:"skill,omitempty"`
		Command        string       `json:"command,omitempty"`
		Context        string       `json:"context,omitempty"`
		Model          string       `json:"model,omitempty"`
		ModelResolved  string       `json:"model_resolved,omitempty"`
		ModelSource    string       `json:"model_source,omitempty"`
		Models         []ModelUsage `json:"model_usage,omitempty"`
		TokensIn       int          `json:"tokens_in,omitempty"`
		TokensOut      int          `json:"tokens_out,omitempty"`
		TokensTotal    int          `json:"tokens_total,omitempty"`
		DurationMs     int64        `json:"duration_ms,omitempty"`
		UsageAvailable bool         `json:"usage_available"` // unconditional — tri-state (sty_56aae77a)
		// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn (sty_363eaf55).
		TokensInFresh    int `json:"tokens_in_fresh,omitempty"`
		TokensCacheWrite int `json:"tokens_cache_write,omitempty"`
		TokensCacheRead  int `json:"tokens_cache_read,omitempty"`
		UsageNote
		// RunCounts: the run's turns and tool calls, or the adapter-named reason
		// each is unavailable — never a bare zero.
		RunCounts
		CostUSD               *float64 `json:"cost_usd,omitempty"`
		CostUnavailableReason string   `json:"cost_unavailable_reason,omitempty"`
		// SystemPromptBytes/PayloadBytes: byte lengths satelle sent, never content.
		SystemPromptBytes int `json:"system_prompt_bytes,omitempty"`
		PayloadBytes      int `json:"payload_bytes,omitempty"`
		// ToolIsolation: offered tool count/source and the adapter limitation
		// (sty_ef3efb51).
		ToolIsolation
		// BundleID/BundleSkills: this one invocation judged every listed rubric
		// (sty_23e10d92); costview allocates its usage across them.
		BundleID     string   `json:"bundle_id,omitempty"`
		BundleSkills []string `json:"bundle_skills,omitempty"`
		// Seat is the agents.toml section when this row is one panel seat's
		// invocation. Empty on a single-seat gate.
		Seat string `json:"seat,omitempty"`
		// Outcome is OutcomeUnavailable or OutcomeMaxTurns when this attempt
		// produced no verdict — the only record such an attempt has. Absent when it
		// judged: the verdict row says accept or reject.
		Outcome string `json:"outcome,omitempty"`
	}{From: from, To: to, Attempt: attempt, Agent: "reviewer", Skill: rv.Skill, Command: rv.Command, Context: rv.Context, Model: rv.Model,
		ModelResolved: rv.ModelResolved, ModelSource: rv.ModelSource, Models: rv.Models,
		TokensIn: rv.TokensIn, TokensOut: rv.TokensOut, TokensTotal: rv.TokensTotal, DurationMs: rv.DurationMs,
		UsageAvailable: rv.UsageAvailable,
		TokensInFresh:  rv.TokensInFresh, TokensCacheWrite: rv.TokensCacheWrite, TokensCacheRead: rv.TokensCacheRead, UsageNote: rv.UsageNote,
		RunCounts: rv.RunCounts,
		CostUSD:   rv.CostUSD, CostUnavailableReason: rv.CostUnavailableReason,
		SystemPromptBytes: rv.SystemPromptBytes, PayloadBytes: rv.PayloadBytes,
		ToolIsolation: rv.ToolIsolation,
		BundleID:      rv.BundleID, BundleSkills: rv.BundleSkills,
		Seat: rv.Seat, Outcome: rv.Outcome}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}
