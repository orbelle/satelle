package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// Event kinds the kernel appends beyond the item folds and gate.run.
const (
	KindPerformerRun = "performer.run"
	KindTaskRun      = "task.run"
)

// ExecutionKind is the kind of the item a task run creates.
const ExecutionKind = "execution"

// Location prefixes of a seat.
var seatLocations = []string{LocationLocal, LocationRemote, LocationCloud}

func (k *Kernel) append(id, kind, actor string, payload any) (ledger.Event, error) {
	e, err := k.Ledger.Append(id, kind, actor, payload)
	if err != nil {
		return e, err
	}
	return e, k.Cache.SyncItem(k.Ledger, id)
}

// workflowOf returns the workflow an item was created under.
func (k *Kernel) workflowOf(it item.Item) (*workflow.Workflow, error) {
	w, ok := k.Workflows[it.Workflow]
	if !ok {
		return nil, fmt.Errorf("item %s names workflow %q, which does not exist", it.ID, it.Workflow)
	}
	return w, nil
}

// Get folds an item from its chain on the ledger.
func (k *Kernel) Get(id string) (item.Item, error) {
	it, _, err := k.load(id)
	return it, err
}

func (k *Kernel) load(id string) (item.Item, []ledger.Event, error) {
	if !ledger.ValidItem(id) {
		return item.Item{}, nil, fmt.Errorf("invalid item id %q", id)
	}
	events, err := k.Ledger.Read(id)
	if err != nil {
		return item.Item{}, nil, err
	}
	if len(events) == 0 {
		return item.Item{}, nil, fmt.Errorf("item %s does not exist", id)
	}
	it, err := item.Fold(events)
	return it, events, err
}

// List returns the cached items matching q, after ingesting any unseen
// ledger tail.
func (k *Kernel) List(q cache.Query) ([]item.Item, error) {
	if err := k.Cache.Sync(k.Ledger); err != nil {
		return nil, err
	}
	return k.Cache.Items(q)
}

func (k *Kernel) prefix(kind string) string {
	if p := k.Settings.Items.Prefix[kind]; p != "" {
		return p
	}
	r := []rune(strings.ToLower(kind))
	if len(r) > 3 {
		r = r[:3]
	}
	return string(r)
}

// WorkflowFor is the workflow an item belongs in and that workflow's start
// state: its own workflow when set, else the first matching rule, else the
// default. Create uses it, and so does placing an item that arrived from a
// server without one.
func (k *Kernel) WorkflowFor(it item.Item) (workflow, start string, err error) {
	name := it.Workflow
	if name == "" {
		name = k.Rules.Select(it)
	}
	w, ok := k.Workflows[name]
	if !ok {
		return "", "", fmt.Errorf("workflow %q does not exist", name)
	}
	return name, w.StartState(), nil
}

// Create records a new item. Kind and title are required; status may not be
// given (it is the workflow's start state). The workflow is f.Workflow, else
// the first matching rule, else the default.
func (k *Kernel) Create(f item.Fields, actor string) (item.Item, error) {
	var draft item.Item
	f.Apply(&draft)
	if strings.TrimSpace(draft.Kind) == "" || strings.TrimSpace(draft.Title) == "" {
		return draft, errors.New("an item needs a kind and a title")
	}
	if f.Status != nil {
		return draft, errors.New("status is not set at create; an item starts in its workflow's start state")
	}
	if draft.Parent != "" {
		if _, err := k.Get(draft.Parent); err != nil {
			return draft, fmt.Errorf("parent: %w", err)
		}
	}
	name, start, err := k.WorkflowFor(draft)
	if err != nil {
		return draft, err
	}
	f.Workflow, f.Status = &name, &start
	if k.Git != nil && f.Base == nil {
		if head, err := k.Git.Head(); err == nil {
			f.Base = &head
		}
	}
	prefix := k.prefix(draft.Kind)
	if !ledger.ValidItem(prefix + "_x") {
		return draft, fmt.Errorf("id prefix %q for kind %q is not usable in an id", prefix, draft.Kind)
	}
	for {
		id := item.NewID(prefix)
		if n, _ := k.Ledger.Size(id); n > 0 {
			continue
		}
		if _, err := k.append(id, item.KindCreated, actor, f); err != nil {
			return draft, err
		}
		return k.Get(id)
	}
}

