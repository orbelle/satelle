// Package drive walks one story along the route its repo declares, with nobody
// at a prompt: it presents each next edge through the same story-set verb a local
// drive uses, lets the step's configured performer run headless, lets the gates
// judge, relays what a rejection said back to the performer, and stops at the
// route's terminal state — or parks the story as blocked, with the question
// recorded on it, when a person has to decide.
//
// Story is the one entry point. It takes no harness: who performs a step is
// whatever the engine's performer rule (verb.PerformerResolver) and the repo's
// agents.toml bind, so the same drive serves a claude, a grok or a pi binding.
// It is called by `satelle story drive`, and is the hook a node's story command
// calls to run a story it was woken for (the remote-workflow stories
// sty_866c379b and sty_602746d8 sit on it).
//
// The drive decides no process. The route says what the steps are, the gates say
// whether an edge is accepted, the verbs say what a status move records; the
// drive only sequences them and stops where the workflow's own mechanisms stop.
package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/runstatus"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Performers is what the drive asks about who performs a step, and how it runs
// the one performer the engine cannot dispatch on entry. *agentstep.Engine
// implements it.
type Performers interface {
	// StepPerformer is the engine's one rule for who performs a step. For an
	// executor-allocated step it also says whether the [executor] binding is the
	// in-loop session (PerformerResolution.ExecutorInLoop).
	verb.PerformerResolver
	// PerformExecutorStep runs that binding headless over an executor-allocated
	// step, with notes (reviewer findings, or "") appended to its prompt.
	PerformExecutorStep(ctx context.Context, item workitem.Item, step, notes string) (verb.DispatchResult, error)
}

// Rework is the outcome of one run of a step's declared rework relay.
type Rework struct {
	// Converged is the consultant saying ready. Anything else ended the relay
	// without agreement (budget spent, or no progress).
	Converged     bool
	Outcome       string
	Rounds        int
	LastObjection string
}

// Reworker runs the story's current step's declared rework relay (a coder and a
// consulting reviewer, bounded by the step's round budget). The relay needs live
// sessions, which belong to the caller's process, so it is injected.
type Reworker func(ctx context.Context, id string) (Rework, error)

// Options carries what the drive cannot resolve itself.
type Options struct {
	Performers Performers
	// Rework runs a step's declared relay. Nil leaves a declared relay unrun: a
	// rejected edge is then relayed to the performer and re-presented.
	Rework Reworker
	// Out receives one line per move and per rejection; nil discards them.
	Out io.Writer
	// OnMove, when set, is called after each status move is recorded in
	// Result.Moves, so a caller can watch the drive live (a remote node reports
	// the step it is on). It runs on the drive's goroutine and must not block.
	OnMove func(Move)
	// Location / LocationLabel are this node's location identity
	// (hosted.KnownLocationID, hosted.LocationLabel). They are stamped on every
	// park and drive-stopped row so the server can tell a park made by a remote's
	// drive from a person's.
	Location      string
	LocationLabel string
	// Runs reads a team remote's run for the story, for a step that waits on one
	// (wfdot.RemoteWait). Nil leaves such a step unwalkable: the drive refuses it
	// rather than guess where the run stands.
	Runs runstatus.Source
}

// Stopped is the error of a drive that ended short of its route's exit and could
// not park the story as blocked (the workflow has no park state, or the park was
// refused). It is never silent: a drive-stopped ledger row carrying Reason was
// written first, and the story's own hold is released by the caller.
type Stopped struct {
	ID     string
	Status string
	Reason string
	// ParkErr is why the story could not be parked.
	ParkErr error
}

func (s *Stopped) Error() string {
	return fmt.Sprintf("%s stopped at %s: %s", s.ID, s.Status, s.Reason)
}

func (s *Stopped) Unwrap() error { return s.ParkErr }

// halt is a stop reported from inside a step: where the drive was, who was meant
// to perform, and what was missing. A halt reached after the drive's first
// presentation becomes a park (or a drive-stopped row); an empty performer is
// derived from the step.
type halt struct {
	step, performer, what string
	err                   error
}

func (h *halt) Error() string { return reasonf(h.step, h.performer, h.what) }
func (h *halt) Unwrap() error { return h.err }
func reasonf(step, performer, what string) string {
	return fmt.Sprintf("step %s, performer %s: %s", step, performer, what)
}

// OutcomeStopped is the outcome a drive-stopped row on an execution carries: the
// execution ended short of its route's exit. It is a drive's record, not an
// outcome a performer records, so it is not in verb.ValidOutcome and is never
// sent as a run's outcome on the heartbeat.
const OutcomeStopped = "stopped"

