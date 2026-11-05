package remotenode

// A node of a task-kind remote (remotedef [drive] kind = "task") does not drive
// the story it was woken for. A wake runs the remote's task once: one execution
// of it, created with the wake's subject as its inputs, driven through the
// task's own route and validators by the same drive a story uses, and reported
// to satelle.dev on the heartbeat as queued, running (with the step) and
// concluded (with the outcome). The subject story item is never loaded or edited:
// the only rows written on it are an execution_link row, written when the run
// concludes, and an execution_stopped alert, written by the drive when the run ends
// short of its route's exit. The subject's and the execution's hosted chains are
// read first, so those rows build on the hosted head, and the run is then pushed to
// the team workspace under the node's own token.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/runstatus"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// DefaultStatusResendInterval is how often the heartbeat is re-sent while a run is
// in progress when the server has not said how long it waits: a third of the 90
// seconds after which satelle-server lists a silent node as lost (remotes
// NodeLostAfter), so two heartbeats can be missed before it does.
const DefaultStatusResendInterval = 30 * time.Second

// resendDivisor divides the server's reported lost-after window into the resend
// interval.
const resendDivisor = 3

// concludeGrace bounds the final heartbeat, which is sent even when the run's
// context has ended.
const concludeGrace = 10 * time.Second

// ExecutionTag marks the execution a node made for its wake: remote-node:<node id>.
// A node runs one wake and satelle-server starts one node per wake, so the node
// id is the run's identity.
const ExecutionTag = "remote-node:"

// Input names a run descriptor fills, when the task declares them.
const (
	InputStoryID       = workitem.InputStoryID
	InputTrigger       = workitem.InputTrigger
	InputTriggerSource = workitem.InputTriggerSource
)

// StatusSender is the heartbeat call; *NodeClient is the real one.
type StatusSender interface {
	Status(ctx context.Context, s NodeStatus) (StatusReply, error)
}

// PublishReport is what a run's publish sent: the project and how many stories'
// events, items, ledger rows, run outputs and story attachments the server took,
// and each attachment it did not take, as "story <id> document <file>: <reason>".
type PublishReport struct {
	Project                                      string
	Stories, Items, Ledger, Outputs, Attachments int
	AttachmentFailures                           []string
}

// TaskRun is what RunTask needs.
type TaskRun struct {
	// Def is the node's definition; it names the task (Def.TaskID).
	Def remotedef.Definition
	// Desc is the wake's subject, from the run descriptor.
	Desc RunDescriptor
	// NodeID is the node's id on satelle.dev.
	NodeID string
	// Client sends the heartbeat.
	Client StatusSender
	// Drive is how the execution is driven: the performers, a rework relay, the
	// log. Its OnMove is called as well as the node's own.
	Drive drive.Options
	// Publish pushes the run to the node's project once it has concluded: the
	// execution, its ledger rows (the subject's link row among them) and its run
	// output, through the push a local satelle makes. Nil is no push.
	Publish func(ctx context.Context, exec workitem.Item, subject string) (PublishReport, error)
	// Fetch, when set, brings the hosted chain of each id into the node's store
	// before the run writes to it, so those writes, and so the push, build on the
	// hosted head (sty_0d621eaf). It is called with the subject before the
	// execution is made or driven, and with the execution before it is driven. A
	// failed fetch is logged and the run goes on: local first, never blocked. A
	// fetch satelle.dev refused (ErrRunRefused), or one for a story the hosted
	// project does not hold (ErrSubjectAbsent), stops the run there, and nothing of
	// it is published.
	Fetch func(ctx context.Context, ids ...string) error
	// ResendEvery, when set, replaces the heartbeat resend interval (a test's).
	ResendEvery time.Duration
}

// TaskRunResult is how a task run ended.
type TaskRunResult struct {
	// Execution is the execution the wake made.
	Execution string
	// Outcome is what the node reported: the outcome on the execution's run-output
	// doc, else performed when the route reached its end and reject when it did not.
	Outcome string
	// Findings is the text the performer recorded on that run-output doc, reported
	// with the outcome.
	Findings string
	// Drive is where the drive stopped.
	Drive drive.Result
}

