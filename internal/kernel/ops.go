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

const (
	KindPerformerRun = item.KindPerformerRun
	KindTaskRun      = "task.run"
)

const ExecutionKind = "execution"

var seatLocations = []string{LocationLocal, LocationRemote, LocationCloud}

func (k *Kernel) append(id, kind, actor string, payload any) (ledger.Event, error) {
	e, err := k.Ledger.Append(id, kind, actor, payload)
	if err != nil {
		return e, err
	}
	return e, k.Cache.SyncItem(k.Ledger, id)
}

func (k *Kernel) appendAfter(id, kind, actor, prev string, payload any) (ledger.Event, error) {
	e, err := k.Ledger.NewEvent(id, kind, actor, payload, prev)
	if err != nil {
		return e, err
	}
	if err := k.Ledger.AppendRaw(e); err != nil {
		return e, err
	}
	return e, k.Cache.SyncItem(k.Ledger, id)
}

func (k *Kernel) workflowOf(it item.Item) (*workflow.Workflow, error) {
	w, ok := k.Workflows[it.Workflow]
	if !ok {
		return nil, fmt.Errorf("item %s names workflow %q, which does not exist", it.ID, it.Workflow)
	}
	return w, nil
}

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
	if err == nil {
		k.overlayQueue(&it)
	}
	return it, events, err
}

func (k *Kernel) List(q cache.Query) ([]item.Item, error) {
	if err := k.Cache.Sync(k.Ledger); err != nil {
		return nil, err
	}
	items, err := k.Cache.Items(q)
	if err != nil {
		return nil, err
	}
	for i := range items {
		k.overlayQueue(&items[i])
	}
	return items, nil
}

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

func (k *Kernel) Create(f item.Fields, actor string) (item.Item, error) {
	return k.create(f, actor, "")
}