// stopGrace bounds the writes that record a stop. They run detached from the
// drive's own context, which is the very thing that may have ended.
const stopGrace = 2 * time.Minute

// Result is where the drive stopped.
type Result struct {
	ID     string
	Status string
	// Terminal: the story is at a terminal step of its route.
	Terminal bool
	// Parked: the story is parked (blocked) and a person must pick it up; Reason
	// is what was recorded on it.
	Parked bool
	Reason string
	// Waiting: the story stands at a waiting step and is neither parked nor
	// terminal. Either the step waits on a team remote's run that has not
	// concluded (or concluded where this drive already took its edge), or it is a
	// route wait step, where the story rests until a move takes it onward. Reason
	// says which. Nothing is parked or recorded; a later drive reads it again.
	Waiting bool
	// Moves are the status moves this drive made, in order.
	Moves []Move
}

// Move is one status move the drive made.
type Move struct{ From, To string }

// unbudgetedPresentations bounds the presentations of an edge whose destination
// step declares no reject_budget, so a reviewer that always rejects cannot hold
// a drive forever. A declared reject_budget is enforced by the verb and is the
// bound instead.
const unbudgetedPresentations = 10

// Story drives story id from its current status to a terminal step of its route
// or to a park, returning where it stopped. An error means the drive refused or
// could not continue, with the story left where it was: the route cannot be
// driven unattended (a step the driving session must perform), or a mechanism
// failed. A story that parked is a Result, not an error.
func Story(ctx context.Context, id string, opts Options) (Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Result{}, errors.New("drive: a story id is required")
	}
	if opts.Performers == nil {
		return Result{}, errors.New("drive: no performer resolver is wired")
	}
	d := &driver{id: id, opts: opts, out: opts.Out}
	if d.out == nil {
		d.out = io.Discard
	}
	return d.run(ctx)
}

type driver struct {
	id   string
	opts Options
	out  io.Writer

	route wfgovern.DerivedRoute
	spine []string
	res   Result
	// presented is set once the workflow has judged one of the drive's
	// presentations (accepted, rejected, or ended by the context). A refusal
	// before it is a refusal to start and records nothing; every end after it
	// leaves a record.
	presented bool
	// exec is set when the item driven is an execution. An execution has no
	// session watching it, so every end of its drive short of the exit leaves a
	// stopped record, even a refusal before its first presentation.
	exec bool
	// recorded is set once the stop is recorded, so no path writes it twice.
	recorded bool
	// entryNotes are findings relayed to the executor binding when the step the
	// story was just moved to is allocated to it; performEntered passes and clears
	// them.
	entryNotes string
}

func (d *driver) run(ctx context.Context) (Result, error) {
	item, err := d.load(ctx)
	if err != nil {
		return Result{}, err
	}
	// An execution is walked as a story is: its route comes from the execution
	// kind, and the set and get verbs the drive uses are kind-agnostic. A task
	// header has no lifecycle to walk.
	if item.Kind != workitem.KindStory && item.Kind != workitem.KindExecution {
		return Result{}, fmt.Errorf("drive: %s is not a story or execution", d.id)
	}
	d.exec = item.Kind == workitem.KindExecution
	d.route, err = verb.GoverningRoute(ctx, item)
	if err != nil {
		return d.fail(ctx, item, fmt.Errorf("drive: %s has no route to walk: %w", d.id, err))
	}
	d.spine = d.route.Spec.SpineSteps()
	d.res.ID = d.id
	if !d.stopped(item) {
		if err := d.preflight(ctx, item); err != nil {
			return d.fail(ctx, item, err)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return d.fail(ctx, item, &halt{step: item.Status, err: err,
				what: "the drive was interrupted before the story reached its exit: " + err.Error()})
		}
		if d.stopped(item) {
			return d.finish(ctx, item), nil
		}
		if wait, ok := d.route.Spec.RemoteWaitOf(item.Status); ok {
			moved, done, err := d.awaitRemote(ctx, item, wait)
			if err != nil {
				return d.fail(ctx, moved, err)
			}
			if done {
				return d.res, nil
			}
			item = moved
			continue
		}
		next, err := d.successor(item.Status)
		if err != nil {
			return d.fail(ctx, item, err)
		}
		item, err = d.advance(ctx, item, next)
		if err != nil {
			return d.fail(ctx, item, err)
		}
		if d.res.Parked {
			return d.res, nil
		}
	}
}