// RunTask runs the node's wake: it creates the execution (or finds the one this
// node already made), drives it, and reports the run. It returns after the
// concluded heartbeat, which is sent however the run ended. An error is a run
// that could not be made or driven; the node still reports it concluded with
// outcome reject.
func RunTask(ctx context.Context, tr TaskRun) (TaskRunResult, error) {
	rep := newStatusReporter(tr.Client, tr.Desc.StoryID, tr.ResendEvery, tr.Drive.Out)
	defer rep.stop()
	rep.send(ctx, NodeStatus{RunState: runstatus.Queued})

	var res TaskRunResult
	var exec workitem.Item
	err := func() error {
		if err := tr.fetch(ctx, tr.Desc.StoryID); err != nil {
			return err
		}
		var err error
		if exec, err = executionFor(ctx, tr); err != nil {
			return err
		}
		res.Execution = exec.ID
		if err := tr.fetch(ctx, exec.ID); err != nil {
			return err
		}
		opts := tr.Drive
		onMove := opts.OnMove
		opts.OnMove = func(m drive.Move) {
			if onMove != nil {
				onMove(m)
			}
			rep.send(ctx, NodeStatus{RunState: runstatus.Running, Step: m.To})
		}
		if res.Drive, err = drive.Story(ctx, exec.ID, opts); err != nil {
			return err
		}
		if cur, gerr := getItem(ctx, "execution", exec.ID); gerr == nil {
			res.Outcome = verb.RunOutcome(cur)
			res.Findings = verb.RunFindings(cur)
		}
		return nil
	}()

	switch {
	case err != nil:
		res.Outcome = verb.OutcomeReject
	case res.Outcome == "" && res.Drive.Terminal:
		res.Outcome = verb.OutcomePerformed
	case res.Outcome == "":
		res.Outcome = verb.OutcomeReject
	}
	if res.Findings == "" && res.Outcome == verb.OutcomeReject {
		res.Findings = stopFindings(err, res.Drive)
	}
	if exec.ID != "" && !errors.Is(err, ErrRunRefused) {
		if refused := tr.record(ctx, exec, res.Outcome); len(refused) > 0 {
			res.Findings = joinFindings(res.Findings, agentcli.RedactSecrets("attachments refused: "+strings.Join(refused, "; ")))
		}
	}
	rep.conclude(ctx, res.Outcome, res.Findings)
	return res, err
}

// stopFindings is what a rejected run the performer recorded no findings for
// reports instead (sty_4bf6adbe): the drive's error, else the reason it parked or
// stopped, so the node and run records say why rather than nothing. The text is
// already scrubbed of resolved secret values where it was made (agentcli and
// agentstep); the pattern scrub here is a backstop.
func stopFindings(err error, res drive.Result) string {
	text := res.Reason
	if err != nil {
		text = err.Error()
	}
	return agentcli.RedactSecrets(text)
}

// joinFindings appends more to findings on a line of its own.
func joinFindings(findings, more string) string {
	if findings == "" {
		return more
	}
	return findings + "\n" + more
}

// record writes the run's link row on the subject and pushes the run, before the
// concluded heartbeat so the server holds the rows when it lists the run
// concluded. Neither failing changes the run's outcome; each is logged. It
// returns the attachments the publish reported refused, whether or not the rest of
// the publish failed, for the concluded findings.
func (tr TaskRun) record(ctx context.Context, exec workitem.Item, outcome string) []string {
	logf := func(what string, err error) {
		if tr.Drive.Out != nil {
			fmt.Fprintf(tr.Drive.Out, "node: %s: %v\n", what, err)
		}
	}
	if err := tr.linkSubject(ctx, exec, outcome); err != nil {
		logf("execution link", err)
	}
	if tr.Publish == nil {
		return nil
	}
	rep, err := tr.Publish(ctx, exec, tr.Desc.StoryID)
	if err != nil {
		logf("workstate", err)
		if tr.Drive.Out != nil {
			fmt.Fprintf(tr.Drive.Out, "node: workstate: %d attachment(s) uploaded\n", rep.Attachments)
		}
		return rep.AttachmentFailures
	}
	if tr.Drive.Out != nil {
		fmt.Fprintf(tr.Drive.Out, "node: workstate: published to %s: the events of %d stor(y/ies), %d item(s), %d ledger row(s), %d output(s), %d attachment(s)\n",
			rep.Project, rep.Stories, rep.Items, rep.Ledger, rep.Outputs, rep.Attachments)
	}
	return rep.AttachmentFailures
}

// ErrRunRefused marks a Fetch error after which satelle.dev may be asked nothing
// more: it refused the node token, or fenced the node out of the project.
var ErrRunRefused = errors.New("satelle.dev refused the node's work state")

// ErrSubjectAbsent marks a Fetch error for a story neither the node's store nor
// the hosted project holds: every story verb the run makes on it would fail, so
// the run stops before it is driven.
var ErrSubjectAbsent = errors.New("the run's story is not in the hosted project")