// Update changes the fields f names. Status moves only by Transition, and an
// item keeps the workflow it was created under.
func (k *Kernel) Update(id string, f item.Fields, actor string) (item.Item, error) {
	it, err := k.Get(id)
	if err != nil {
		return it, err
	}
	if f.Status != nil {
		return it, errors.New("status changes only by a transition")
	}
	if f.Workflow != nil && *f.Workflow != it.Workflow {
		return it, errors.New("an item keeps the workflow it was created under")
	}
	if f.Parent != nil && *f.Parent != "" {
		if *f.Parent == id {
			return it, errors.New("an item cannot be its own parent")
		}
		if _, err := k.Get(*f.Parent); err != nil {
			return it, fmt.Errorf("parent: %w", err)
		}
	}
	if _, err := k.append(id, item.KindUpdated, actor, f); err != nil {
		return it, err
	}
	return k.Get(id)
}

// TransitionResult describes a presented transition.
type TransitionResult struct {
	Item  item.Item // after the transition when it moved
	From  string
	To    string
	Moved bool
	Gates []gate.Result
}

// Rejects returns the gates that rejected.
func (r TransitionResult) Rejects() []gate.Result {
	var out []gate.Result
	for _, g := range r.Gates {
		if g.Result == gate.Reject {
			out = append(out, g)
		}
	}
	return out
}

// Transition presents the move of id to `to`: it finds the explicit
// transition, runs its gates, records every gate result, and records the move
// only when no gate rejected. A rejected move is a result, not an error.
func (k *Kernel) Transition(ctx context.Context, id, to, actor string) (TransitionResult, error) {
	it, events, err := k.load(id)
	if err != nil {
		return TransitionResult{}, err
	}
	res := TransitionResult{Item: it, From: it.Status, To: to}
	w, err := k.workflowOf(it)
	if err != nil {
		return res, err
	}
	tr, err := w.Find(it.Status, to, item.PriorStatus(events))
	if err != nil {
		return res, err
	}
	sp := span{}
	if k.Git != nil {
		sp = k.spanOf(it, events)
	}
	base := sp.base
	var changed []string
	if k.Git != nil && base != "" && len(tr.Groups) > 0 {
		if changed, err = k.changed(sp); err != nil {
			return res, err
		}
	}
	var prior []gate.Result
	for _, e := range events {
		var r gate.Result
		if e.Kind == gate.KindRun && e.Decode(&r) == nil {
			prior = append(prior, r)
		}
	}
	run := gate.Run{
		Item: it, From: it.Status, To: to, Transition: tr, Workflow: w,
		Skills: k.Skills, Agents: k.Agents, Dispatcher: k.Dispatcher,
		Dir: k.Dir, Base: base, Changed: changed, Prior: prior,
		ReviewerDiffers: k.Settings.Rules.ReviewerDiffersFromPerformer,
		CheckTimeout:    k.CheckTimeout,
		Coverage:        func(paths []string) (string, error) { return k.coverage(it, events, sp, paths) },
		Diff:            func(paths []string) (string, error) { return k.diffSpan(sp, paths) },
	}
	// A judge reads the item's record; checks need none. The folder is
	// written after the changed paths are computed, and is gitignored.
	if k.judged(w, tr) {
		rec, err := k.record(it, events, true)
		if err != nil {
			return res, err
		}
		defer rec.Close()
		run.Record = rec.Section
	}
	results, pass := run.Execute(ctx)
	res.Gates = results
	attempt := item.NewID("att")
	for _, r := range results {
		r.Attempt = attempt
		if _, err := k.append(id, gate.KindRun, actor, r); err != nil {
			return res, err
		}
	}
	if !pass {
		return res, nil
	}
	if _, err := k.append(id, item.KindTransitioned, actor, item.Move{From: it.Status, To: to}); err != nil {
		return res, err
	}
	res.Moved = true
	res.Item, err = k.Get(id)
	return res, err
}

// base is the commit an item's work is judged from: the latest bound seat's
// base, else the commit at create.
func (k *Kernel) base(it item.Item, events []ledger.Event) string {
	seats := item.FoldSeats(events)
	for i := len(seats) - 1; i >= 0; i-- {
		if seats[i].Base != "" {
			return seats[i].Base
		}
	}
	return it.Base
}