// awaitRemote reads where the team remote's run for the story stands, for a story
// at a step that waits on it. A run still queued or running (or none) ends the
// drive as Waiting. A concluded run takes the edge the step maps its outcome to:
// a park goes through the drive's one park; any other edge is presented through
// advance, so every gate, reject budget and move record applies, after the run's
// findings are handed to whoever performs the step it leads to. done is true when
// the drive ended here (waiting or parked); otherwise the story moved and the
// returned item is where it now stands.
func (d *driver) awaitRemote(ctx context.Context, item workitem.Item, wait wfdot.RemoteWait) (workitem.Item, bool, error) {
	step := item.Status
	if d.opts.Runs == nil {
		return item, false, fmt.Errorf("drive: step %q waits on remote %q's run for the story, and no run source is wired", step, wait.Remote)
	}
	run, ok, err := d.opts.Runs.StoryRun(ctx, wait.Remote, d.id)
	if err != nil {
		return item, false, &halt{step: step, performer: wait.Remote, err: err,
			what: fmt.Sprintf("could not read remote %q's run for the story: %v", wait.Remote, err)}
	}
	line := runstatus.Render(run, ok, wait.Remote)
	fmt.Fprintf(d.out, "drive: %s at %s: %s\n", d.id, step, line)
	if !ok || run.State != runstatus.Concluded {
		d.res.Status, d.res.Waiting, d.res.Reason = step, true, line
		return item, true, nil
	}
	if d.actedOn(ctx, wait.Remote, step, run.Key()) {
		d.res.Status, d.res.Waiting, d.res.Reason = step, true, line+" (already acted on; waiting for a new run)"
		return item, true, nil
	}
	moved, done, to, err := d.takeRunEdge(ctx, item, wait, run)
	// The conclusion is acted on once the story has left the waiting step, even if
	// an error followed the move (the entered step's performer failed, say): the
	// next drive must not take the same edge again from a stale conclusion. An
	// error that left the story at the step records nothing, so it is retried.
	if err == nil || d.leftStep(ctx, moved, step) {
		d.recordActed(ctx, wait.Remote, step, run, to)
	}
	if err != nil {
		return moved, false, err
	}
	return moved, done, nil
}

// leftStep reports whether the story no longer stands at step. It re-reads the
// story, since an edge that failed after its move returns the item as it was
// before it; where the story cannot be read, the returned item is what is known.
func (d *driver) leftStep(ctx context.Context, moved workitem.Item, step string) bool {
	if now, err := d.load(context.WithoutCancel(ctx)); err == nil {
		return now.Status != step
	}
	return moved.Status != step
}

// takeRunEdge acts on a concluded run: it takes the edge the step maps the run's
// outcome to. to is where the story was sent (the park state for a park, empty
// when the outcome maps to no edge and the story was parked for that).
func (d *driver) takeRunEdge(ctx context.Context, item workitem.Item, wait wfdot.RemoteWait, run runstatus.Run) (moved workitem.Item, done bool, to string, err error) {
	step := item.Status
	target, mapped := wait.On[run.Outcome]
	if !mapped {
		return item, true, "", d.park(ctx, item, step, wait.Remote, fmt.Sprintf(
			"remote %s run concluded with outcome %q, which the step's remote_wait maps to no edge", wait.Remote, run.Outcome))
	}
	concluded := fmt.Sprintf("remote %s run concluded: %s", wait.Remote, run.Outcome)
	findings := strings.TrimSpace(run.Findings)
	if d.route.Spec.IsParkState(target) {
		why := concluded
		if findings != "" {
			why += "\n" + findings
		}
		return item, true, target, d.park(ctx, item, step, wait.Remote, why)
	}
	if err := d.relayRun(ctx, item, target, concluded, findings); err != nil {
		return item, false, target, err
	}
	moved, err = d.advance(ctx, item, target)
	d.entryNotes = ""
	if err != nil {
		return moved, false, target, err
	}
	return moved, d.res.Parked, target, nil
}

// actedOn reports whether the story's ledger holds a remote_wait_acted row for
// this run's conclusion at this step: the guard that outlives the drive, so the
// next `story drive` does not act on a stale conclusion again.
func (d *driver) actedOn(ctx context.Context, remote, step, key string) bool {
	for _, r := range verb.StoryLedgerKind(ctx, d.id, ledger.KindRemoteWaitActed) {
		var p struct {
			Remote string `json:"remote"`
			Step   string `json:"step"`
			RunKey string `json:"run_key"`
		}
		if json.Unmarshal(r.Payload, &p) == nil && p.Remote == remote && p.Step == step && p.RunKey == key {
			return true
		}
	}
	return false
}