// fetch runs Fetch for id, logging a failure: the write that follows goes on with
// what the node's store holds. A refusal (ErrRunRefused) or an absent story
// (ErrSubjectAbsent) is returned, and the run stops on it.
func (tr TaskRun) fetch(ctx context.Context, id string) error {
	if tr.Fetch == nil || id == "" {
		return nil
	}
	err := tr.Fetch(ctx, id)
	if errors.Is(err, ErrRunRefused) || errors.Is(err, ErrSubjectAbsent) {
		return err
	}
	if err != nil && tr.Drive.Out != nil {
		fmt.Fprintf(tr.Drive.Out, "node: workstate: the hosted chain of %s was not read before this write: %v\n", id, err)
	}
	return nil
}

// linkSubject appends the execution_link row on the wake's subject story, once per
// execution. A wake with no subject writes none.
func (tr TaskRun) linkSubject(ctx context.Context, exec workitem.Item, outcome string) error {
	subject := tr.Desc.StoryID
	if subject == "" {
		return nil
	}
	var linked []ledger.Entry
	if err := dispatchJSON(ctx, "ledger-list", map[string]any{"story_id": subject, "kind": ledger.KindExecutionLink}, &linked); err != nil {
		return err
	}
	for _, e := range linked {
		var p ledger.ExecutionLinkPayload
		if json.Unmarshal(e.Payload, &p) == nil && p.Execution == exec.ID {
			return nil
		}
	}
	var row ledger.Entry
	return dispatchJSON(ctx, "ledger-append", map[string]any{
		"story_id": subject,
		"kind":     ledger.KindExecutionLink,
		"actor":    "remote-node",
		"payload":  ledger.ExecutionLinkPayload{Execution: exec.ID, Task: tr.Def.TaskID(), Remote: tr.NodeID, Outcome: outcome},
		"refs":     map[string]string{"execution": exec.ID},
	}, &row)
}

// executionFor is the execution of the node's task made for this node: the one it
// already made, else a new one whose inputs are the descriptor's fields that the
// task declares.
func executionFor(ctx context.Context, tr TaskRun) (workitem.Item, error) {
	taskID := tr.Def.TaskID()
	tag := ExecutionTag + tr.NodeID
	var made []workitem.Item
	if err := dispatchJSON(ctx, "execution-list", map[string]any{"parent_id": taskID, "tag": tag}, &made); err != nil {
		return workitem.Item{}, err
	}
	if len(made) > 0 {
		return made[0], nil
	}
	task, err := getItem(ctx, "task", taskID)
	if err != nil {
		return workitem.Item{}, err
	}
	var exec workitem.Item
	// A node's repo gates creation on the story structure check, which asks every
	// kind for a body that is not its title and a category (workitem.RunCategory).
	err = dispatchJSON(ctx, "execution-create", map[string]any{
		"title": "wake of " + task.Title + " on " + tr.NodeID,
		"body": fmt.Sprintf("One run of %s for the wake of node %s: subject %q, trigger %q, source %q.",
			taskID, tr.NodeID, tr.Desc.StoryID, tr.Desc.Trigger, tr.Desc.TriggerSource),
		"category":  workitem.RunCategory,
		"parent_id": taskID,
		"tags":      []string{tag},
		"inputs":    wakeInputs(task.Inputs, tr.Desc),
	}, &exec)
	return exec, err
}

// wakeInputs maps the run descriptor onto the inputs the task declares
// (workitem.MapInputs).
func wakeInputs(decls []workitem.InputDecl, d RunDescriptor) map[string]string {
	return workitem.MapInputs(decls, map[string]string{InputStoryID: d.StoryID, InputTrigger: d.Trigger, InputTriggerSource: d.TriggerSource})
}

// getItem reads one work item through the get verb of its group ("task" or
// "execution").
func getItem(ctx context.Context, group, id string) (workitem.Item, error) {
	var it workitem.Item
	err := dispatchJSON(ctx, group+"-get", map[string]any{"id": id}, &it)
	return it, err
}

// dispatchJSON runs a verb and decodes its answer into out.
var dispatchJSON = verb.DispatchJSON

// ReportStartFailure tells satelle.dev a node could not start the run for storyID:
// one concluded heartbeat with outcome reject and the findings, sent even when ctx
// has ended. It is the failed-start counterpart of RunTask's concluded heartbeat.
func ReportStartFailure(ctx context.Context, client StatusSender, storyID, findings string, out io.Writer) {
	newStatusReporter(client, storyID, 0, out).conclude(ctx, verb.OutcomeReject, findings)
}