// coverage hashes what a gate judges: the item's body, criteria and
// attachments, and the diff of the gate's paths over the span judged.
func (k *Kernel) coverage(it item.Item, events []ledger.Event, sp span, paths []string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", it.Body, strings.Join(it.Criteria, "\x1f"))
	for _, a := range item.FoldAttachments(events) {
		fmt.Fprintf(h, "%s=%s\x00", a.Name, a.SHA256)
	}
	d, err := k.diffSpan(sp, paths)
	if err != nil {
		return "", err
	}
	h.Write([]byte(d))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Attach stores data as a blob and records it on the item.
func (k *Kernel) Attach(id, name string, data []byte, mediaType, actor string) (item.Attachment, error) {
	if _, err := k.Get(id); err != nil {
		return item.Attachment{}, err
	}
	if strings.TrimSpace(name) == "" {
		return item.Attachment{}, errors.New("an attachment needs a name")
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	sha, err := k.Ledger.PutBlob(data)
	if err != nil {
		return item.Attachment{}, err
	}
	a := item.Attachment{Name: name, SHA256: sha, Size: int64(len(data)), MediaType: mediaType}
	e, err := k.append(id, item.KindAttachmentAdd, actor, a)
	a.Item, a.At, a.Actor = id, e.At, actor
	return a, err
}

// Seat binds session at location to the item, with HEAD as its base. A
// terminal item takes no seat.
func (k *Kernel) Seat(id, session, location, actor string) (item.Seat, error) {
	it, err := k.Get(id)
	if err != nil {
		return item.Seat{}, err
	}
	if session == "" {
		return item.Seat{}, errors.New("a seat needs a session")
	}
	known := false
	for _, p := range seatLocations {
		known = known || (strings.HasPrefix(location, p) && len(location) > len(p))
	}
	if !known {
		return item.Seat{}, fmt.Errorf("location %q is not one of %s<where>", location, strings.Join(seatLocations, "<where>, "))
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return item.Seat{}, err
	}
	if w.States[it.Status].Terminal {
		return item.Seat{}, fmt.Errorf("item %s is in terminal state %q", id, it.Status)
	}
	s := item.Seat{Session: session, Location: location}
	if k.Git != nil {
		s.Base, _ = k.Git.Head()
	}
	e, err := k.append(id, item.KindSeatBound, actor, s)
	s.Item, s.BoundAt = id, e.At
	return s, err
}

// Release unbinds session's seat on the item.
func (k *Kernel) Release(id, session, actor string) error {
	_, events, err := k.load(id)
	if err != nil {
		return err
	}
	for _, s := range item.FoldSeats(events) {
		if s.Session == session {
			_, err := k.append(id, item.KindSeatReleased, actor, item.SeatRelease{Session: session})
			return err
		}
	}
	return fmt.Errorf("session %q holds no seat on %s", session, id)
}

// PerformerRun is the performer.run payload.
type PerformerRun struct {
	State      string `json:"state"`
	Binding    string `json:"binding"`
	Skill      string `json:"skill"`
	Model      string `json:"model,omitempty"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"` // blob sha256 of the performer's answer
	Error      string `json:"error,omitempty"`
	// Location is where the step ran when it was not dispatched here:
	// remote:<node> or cloud:<url>.
	Location string `json:"location,omitempty"`
}

// Perform dispatches the current state's performer and records the run. A
// state performed by the session is refused: the session does that work. A
// binding placed remote:<id> runs on a node of that remote (k.Remote); one
// placed in the cloud starts a cloud session (k.Cloud) seated at cloud:<url>.
// An answer ending in an {"artifact":{name,type,body}} object also attaches
// that document to the item under the artifact's name; a malformed artifact
// fails the run and attaches nothing.
func (k *Kernel) Perform(ctx context.Context, id, actor string) (agent.Result, error) {
	it, events, err := k.load(id)
	if err != nil {
		return agent.Result{}, err
	}
	w, err := k.workflowOf(it)
	if err != nil {
		return agent.Result{}, err
	}
	st := w.States[it.Status]
	if st.Performer == nil {
		return agent.Result{}, fmt.Errorf("state %q has no performer", it.Status)
	}
	b := k.Agents[st.Performer.Agent]
	if agent.IsSession(b) {
		return agent.Result{}, fmt.Errorf("state %q is performed by the session (agent %q), not dispatched", it.Status, b.Name)
	}
	prompt := renderItem(it) + rejections(it, events)
	// The record: a remote node builds its own; a detached run cannot read
	// this machine, so it gets the inline part only.
	switch p := b.Placement; {
	case strings.HasPrefix(p, agent.PlaceRemote):
	case p == agent.PlaceCloud:
		rec, err := k.record(it, events, false)
		if err != nil {
			return agent.Result{}, err
		}
		prompt += rec.Section
	default:
		rec, err := k.record(it, events, true)
		if err != nil {
			return agent.Result{}, err
		}
		defer rec.Close()
		prompt += rec.Section
	}
	req := agent.Request{Binding: b, System: k.Skills[st.Performer.Skill].Body, Prompt: prompt, Dir: k.Dir}
	start := time.Now()
	var res agent.Result
	var runErr error
	var loc string
	switch p := b.Placement; {
	case strings.HasPrefix(p, agent.PlaceRemote):
		res, loc, runErr = k.performRemote(ctx, it, strings.TrimPrefix(p, agent.PlaceRemote), req, actor)
	case p == agent.PlaceCloud:
		res, loc, runErr = k.performCloud(ctx, it, req, actor)
		if errors.Is(runErr, ErrCloudByHand) {
			return res, runErr
		}
	default:
		res, runErr = k.Dispatcher.Dispatch(ctx, agent.Perform, req)
	}
	var art *agent.Artifact
	if runErr == nil {
		art, runErr = agent.ParseArtifact(res.Text)
	}
	rec := PerformerRun{State: it.Status, Binding: b.Name, Skill: st.Performer.Skill, Model: res.Model,
		TokensIn: res.TokensIn, TokensOut: res.TokensOut, DurationMS: time.Since(start).Milliseconds(), Location: loc}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	if res.Text != "" {
		if rec.Output, err = k.Ledger.PutBlob([]byte(res.Text)); err != nil {
			return res, err
		}
	}
	if _, err := k.append(id, KindPerformerRun, actor, rec); err != nil {
		return res, err
	}
	if art != nil && runErr == nil {
		if _, err := k.Attach(id, art.Name, []byte(art.Body), art.MediaType(), actor); err != nil {
			return res, err
		}
	}
	return res, runErr
}

// TaskRunRecord is the task.run payload: the task and the items it took.
type TaskRunRecord struct {
	Task   string   `json:"task"`
	Inputs []string `json:"inputs"`
}

// TaskRun runs a task once: it selects the task's inputs and creates an
// execution item (parent = the task) recording which items it took.
func (k *Kernel) TaskRun(taskID, actor string) (item.Item, []string, error) {
	task, err := k.Get(taskID)
	if err != nil {
		return item.Item{}, nil, err
	}
	if task.Inputs == nil {
		return item.Item{}, nil, fmt.Errorf("item %s names no input selection", taskID)
	}
	sel := *task.Inputs
	matches, err := k.List(cache.Query{Kind: sel.Kind, Status: sel.Status, Tag: sel.Tag, Category: sel.Category})
	if err != nil {
		return item.Item{}, nil, err
	}
	ids := []string{}
	for _, m := range matches {
		if m.ID != taskID && m.Parent != taskID {
			ids = append(ids, m.ID)
		}
	}
	kind, title := ExecutionKind, task.Title
	exec, err := k.Create(item.Fields{Kind: &kind, Title: &title, Parent: &taskID}, actor)
	if err != nil {
		return exec, nil, err
	}
	if _, err := k.append(exec.ID, KindTaskRun, actor, TaskRunRecord{Task: taskID, Inputs: ids}); err != nil {
		return exec, nil, err
	}
	return exec, ids, nil
}

// renderItem is the prompt a performer receives.
func renderItem(it item.Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Work item %s (%s), state %q.\nTitle: %s\n", it.ID, it.Kind, it.Status, it.Title)
	if it.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", it.Body)
	}
	if len(it.Criteria) > 0 {
		b.WriteString("\nAcceptance criteria:\n")
		for i, c := range it.Criteria {
			fmt.Fprintf(&b, "%d. %s\n", i+1, c)
		}
	}
	return b.String()
}

// judged reports whether any gate of tr is judged by an agent (not a check).
func (k *Kernel) judged(w *workflow.Workflow, tr *workflow.Transition) bool {
	for _, g := range tr.Groups {
		for _, name := range g.Gates {
			if !k.Skills[w.Gates[name].Skill].IsCheck() {
				return true
			}
		}
	}
	return false
}

// rejections lists the gates that rejected the newest attempt to leave the
// item's current state since it last entered it, with their notes bounded;
// "" when that attempt did not reject or there is none.
func rejections(it item.Item, events []ledger.Event) string {
	start := 0
	for i, e := range events {
		if e.Kind == item.KindTransitioned {
			start = i + 1
		}
	}
	var runs []gate.Result
	for _, e := range events[start:] {
		var r gate.Result
		if e.Kind == gate.KindRun && e.Decode(&r) == nil && r.Attempt != "" && gate.FromOf(r.Transition) == it.Status {
			runs = append(runs, r)
		}
	}
	if len(runs) == 0 {
		return ""
	}
	latest := runs[len(runs)-1].Attempt
	var b strings.Builder
	for _, r := range runs {
		if r.Attempt != latest || r.Result != gate.Reject {
			continue
		}
		if b.Len() == 0 {
			fmt.Fprintf(&b, "\nThe latest attempt to leave %q was rejected; address these findings:\n", it.Status)
		}
		notes, n := clip(r.Notes, RejectNotesMax)
		fmt.Fprintf(&b, "- gate %s (%s) on %s:\n%s\n", r.Gate, r.Binding, r.Transition, notes)
		if n < len(r.Notes) {
			fmt.Fprintf(&b, "[shown %d of %d bytes of notes]\n", n, len(r.Notes))
		}
	}
	return b.String()
}