// recordActed writes the row actedOn reads, once the edge was taken. It cannot
// undo the move, so a failed write is reported rather than returned.
func (d *driver) recordActed(ctx context.Context, remote, step string, run runstatus.Run, to string) {
	ctx = context.WithoutCancel(ctx)
	body := fmt.Sprintf("remote %s run concluded %s: edge taken from %s", remote, run.Outcome, step)
	payload := map[string]any{"remote": remote, "step": step, "run_key": run.Key(), "outcome": run.Outcome, "to": to}
	if err := d.appendRow(ctx, ledger.KindRemoteWaitActed, body, payload); err != nil {
		fmt.Fprintf(d.out, "drive: %s: could not record that the run's conclusion was acted on: %v\n", d.id, err)
	}
}

// relayRun hands a concluded run's findings to whoever performs the step the
// story is about to move to, before the move, so the performer's entry carries
// them: a named agent gets a message the dispatch payload includes, the executor
// binding gets them as notes on its prompt. A step nobody performs has no one to
// tell, and a run with no findings has nothing to say.
func (d *driver) relayRun(ctx context.Context, item workitem.Item, target, concluded, findings string) error {
	if findings == "" {
		return nil
	}
	perf, err := d.opts.Performers.StepPerformer(ctx, item, target)
	if err != nil {
		return &halt{step: target, err: err, what: err.Error()}
	}
	body := concluded + "\n\n" + findings
	switch perf.Kind {
	case verb.PerformerDispatched:
		if err := d.message(ctx, perf.Agent, body); err != nil {
			return &halt{step: target, performer: perf.Agent, err: err, what: err.Error()}
		}
	case verb.PerformerExecutor:
		d.entryNotes = body
	}
	return nil
}

// fail ends a drive that cannot continue. Before the first presentation a story's
// drive is a plain refusal, while an execution's is recorded as stopped (nothing
// is parked: nothing was presented). After it, the item is parked with the reason
// (a Result), or, where it cannot be parked, a drive-stopped row records the stop
// and the error is a *Stopped: the drive never ends short of its exit without a
// record.
func (d *driver) fail(ctx context.Context, item workitem.Item, err error) (Result, error) {
	var stopped *Stopped
	if errors.As(err, &stopped) {
		return Result{}, err
	}
	if !d.presented && !d.exec {
		return Result{}, err
	}
	var h *halt
	if !errors.As(err, &h) {
		h = &halt{step: item.Status, what: err.Error(), err: err}
	}
	if !d.presented {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
		defer cancel()
		performer := h.performer
		if performer == "" {
			performer = d.performerOf(ctx, item, h.step)
		}
		return Result{}, d.stop(ctx, item, h.step, performer, reasonf(h.step, performer, h.what),
			fmt.Errorf("nothing was presented, so nothing was parked: %w", err))
	}
	if perr := d.park(ctx, item, h.step, h.performer, h.what); perr != nil {
		return Result{}, perr
	}
	return d.res, nil
}

// stopped reports whether the story is somewhere the drive does not move it from:
// a terminal step, a park, or a wait step.
func (d *driver) stopped(item workitem.Item) bool {
	return d.route.Spec.IsAtRest(item.Status)
}

// finish reports a story that is already stopped.
func (d *driver) finish(ctx context.Context, item workitem.Item) Result {
	d.res.Status = item.Status
	if d.route.Spec.IsTerminalState(item.Status) {
		d.res.Terminal = true
		return d.res
	}
	if d.route.Spec.IsWaitState(item.Status) {
		d.res.Waiting = true
		d.res.Reason = "the route's wait step; the story rests here until a move takes it onward"
		return d.res
	}
	d.res.Parked = true
	d.res.Reason = d.recordedReason(ctx)
	return d.res
}

// successor is the spine step after status.
func (d *driver) successor(status string) (string, error) {
	for i, name := range d.spine {
		if name != status {
			continue
		}
		if i+1 >= len(d.spine) {
			break
		}
		next := d.spine[i+1]
		if !d.route.Spec.HasEdge(status, next) {
			return "", fmt.Errorf("drive: the route declares no edge %s → %s", status, next)
		}
		return next, nil
	}
	return "", fmt.Errorf("drive: status %q is not a step on the route's spine, or has no step after it", status)
}