func (k *Kernel) create(f item.Fields, actor, id string) (item.Item, error) {
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
	if err := k.checkTrigger(draft.Kind, f.Trigger); err != nil {
		return draft, err
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
	prefix := k.Settings.Prefix(draft.Kind)
	if !ledger.ValidItem(prefix + "_x") {
		return draft, fmt.Errorf("id prefix %q for kind %q is not usable in an id", prefix, draft.Kind)
	}
	if id != "" {
		if n, _ := k.Ledger.Size(id); n > 0 {
			return draft, fmt.Errorf("item %s already exists", id)
		}
		if _, err := k.append(id, item.KindCreated, actor, f); err != nil {
			return draft, err
		}
		return k.Get(id)
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

var ErrKeepsWorkflow = errors.New("its workflow has moved or judged it")

func (k *Kernel) Reselect(id, want, actor string) (item.Item, error) {
	it, events, err := k.load(id)
	if err != nil {
		return it, err
	}
	if _, ok := k.Workflows[want]; !ok {
		return it, fmt.Errorf("workflow %s does not exist", want)
	}
	if it.Workflow == want {
		return it, fmt.Errorf("%s is already under workflow %s", id, want)
	}
	draft := it
	draft.Workflow = ""
	name, start, err := k.WorkflowFor(draft)
	if err != nil {
		return it, err
	}
	if name != want {
		return it, fmt.Errorf("the repo's rules select %s for %s, not %s", name, id, want)
	}
	for _, e := range events {
		switch e.Kind {
		case gate.KindRun:
			return it, fmt.Errorf("%w: %s has a gate run under workflow %s", ErrKeepsWorkflow, id, it.Workflow)
		case item.KindTransitioned:
			var m item.Move
			if err := e.Decode(&m); err != nil {
				return it, err
			}
			if m.From != "" {
				return it, fmt.Errorf("%w: %s was moved by workflow %s (%s -> %s)", ErrKeepsWorkflow, id, it.Workflow, m.From, m.To)
			}
		}
	}
	f, m := item.Placement(want, it.Status, start)
	e1, err := k.appendAfter(id, item.KindUpdated, actor, events[len(events)-1].Hash, f)
	if err != nil {
		return it, err
	}
	if _, err := k.appendAfter(id, item.KindTransitioned, actor, e1.Hash, m); err != nil {
		return it, err
	}
	return k.Get(id)
}

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
	if def := k.queueFiles[id]; def != nil && (f.Title != nil || f.Inputs != nil || f.Trigger != nil) {
		return it, fmt.Errorf("queue %s is defined by %s: edit its title, filter and trigger there", id, k.RelPath(def.File))
	}
	if f.Trigger != nil {
		if err := k.checkTrigger(it.Kind, f.Trigger); err != nil {
			return it, err
		}
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

type TransitionResult struct {
	Item   item.Item
	From   string
	To     string
	Moved  bool
	Gates  []gate.Result
	Queued []QueueOutcome `json:",omitempty"`
	Held   *item.Hold     `json:",omitempty"`
}

func (r TransitionResult) Rejects() []gate.Result {
	var out []gate.Result
	for _, g := range r.Gates {
		if g.Result == gate.Reject {
			out = append(out, g)
		}
	}
	return out
}

func (r TransitionResult) Errors() []gate.Result {
	var out []gate.Result
	for _, g := range r.Gates {
		if g.Result == gate.Error {
			out = append(out, g)
		}
	}
	return out
}

func (k *Kernel) Transition(ctx context.Context, id, to, actor string) (TransitionResult, error) {
	return k.transition(ctx, id, to, actor, nil)
}

func (k *Kernel) transition(ctx context.Context, id, to, actor string, hold *item.Hold) (TransitionResult, error) {
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
	if typ, ok := w.States[it.Status].Operated(); ok && !operatorRecorded(item.SinceEntered(events), typ) {
		return res, &OperatorRecordError{Item: id, Kind: it.Kind, State: it.Status, Type: typ}
	}
	var queues []string
	for _, name := range w.States[to].Enqueue {
		q, _ := k.ResolveQueue(name)
		if _, err := k.openQueue(q); err != nil {
			return res, fmt.Errorf("state %q of workflow %s enqueues into %s: %w", to, w.Name, name, err)
		}
		queues = append(queues, q)
	}
	run, sp, done, err := k.gateRun(it, events, w, tr, it.Status, to)
	if err != nil {
		return res, err
	}
	defer done()
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
		if hold != nil {
			return res, nil
		}
		return k.holdIfCapped(ctx, res, w, actor)
	}
	var frozen *item.Span
	if w.States[to].Terminal && k.Git != nil {
		if frozen, err = k.frozenSpan(it, w, sp, append(run.Prior, results...)); err != nil {
			return res, err
		}
	}
	if _, err := k.append(id, item.KindTransitioned, actor, item.Move{From: it.Status, To: to, Hold: hold, Change: frozen}); err != nil {
		return res, err
	}
	res.Moved = true
	if res.Item, err = k.Get(id); err != nil {
		return res, err
	}
	cause := item.StateCause(w.Name, it.Status, to)
	for _, q := range queues {
		out, err := k.Enqueue(q, []string{id}, cause, actor)
		if err != nil {
			return res, fmt.Errorf("%s moved to %s, but enqueueing it into %s failed (add it with satelle queue add %s %s): %w", id, to, q, q, id, err)
		}
		res.Queued = append(res.Queued, out)
	}
	return res, nil
}

func (k *Kernel) coverage(it item.Item, events []ledger.Event, sp span, paths []string) (coverage, change string, err error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", it.Body, strings.Join(it.Criteria, "\x1f"))
	for _, a := range item.FoldAttachments(events) {
		fmt.Fprintf(h, "%s=%s\x00", a.Name, a.SHA256)
	}
	d, err := k.diffSpan(sp, paths)
	if err != nil {
		return "", "", err
	}
	h.Write([]byte(d))
	return hex.EncodeToString(h.Sum(nil)), changeHash(d), nil
}

func changeHash(diff string) string {
	s := sha256.Sum256([]byte(diff))
	return hex.EncodeToString(s[:])
}

func (k *Kernel) Attach(id, name string, data []byte, mediaType, actor string) (item.Attachment, error) {
	return k.attach(id, item.Attachment{Name: name, MediaType: mediaType}, data, actor)
}

func (k *Kernel) attach(id string, a item.Attachment, data []byte, actor string) (item.Attachment, error) {
	if _, err := k.Get(id); err != nil {
		return item.Attachment{}, err
	}
	if strings.TrimSpace(a.Name) == "" {
		return item.Attachment{}, errors.New("an attachment needs a name")
	}
	if a.MediaType == "" {
		a.MediaType = "application/octet-stream"
	}
	sha, err := k.Ledger.PutBlob(data)
	if err != nil {
		return item.Attachment{}, err
	}
	a.Item, a.At, a.Actor = "", time.Time{}, ""
	a.SHA256, a.Size = sha, int64(len(data))
	e, err := k.append(id, item.KindAttachmentAdd, actor, a)
	a.Item, a.At, a.Actor = id, e.At, actor
	return a, err
}

func (k *Kernel) Seat(id, session, location, baseRef, actor string) (item.Seat, error) {
	s := item.Seat{Session: session, Location: location}
	if baseRef != "" {
		base, err := k.seatBase(baseRef)
		if err != nil {
			return item.Seat{}, err
		}
		s.Base, s.BaseRef = base, baseRef
	} else if k.Git != nil {
		s.Base, _ = k.Git.Head()
	}
	return k.bindSeat(id, s, actor)
}

func (k *Kernel) bindSeat(id string, s item.Seat, actor string) (item.Seat, error) {
	session, location := s.Session, s.Location
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
	s.Machine, s.Host = k.Machine, k.Host
	if k.User != nil {
		s.User = k.User()
	}
	e, err := k.append(id, item.KindSeatBound, actor, s)
	s.Item, s.BoundAt = id, e.At
	return s, err
}

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

type PerformerRun struct {
	State      string `json:"state"`
	Binding    string `json:"binding"`
	Skill      string `json:"skill"`
	Model      string `json:"model,omitempty"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	Location   string `json:"location,omitempty"`
}

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
	if _, ok := st.Operated(); ok {
		return agent.Result{}, fmt.Errorf("state %q is the operator's: nothing is dispatched", it.Status)
	}
	perf := st.Agent()
	if perf == nil {
		return agent.Result{}, fmt.Errorf("state %q has no performer", it.Status)
	}
	b := k.Agents[perf.Agent]
	if agent.IsSession(b) {
		return agent.Result{}, fmt.Errorf("state %q is performed by the session (agent %q), not dispatched", it.Status, b.Name)
	}
	prompt := renderItem(it) + rejections(it, events)
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
	req := agent.Request{Binding: b, System: k.Skills[perf.Skill].Body, Prompt: prompt, Dir: k.Dir}
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
		res, runErr = k.performLocal(ctx, it, events, req, actor)
	}
	var art *agent.Artifact
	if runErr == nil {
		art, runErr = agent.ParseArtifact(res.Text)
	}
	rec := PerformerRun{State: it.Status, Binding: b.Name, Skill: perf.Skill, Model: res.Model,
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

type TaskRunRecord struct {
	Task    string   `json:"task,omitempty"`
	Inputs  []string `json:"inputs"`
	BaseRef string   `json:"base_ref,omitempty"`
	Queue   string   `json:"queue,omitempty"`
}

func (k *Kernel) runInputs(run string) []string {
	ev, err := k.Cache.Events(run, KindTaskRun)
	if err != nil || len(ev) == 0 {
		return nil
	}
	var rec TaskRunRecord
	if ev[0].Decode(&rec) != nil {
		return nil
	}
	return rec.Inputs
}

func (k *Kernel) TaskRun(taskID, baseRef, actor string) (item.Item, TaskRunRecord, error) {
	rec := TaskRunRecord{Task: taskID}
	task, err := k.Get(taskID)
	if err != nil {
		return item.Item{}, rec, err
	}
	if task.Inputs == nil {
		return item.Item{}, rec, fmt.Errorf("item %s names no input selection", taskID)
	}
	matches, err := k.selectInputs(*task.Inputs)
	if err != nil {
		return item.Item{}, rec, err
	}
	rec.Inputs = []string{}
	for _, m := range matches {
		if m.ID != taskID && m.Parent != taskID {
			rec.Inputs = append(rec.Inputs, m.ID)
		}
	}
	kind, title := ExecutionKind, task.Title
	return k.startRun("", item.Fields{Kind: &kind, Title: &title, Parent: &taskID}, baseRef, rec, actor)
}

func (k *Kernel) selectInputs(sel item.Selection) ([]item.Item, error) {
	return k.List(cache.Query{Kind: sel.Kind, Status: sel.Status, Tag: sel.Tag, Category: sel.Category})
}

func (k *Kernel) startRun(id string, f item.Fields, baseRef string, rec TaskRunRecord, actor string) (item.Item, TaskRunRecord, error) {
	if baseRef != "" {
		commit, err := k.resolveBase(baseRef)
		if err != nil {
			return item.Item{}, rec, err
		}
		f.Base, rec.BaseRef = &commit, baseRef
	} else if g, ok := k.Git.(BranchGit); ok {
		rec.BaseRef, _ = g.Branch()
	}
	exec, err := k.create(f, actor, id)
	if err != nil {
		return exec, rec, err
	}
	if _, err := k.append(exec.ID, KindTaskRun, actor, rec); err != nil {
		return exec, rec, err
	}
	return exec, rec, nil
}

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

func rejections(it item.Item, events []ledger.Event) string {
	attempts := attemptsLeaving(it.Status, events)
	if len(attempts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range attempts[len(attempts)-1].Results {
		if r.Result != gate.Reject {
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