// statusReporter sends a run's heartbeat and, until the run concludes, re-sends
// the latest one so satelle.dev does not list the node lost while a long step
// runs. Sends are serialised, so the server sees them in the order they were made
// and none follows the concluded one.
type statusReporter struct {
	client   StatusSender
	storyID  string
	override time.Duration
	log      func(error)
	// beat, when set, logs one live heartbeat as it is sent: the host's vitals,
	// so a node that stalls leaves the state it was in.
	beat func()
	// refresh, when set, makes each re-send report the status it returns now rather
	// than the last one sent: a node-lifetime heartbeat whose step changes.
	refresh func() NodeStatus

	sendMu sync.Mutex // serialises sends; held across the PUT
	mu     sync.Mutex // guards the fields below
	last   NodeStatus
	every  time.Duration
	done   bool

	quit chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

func newStatusReporter(client StatusSender, storyID string, override time.Duration, out io.Writer) *statusReporter {
	r := &statusReporter{client: client, storyID: storyID, override: override, quit: make(chan struct{}), every: DefaultStatusResendInterval}
	r.log = func(err error) {
		if out != nil {
			fmt.Fprintf(out, "node: status: %v\n", err)
		}
	}
	if out != nil {
		r.beat = func() { fmt.Fprintf(out, "node: heartbeat: %s\n", ReadVitals(procRoot)) }
	}
	if override > 0 {
		r.every = override
	}
	return r
}

// send reports s as the node's status, starting the resend loop with the first.
// A refused heartbeat is logged and does not fail the run.
func (r *statusReporter) send(ctx context.Context, s NodeStatus) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	s.State, s.StoryID = StateRunning, r.storyID
	r.last = s
	r.mu.Unlock()
	r.logBeat()
	r.put(ctx, s)
	r.once.Do(func() {
		r.wg.Add(1)
		go r.resend()
	})
}

// logBeat logs a live heartbeat about to be sent.
func (r *statusReporter) logBeat() {
	if r.beat != nil {
		r.beat()
	}
}

// put sends one heartbeat and learns the resend interval from its reply.
func (r *statusReporter) put(ctx context.Context, s NodeStatus) {
	reply, err := r.client.Status(ctx, s)
	if err != nil {
		if ctx.Err() == nil {
			r.log(err)
		}
		return
	}
	if r.override == 0 && reply.LostAfterSeconds > 0 {
		r.mu.Lock()
		r.every = time.Duration(reply.LostAfterSeconds) * time.Second / resendDivisor
		r.mu.Unlock()
	}
}

// resend re-sends the latest heartbeat every interval until stop.
func (r *statusReporter) resend() {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		every := r.every
		r.mu.Unlock()
		select {
		case <-r.quit:
			return
		case <-time.After(every):
		}
		r.sendMu.Lock()
		r.mu.Lock()
		if r.refresh != nil && !r.done {
			r.last = r.refresh()
			r.last.State, r.last.StoryID = StateRunning, r.storyID
		}
		last, done := r.last, r.done
		r.mu.Unlock()
		if !done {
			r.logBeat()
			r.put(context.Background(), last)
		}
		r.sendMu.Unlock()
	}
}

// conclude sends the concluded heartbeat with outcome and the findings behind it,
// even when ctx has ended, and sends nothing after it.
func (r *statusReporter) conclude(ctx context.Context, outcome, findings string) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), concludeGrace)
	defer cancel()
	r.put(cctx, NodeStatus{State: StateRunning, StoryID: r.storyID, RunState: runstatus.Concluded, Outcome: outcome, Findings: findings})
}

// Heartbeat is the heartbeat of a node that runs no task: it keeps satelle.dev
// hearing from the node for as long as the node lives, whether or not a job is in
// flight or the channel is attached. It is the task run's status mechanism
// (statusReporter), started on the node rather than on a run.
type Heartbeat struct{ r *statusReporter }

// StartHeartbeat sends state() as the node's status now, then re-sends it, re-read
// each time, until Stop. every is the resend interval; 0 is
// DefaultStatusResendInterval, or a third of the window the server reports. A
// refused heartbeat is logged to out and does not stop the node.
func StartHeartbeat(ctx context.Context, client StatusSender, every time.Duration, state func() NodeStatus, out io.Writer) *Heartbeat {
	r := newStatusReporter(client, "", every, out)
	r.refresh = state
	r.send(ctx, state())
	return &Heartbeat{r: r}
}

// Stop ends the re-sends and waits for one in flight; nothing is sent after it.
// It is safe to call more than once.
func (h *Heartbeat) Stop() {
	h.r.mu.Lock()
	h.r.done = true
	h.r.mu.Unlock()
	h.r.stop()
}

// stop ends the resend loop and waits for it.
func (r *statusReporter) stop() {
	select {
	case <-r.quit:
	default:
		close(r.quit)
	}
	r.wg.Wait()
}