// preflight refuses, before any transition, a route this drive cannot walk
// unattended: a step the driving session itself would have to perform. It asks
// the engine's performer rule about the current step and every step after it, so
// placement and in-loop bindings are respected, not re-derived here.
func (d *driver) preflight(ctx context.Context, item workitem.Item) error {
	at := -1
	for i, name := range d.spine {
		if name == item.Status {
			at = i
		}
	}
	if at < 0 {
		return fmt.Errorf("drive: status %q is not a step on the route's spine", item.Status)
	}
	executorChecked := false
	for _, step := range d.spine[at:] {
		perf, err := d.opts.Performers.StepPerformer(ctx, item, step)
		if err != nil {
			return fmt.Errorf("drive: step %q: %w", step, err)
		}
		switch perf.Kind {
		case verb.PerformerInLoop:
			return fmt.Errorf(
				"drive: step %q is allocated to agent %q, which .satelle/workflows/agents.toml [%s] binds in-loop — bind a headless CLI to drive unattended",
				step, perf.Agent, perf.Agent)
		case verb.PerformerExecutor:
			if executorChecked {
				continue
			}
			executorChecked = true
			if perf.ExecutorInLoop {
				return fmt.Errorf(
					"drive: step %q is allocated to agent=executor and .satelle/workflows/agents.toml [executor] command is in-loop — bind a headless CLI to drive unattended",
					step)
			}
		}
	}
	return nil
}

// advance presents the edge item.Status → next until it is accepted, relaying each
// rejection back to the step's performer in between, then performs the step if it
// is executor-allocated. It returns the story as it then stands; Result.Parked is
// set when the story was parked instead.
func (d *driver) advance(ctx context.Context, item workitem.Item, next string) (workitem.Item, error) {
	from := item.Status
	limit := 0
	if st, ok := d.route.Spec.StateNamed(next); ok && st.RejectBudget < 1 {
		limit = unbudgetedPresentations
	}
	for attempt := 1; ; attempt++ {
		fmt.Fprintf(d.out, "drive: %s presenting %s → %s\n", d.id, from, next)
		_, err := d.set(ctx, next)
		if err == nil {
			d.presented = true
			break
		}
		var rejected *verb.GateRejection
		switch {
		case errors.As(err, &rejected):
			d.presented = true
			fmt.Fprintf(d.out, "drive: %s %s → %s rejected\n%s\n", d.id, from, next, rejected.Notes())
			if limit > 0 && attempt >= limit {
				skill := ""
				if len(rejected.Rejects) > 0 {
					skill = rejected.Rejects[0].Skill
				}
				step, performer := d.blame(ctx, item, next, skill)
				return item, d.park(ctx, item, step, performer, fmt.Sprintf(
					"the %s → %s edge was rejected %d times and its step declares no reject_budget; last objection: %s",
					from, next, attempt, rejected.Notes()))
			}
			if err := d.answerRejection(ctx, item, next, rejected.Notes()); err != nil {
				return item, err
			}
			if d.res.Parked {
				return item, nil
			}
		default:
			// A context that ended mid-presentation (a timeout, a spend cap) is a
			// stop worth a record even on the first presentation.
			if ctx.Err() != nil {
				d.presented = true
			}
			return item, &halt{step: next, err: err, what: fmt.Sprintf("presenting %s → %s was refused: %v", from, next, err)}
		}
	}
	moved, err := d.load(ctx)
	if err != nil {
		return item, err
	}
	switch {
	case moved.Status == next:
	case d.route.Spec.IsParkState(moved.Status):
		// The set's own mechanism parked it: a performer rejected the premise, or a
		// spend bound was broken where the story would park.
		d.parkedByMechanism(ctx, moved, from, next)
		return moved, nil
	default:
		return moved, &halt{step: next, what: fmt.Sprintf("presenting %s → %s left the story at %q", from, next, moved.Status)}
	}
	move := Move{From: from, To: next}
	d.res.Moves = append(d.res.Moves, move)
	if d.opts.OnMove != nil {
		d.opts.OnMove(move)
	}
	d.res.Status = next
	d.res.Terminal = d.route.Spec.IsTerminalState(next)
	return moved, d.performEntered(ctx, moved, next)
}

// performEntered runs the headless executor over a step the story has just
// entered, when the workflow allocates that step to agent=executor. A step a
// named agent performs ran during its entry, and an agent-less step has nothing
// to run.
func (d *driver) performEntered(ctx context.Context, item workitem.Item, step string) error {
	perf, err := d.opts.Performers.StepPerformer(ctx, item, step)
	if err != nil {
		return fmt.Errorf("drive: step %q: %w", step, err)
	}
	notes := d.entryNotes
	d.entryNotes = ""
	if perf.Kind != verb.PerformerExecutor {
		return nil
	}
	return d.perform(ctx, item, step, notes)
}

// perform runs the executor binding over step. A performer that rejects parks the
// story with its notes; a run that fails stops the drive (a park, or a
// drive-stopped row), leaving the story at the step, where a re-run of the drive
// finds the exit gates judging what was done. The run's row is written detached
// from ctx, so a run the context ended is still recorded.
func (d *driver) perform(ctx context.Context, item workitem.Item, step, notes string) error {
	fmt.Fprintf(d.out, "drive: %s performing %s with the executor binding\n", d.id, step)
	run, err := d.opts.Performers.PerformExecutorStep(ctx, item, step, notes)
	record := context.WithoutCancel(ctx)
	var rejected *verb.PerformerReject
	if errors.As(err, &rejected) {
		_ = verb.RecordStepPerformance(record, item, step, run, nil)
		return d.park(ctx, item, step, "executor", fmt.Sprintf("the executor rejected step %s: %s", step, rejected.Error()))
	}
	// A run the skill-evidence check refused (skill_evidence = reject) stops the
	// drive as a failed run does: the story stays at the step.
	if rerr := verb.RecordStepPerformance(record, item, step, run, err); err == nil && rerr != nil {
		err = rerr
	}
	if err != nil {
		return &halt{step: step, performer: "executor", err: err, what: fmt.Sprintf("the executor run failed: %v", err)}
	}
	return nil
}

// rejectionTarget is verb.RejectionTarget over the drive's performers, with a
// performer that cannot be resolved stopping the drive at its step.
func (d *driver) rejectionTarget(ctx context.Context, item workitem.Item, to string) (step string, target, fromPerf verb.PerformerResolution, err error) {
	step, target, fromPerf, err = verb.RejectionTarget(ctx, d.opts.Performers, item, to)
	if err != nil {
		return "", target, fromPerf, &halt{step: step, err: err, what: err.Error()}
	}
	return step, target, fromPerf, nil
}

// blame names the step and performer a rejected edge into to is held against, for
// the reason a park records. A step nobody performs is blamed on the gate that
// rejected it (skill, when known).
func (d *driver) blame(ctx context.Context, item workitem.Item, to, skill string) (step, performer string) {
	step, target, _, err := d.rejectionTarget(ctx, item, to)
	if err != nil {
		return to, ""
	}
	performer = performerName(target)
	if performer == "none" {
		performer = "gate"
		if skill != "" {
			performer += ":" + skill
		}
	}
	return step, performer
}

// performerName is how a reason names a step's performer.
func performerName(perf verb.PerformerResolution) string {
	switch {
	case perf.Kind == verb.PerformerExecutor:
		return "executor"
	case perf.Agent != "":
		return perf.Agent
	}
	return "none"
}

// answerRejection carries a rejected edge's findings to whoever must act on them
// before the edge is presented again (rejectionTarget names them). A named
// performer gets the findings as a message and, when the step it performed
// declares a rework relay, that relay runs; the executor binding is re-run with
// them appended to its prompt. A relay that ends without agreement, or an
// executor that rejects, parks the story (Result.Parked) instead.
func (d *driver) answerRejection(ctx context.Context, item workitem.Item, to, notes string) error {
	from := item.Status
	step, target, fromPerf, err := d.rejectionTarget(ctx, item, to)
	if err != nil {
		return err
	}
	switch target.Kind {
	case verb.PerformerDispatched:
		if err := d.message(ctx, target.Agent, notes); err != nil {
			return &halt{step: step, performer: target.Agent, err: err, what: err.Error()}
		}
	case verb.PerformerExecutor:
		return d.perform(ctx, item, from, notes)
	}
	if fromPerf.Kind != verb.PerformerDispatched || d.opts.Rework == nil || !d.declaresRework(from) {
		return nil
	}
	rw, err := d.opts.Rework(ctx, d.id)
	if err != nil {
		return &halt{step: from, performer: fromPerf.Agent, err: err, what: fmt.Sprintf("the rework relay failed: %v", err)}
	}
	if rw.Converged {
		return nil
	}
	objection := rw.LastObjection
	if objection == "" {
		objection = notes
	}
	return d.park(ctx, item, from, fromPerf.Agent, fmt.Sprintf(
		"the %s rework relay ended %s after %d round(s) without agreement; last objection: %s",
		from, rw.Outcome, rw.Rounds, objection))
}

func (d *driver) declaresRework(step string) bool {
	for _, rw := range d.route.Reworks {
		if rw.Step == step {
			return true
		}
	}
	return false
}

// message records the findings as a directed message to the performer, through
// the same verb `satelle story message` uses, so its next payload carries them.
func (d *driver) message(ctx context.Context, to, body string) error {
	req, _ := json.Marshal(map[string]string{"id": d.id, "from": "orchestrator", "to": to, "body": body})
	if _, err := verb.Dispatch(ctx, "story-message", req); err != nil {
		return fmt.Errorf("drive: relay to %s: %w", to, err)
	}
	return nil
}

// park ends the drive on what it was missing: it moves the story to the route's
// park state with the reason (step, performer and what was missing) recorded on
// its timeline first, which is what the park gate reads. Where the story cannot be
// parked — the workflow offers no park state, or the park is refused — a
// drive-stopped row records the stop instead and the error is a *Stopped, so no
// end of a drive is without a record. An empty performer is derived from step.
// The writes run detached from ctx: the context that ended may be the very thing
// being recorded.
func (d *driver) park(ctx context.Context, item workitem.Item, step, performer, what string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	if performer == "" {
		performer = d.performerOf(ctx, item, step)
	}
	reason := reasonf(step, performer, what)
	state := verb.ResumeParkName(ctx, item)
	if state == "" {
		return d.stop(ctx, item, step, performer, reason, errors.New("the workflow offers no park state to hold the question"))
	}
	if err := d.recordParkComment(ctx, item, state, step, performer, reason); err != nil {
		return d.stop(ctx, item, step, performer, reason, fmt.Errorf("record the question: %w", err))
	}
	if _, err := d.set(ctx, state); err != nil {
		return d.stop(ctx, item, step, performer, reason, fmt.Errorf("park at %s: %w", state, err))
	}
	fmt.Fprintf(d.out, "drive: %s parked at %s\n", d.id, state)
	d.res.Parked, d.res.Status, d.res.Reason = true, state, reason
	d.recordExecutionStop(ctx, item, step, performer, reason)
	return nil
}

// parkedByMechanism reports a story the set's own mechanism parked at the step it
// was presenting (a performer rejected the premise, or a spend bound broke): the
// mechanism wrote the reason, and the drive adds its own park record, naming the
// step and performer and marking the park as a drive's, without a second move.
func (d *driver) parkedByMechanism(ctx context.Context, moved workitem.Item, from, step string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	performer := d.performerOf(ctx, moved, step)
	reason := reasonf(step, performer, d.recordedReason(ctx))
	was := moved
	was.Status = from
	_ = d.recordParkComment(ctx, was, moved.Status, step, performer, reason)
	d.res.Parked, d.res.Status, d.res.Reason = true, moved.Status, reason
	d.recordExecutionStop(ctx, was, step, performer, reason)
}

// stop records a drive that ended with the item not parked: one drive-stopped
// row with the reason, then the *Stopped the caller exits 1 on.
func (d *driver) stop(ctx context.Context, item workitem.Item, step, performer, reason string, parkErr error) error {
	if err := d.recordStop(ctx, item, step, performer, reason, parkErr); err != nil {
		parkErr = fmt.Errorf("%w (and the stop could not be recorded: %v)", parkErr, err)
	}
	fmt.Fprintf(d.out, "drive: %s stopped at %s: %s\n", d.id, item.Status, reason)
	return &Stopped{ID: d.id, Status: item.Status, Reason: reason, ParkErr: parkErr}
}

// recordExecutionStop records the stop of an execution the drive parked, which
// the park alone would leave without a drive-stopped row or an alert on its
// subject. The park stands whether or not this write succeeds, so a failure is
// logged. A story's park is its record and needs nothing more.
func (d *driver) recordExecutionStop(ctx context.Context, item workitem.Item, step, performer, reason string) {
	if !d.exec {
		return
	}
	if err := d.recordStop(ctx, item, step, performer, reason, nil); err != nil {
		fmt.Fprintf(d.out, "drive: %s: the stop could not be recorded: %v\n", d.id, err)
	}
}

// recordStop writes the drive-stopped row for a drive that ended short of its
// route's exit, once. An execution's row carries outcome "stopped", and when its
// story_id input names a story the same stop is written there as an
// execution_stopped alert. parkErr is why the item was not parked, nil when it was.
func (d *driver) recordStop(ctx context.Context, item workitem.Item, step, performer, reason string, parkErr error) error {
	if d.recorded {
		return nil
	}
	d.recorded = true
	payload := d.payload(ctx, item, "", step, performer, reason)
	if d.exec {
		payload["outcome"] = OutcomeStopped
	}
	if parkErr != nil {
		payload["park_error"] = parkErr.Error()
	}
	err := d.appendRow(ctx, ledger.KindDriveStopped, reason, payload)
	if subject := subjectOf(item); d.exec && subject != "" {
		alert := maps.Clone(payload)
		alert["execution"], alert["task"] = d.id, item.ParentID
		if aerr := d.appendRowTo(ctx, subject, ledger.KindExecutionStopped, reason, alert); aerr != nil {
			err = errors.Join(err, fmt.Errorf("alert %s: %w", subject, aerr))
		}
	}
	return err
}

// subjectOf is the story an execution was made about: its story_id input.
func subjectOf(item workitem.Item) string {
	subject, _ := item.InputValues[workitem.InputStoryID].(string)
	return strings.TrimSpace(subject)
}

// recordParkComment writes the comment row a park is announced by. Its payload is
// what the server reads to tell a remote's park from a person's.
func (d *driver) recordParkComment(ctx context.Context, item workitem.Item, to, step, performer, reason string) error {
	return d.appendRow(ctx, ledger.KindComment, reason, d.payload(ctx, item, to, step, performer, reason))
}

func (d *driver) appendRow(ctx context.Context, kind, body string, payload map[string]any) error {
	return d.appendRowTo(ctx, d.id, kind, body, payload)
}

// appendRowTo writes a row on the ledger of id, which is d.id or, for an
// execution's alert, its subject story.
func (d *driver) appendRowTo(ctx context.Context, id, kind, body string, payload map[string]any) error {
	entry, _ := json.Marshal(map[string]any{
		"story_id": id, "kind": kind, "actor": "orchestrator", "body": body, "payload": payload,
	})
	_, err := verb.Dispatch(ctx, "ledger-append", entry)
	return err
}

// payload is the record of a drive's stop: the move it stands for, the step and
// performer it names, the reason, what the performer last said, and the node it
// happened on. source "drive" is what marks the row a headless drive's.
func (d *driver) payload(ctx context.Context, item workitem.Item, to, step, performer, reason string) map[string]any {
	return map[string]any{
		"from": item.Status, "to": to, "source": "drive",
		"step": step, "performer": performer, "reason": reason,
		"last_message": d.lastMessage(ctx),
		"location_id":  d.opts.Location, "location_label": d.opts.LocationLabel,
	}
}

// lastMessageScan bounds how far back lastMessage reads the story's
// agent_invocation rows for a performer's.
const lastMessageScan = 200

// lastMessage is the last_message of the story's newest performer
// agent_invocation row: the newest row carrying that key, so a reviewer's or a
// summariser's newer row is skipped. "" when no performer row carries one.
func (d *driver) lastMessage(ctx context.Context) string {
	req, _ := json.Marshal(map[string]any{"story_id": d.id, "kind": ledger.KindAgentInvocation, "limit": lastMessageScan})
	raw, err := verb.Dispatch(ctx, "ledger-list", req)
	if err != nil {
		return ""
	}
	var rows []ledger.Entry
	if json.Unmarshal(raw, &rows) != nil {
		return ""
	}
	for i := len(rows) - 1; i >= 0; i-- {
		var p struct {
			LastMessage *string `json:"last_message"`
		}
		if json.Unmarshal(rows[i].Payload, &p) == nil && p.LastMessage != nil {
			return *p.LastMessage
		}
	}
	return ""
}

// performerOf names the performer of step for a reason: "executor", the named
// agent, or "none" (the step has no performer, so its gates are what stopped).
func (d *driver) performerOf(ctx context.Context, item workitem.Item, step string) string {
	perf, err := d.opts.Performers.StepPerformer(ctx, item, step)
	if err != nil {
		return "unknown"
	}
	return performerName(perf)
}

// set presents a status move through the story-set verb.
func (d *driver) set(ctx context.Context, status string) (json.RawMessage, error) {
	req, _ := json.Marshal(map[string]string{"id": d.id, "status": status})
	return verb.Dispatch(ctx, "story-set", req)
}

func (d *driver) load(ctx context.Context) (workitem.Item, error) {
	req, _ := json.Marshal(map[string]string{"id": d.id})
	raw, err := verb.Dispatch(ctx, "story-get", req)
	if err != nil {
		return workitem.Item{}, fmt.Errorf("drive: %w", err)
	}
	var item workitem.Item
	if err := json.Unmarshal(raw, &item); err != nil {
		return workitem.Item{}, fmt.Errorf("drive: decode %s: %w", d.id, err)
	}
	return item, nil
}

// recordedReason is the newest comment on the story's timeline — what a park
// mechanism, or the drive, wrote down as the reason.
func (d *driver) recordedReason(ctx context.Context) string {
	req, _ := json.Marshal(map[string]any{"story_id": d.id, "kind": ledger.KindComment, "limit": 1})
	raw, err := verb.Dispatch(ctx, "ledger-list", req)
	if err != nil {
		return ""
	}
	var rows []ledger.Entry
	if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
		return ""
	}
	return strings.TrimSpace(rows[len(rows)-1].Body)
}
