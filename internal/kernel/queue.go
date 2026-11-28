package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

var (
	ErrRunOpen    = errors.New("the queue's last run is still open")
	ErrQueueMoved = errors.New("queue changed concurrently; re-run satelle queue fire")
)

type QueueOutcome struct {
	Queue   string       `json:"queue"`
	Name    string       `json:"name,omitempty"`
	Added   []string     `json:"added"`
	Already []string     `json:"already,omitempty"`
	Outside []string     `json:"outside,omitempty"`
	Repo    string       `json:"repo,omitempty"`
	Fire    *FireOutcome `json:"fire,omitempty"`
	Held    string       `json:"held,omitempty"`
}

type FireOutcome struct {
	Queue    string   `json:"queue"`
	Name     string   `json:"name,omitempty"`
	Run      string   `json:"run,omitempty"`
	Status   string   `json:"status,omitempty"`
	Task     string   `json:"task,omitempty"`
	Workflow string   `json:"workflow,omitempty"`
	Inputs   []string `json:"inputs"`
	Base     string   `json:"base,omitempty"`
	BaseRef  string   `json:"base_ref,omitempty"`
	Cause    string   `json:"cause"`
}

func (k *Kernel) ResolveQueue(ref string) (id string, def *workflow.Queue) {
	if q := k.Queues[ref]; q != nil {
		return q.ChainID(k.Settings.Prefix(item.QueueKind)), q
	}
	return ref, k.queueFiles[ref]
}

func (k *Kernel) QueueFile(id string) *workflow.Queue { return k.queueFiles[id] }

func (k *Kernel) overlayQueue(it *item.Item) {
	if it.Kind != item.QueueKind {
		return
	}
	if def := k.queueFiles[it.ID]; def != nil {
		def.Apply(it)
	}
}

type queueMirror struct {
	item.Fields
	Cause string `json:"cause"`
}

func (k *Kernel) materializeQueues() error {
	for _, id := range sortedKeys(k.queueFiles) {
		if _, err := k.materializeQueue(id); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) materializeQueue(id string) (wrote bool, err error) {
	def := k.queueFiles[id]
	if def == nil || k.linkedTree {
		return false, nil
	}
	events, err := k.Ledger.Read(id)
	if err != nil {
		return false, err
	}
	p := queueMirror{Fields: def.Fields(), Cause: queueMirrorCause + k.RelPath(def.File)}
	if len(events) == 0 {
		if def.ID != "" {
			return false, nil
		}
		qk := item.QueueKind
		name, start, werr := k.WorkflowFor(item.Item{Kind: qk, Title: def.Name})
		if werr != nil {
			return false, fmt.Errorf("%s: %w", k.RelPath(def.File), werr)
		}
		p.Kind, p.Workflow, p.Status = &qk, &name, &start
		err = k.appendFileEvent(id, item.KindCreated, "", queueGenesisAt, p)
	} else {
		it, ferr := item.Fold(events)
		if ferr != nil || it.Kind != item.QueueKind || !def.Differs(it) {
			return false, nil
		}
		last := events[len(events)-1]
		err = k.appendFileEvent(id, item.KindUpdated, last.Hash, last.At, p)
	}
	if err != nil {
		var stale *ledger.StaleTail
		if errors.As(err, &stale) {
			return false, nil
		}
		return false, fmt.Errorf("%s: mirroring queue %s: %w", k.RelPath(def.File), id, err)
	}
	return true, nil
}

const queueMirrorCause = "queue file "

func (k *Kernel) MirrorTail(id string, events []ledger.Event, from int) bool {
	def := k.queueFiles[id]
	if def == nil || from < 0 || from >= len(events) {
		return false
	}
	for i := from; i < len(events); i++ {
		e := events[i]
		want := item.KindUpdated
		if i == 0 {
			if def.ID != "" {
				return false
			}
			want = item.KindCreated
		}
		if e.Kind != want || e.Actor != "satelle" {
			return false
		}
		var p struct {
			Cause string `json:"cause"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || !strings.HasPrefix(p.Cause, queueMirrorCause) {
			return false
		}
	}
	return true
}

func (k *Kernel) Remirror(id string) (wrote bool, err error) { return k.materializeQueue(id) }

var queueGenesisAt = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

func queueFileEvent(id, kind, prevHash string, at time.Time, p queueMirror) (ledger.Event, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return ledger.Event{}, err
	}
	sum := sha256.Sum256([]byte(id + "\n" + prevHash + "\n" + string(b)))
	return ledger.NewEventWith(hex.EncodeToString(sum[:])[:24], at, id, kind, "satelle", json.RawMessage(b), prevHash)
}

func (k *Kernel) appendFileEvent(id, kind, prevHash string, at time.Time, p queueMirror) error {
	e, err := queueFileEvent(id, kind, prevHash, at, p)
	if err != nil {
		return err
	}
	if err := k.Ledger.AppendRaw(e); err != nil {
		return err
	}
	return k.Cache.SyncItem(k.Ledger, id)
}

func (k *Kernel) notHeldYet(id string) error {
	def := k.queueFiles[id]
	if def == nil || (def.ID == "" && !k.linkedTree) {
		return nil
	}
	events, err := k.Ledger.Read(id)
	if err != nil || len(events) > 0 {
		return err
	}
	if def.ID == "" {
		return fmt.Errorf("queue %s has no chain yet: %s is mirrored onto the ledger only by a command in the repository's main checkout, not in a linked worktree", def.Name, k.RelPath(def.File))
	}
	return fmt.Errorf("queue %s adopts %s, which this runtime does not hold yet; it arrives with the next ledger pull (or remove id from %s to start a new queue)", def.Name, id, k.RelPath(def.File))
}

func (k *Kernel) RelPath(p string) string {
	if r, err := filepath.Rel(k.Dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

func (k *Kernel) checkTrigger(kind string, t *item.Trigger) error {
	if t == nil {
		return nil
	}
	if kind != item.QueueKind {
		return fmt.Errorf("only a %s item carries a trigger, not a %s", item.QueueKind, kind)
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if t.Task != "" {
		task, err := k.Get(t.Task)
		if err != nil {
			return fmt.Errorf("trigger task: %w", err)
		}
		if task.Kind != "task" {
			return fmt.Errorf("trigger task %s is a %s, not a task", t.Task, task.Kind)
		}
		if w := k.Workflows[task.Workflow]; w != nil && w.IsTerminal(task.Status) {
			return fmt.Errorf("trigger task %s is %s, a terminal state", t.Task, task.Status)
		}
	}
	if t.Workflow != "" {
		if _, ok := k.Workflows[t.Workflow]; !ok {
			return fmt.Errorf("trigger workflow %q does not exist", t.Workflow)
		}
	}
	return nil
}

func (k *Kernel) openQueue(id string) (item.Item, error) {
	if err := k.notHeldYet(id); err != nil {
		return item.Item{}, err
	}
	q, err := k.Get(id)
	if err != nil {
		return q, err
	}
	if q.Kind != item.QueueKind {
		return q, fmt.Errorf("%s is a %s, not a %s", id, q.Kind, item.QueueKind)
	}
	if w := k.Workflows[q.Workflow]; w != nil && w.IsTerminal(q.Status) {
		return q, fmt.Errorf("queue %s is %s, a terminal state", id, q.Status)
	}
	return q, nil
}

func (k *Kernel) memberStatus(id string) (string, bool, bool) {
	it, err := k.Get(id)
	if err != nil {
		return "", false, false
	}
	w := k.Workflows[it.Workflow]
	return it.Status, w != nil && w.IsTerminal(it.Status), true
}

func (k *Kernel) memberTarget(id string) (string, bool) {
	it, err := k.Get(id)
	if err != nil {
		return "", false
	}
	return it.Target(), true
}

func (k *Kernel) membership(q item.Item, st item.QueueState) (item.Membership, error) {
	var candidates []item.Item
	if q.Inputs != nil {
		var err error
		if candidates, err = k.selectInputs(*q.Inputs); err != nil {
			return item.Membership{}, err
		}
	}
	return item.Members(q, st, candidates, k.memberStatus, k.memberTarget), nil
}

func (k *Kernel) QueueMembers(ref string) (item.Item, item.QueueState, item.Membership, error) {
	id, _ := k.ResolveQueue(ref)
	if err := k.notHeldYet(id); err != nil {
		return item.Item{}, item.QueueState{}, item.Membership{}, err
	}
	q, events, err := k.load(id)
	if err != nil {
		return q, item.QueueState{}, item.Membership{}, err
	}
	if q.Kind != item.QueueKind {
		return q, item.QueueState{}, item.Membership{}, fmt.Errorf("%s is a %s, not a %s", id, q.Kind, item.QueueKind)
	}
	st := item.FoldQueue(events)
	m, err := k.membership(q, st)
	return q, st, m, err
}

func (k *Kernel) Enqueue(ref string, ids []string, cause, actor string) (QueueOutcome, error) {
	queueID, def := k.ResolveQueue(ref)
	out := QueueOutcome{Queue: queueID, Added: []string{}}
	if def != nil {
		out.Name = def.Name
	}
	q, err := k.openQueue(queueID)
	if err != nil {
		return out, err
	}
	events, err := k.Ledger.Read(queueID)
	if err != nil {
		return out, err
	}
	st := item.FoldQueue(events)
	_, stateCause := item.EnqueuedState(cause)
	live := item.Members(item.Item{ID: q.ID}, st, nil, k.memberStatus, nil).Members
	if q.Inputs != nil {
		out.Repo = q.Inputs.Repo
	}
	for _, id := range ids {
		if id == queueID {
			return out, fmt.Errorf("a queue cannot hold itself")
		}
		it, err := k.Get(id)
		if err != nil {
			return out, err
		}
		if out.Repo != "" && it.Target() != out.Repo {
			out.Outside = append(out.Outside, id)
			continue
		}
		if slices.Contains(out.Added, id) || (!stateCause && slices.Contains(live, id)) ||
			(cause != "" && !stateCause && st.AddedWith(id, cause)) {
			out.Already = append(out.Already, id)
			continue
		}
		if w := k.Workflows[it.Workflow]; !stateCause && w != nil && w.IsTerminal(it.Status) {
			return out, fmt.Errorf("%s is %s, a terminal state; a queue does not hold it", id, it.Status)
		}
		if _, err := k.append(queueID, item.KindQueueAdded, actor, item.QueueEntry{Item: id, Cause: cause}); err != nil {
			return out, err
		}
		out.Added = append(out.Added, id)
	}
	t := q.Trigger
	if t == nil || t.MinSize == 0 || len(out.Added) == 0 {
		return out, nil
	}
	var members []string
	if t.Overlaps() {
		r, err := k.fireReady(queueID, "")
		if errors.Is(err, ErrQueueMoved) {
			out.Held = err.Error()
			return out, nil
		}
		if err != nil {
			return out, err
		}
		members = r.members
	} else {
		_, _, m, err := k.QueueMembers(queueID)
		if err != nil {
			return out, err
		}
		members = m.Members
	}
	if len(members) < t.MinSize {
		return out, nil
	}
	fire, err := k.Fire(queueID, "", "enqueue", actor)
	switch {
	case errors.Is(err, ErrRunOpen), errors.Is(err, ErrQueueMoved):
		out.Held = err.Error()
	case err != nil:
		return out, err
	case fire.Run != "":
		out.Fire = &fire
	}
	return out, nil
}

func (k *Kernel) Dequeue(ref string, ids []string, actor string) ([]string, error) {
	queueID, _ := k.ResolveQueue(ref)
	q, st, m, err := k.QueueMembers(queueID)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, id := range ids {
		if !slices.Contains(st.Explicit, id) {
			if slices.Contains(m.Members, id) {
				return removed, fmt.Errorf("%s matches the queue's filter; it is not an explicit member of %s", id, q.ID)
			}
			return removed, fmt.Errorf("%s is not in queue %s", id, q.ID)
		}
		if _, err := k.append(queueID, item.KindQueueRemoved, actor, item.QueueEntry{Item: id}); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}
	return removed, nil
}

type fireReadiness struct {
	queue     item.Item
	events    []ledger.Event
	trigger   *item.Trigger
	members   []string
	kept      []string
	baseRef   string
	unsettled *item.Fired
}

func (k *Kernel) runOpen(run item.Item) bool {
	w := k.Workflows[run.Workflow]
	return w == nil || !w.IsTerminal(run.Status)
}

func (k *Kernel) lastOpenRun(st item.QueueState) (item.Item, bool) {
	lf := st.LastFire
	if lf == nil || lf.Run == "" {
		return item.Item{}, false
	}
	run, err := k.Get(lf.Run)
	if err != nil || !k.runOpen(run) {
		return item.Item{}, false
	}
	return run, true
}

type heldRun struct {
	run    item.Item
	inputs []string
}

func (k *Kernel) holdings(st item.QueueState) ([]heldRun, *item.Fired, error) {
	var held []heldRun
	for i, f := range st.Fires {
		if f.Run == "" {
			continue
		}
		run, rec, settled, err := k.taskRun(f.Run)
		if err != nil {
			return nil, nil, err
		}
		if !settled {
			if i == len(st.Fires)-1 {
				return held, &st.Fires[i], nil
			}
			continue
		}
		if k.runOpen(run) {
			held = append(held, heldRun{run: run, inputs: rec.Inputs})
		}
	}
	return held, nil, nil
}

func unsettledFire(queueID string, f *item.Fired) error {
	return fmt.Errorf("%w: queue %s's fire of %s has no task.run yet", ErrQueueMoved, queueID, f.Run)
}

func untaken(st item.QueueState, members []string, open []heldRun) (take, kept []string) {
	held := func(id string) bool {
		return slices.ContainsFunc(open, func(h heldRun) bool { return slices.Contains(h.inputs, id) })
	}
	take = []string{}
	for _, id := range members {
		if !held(id) {
			take = append(take, id)
		}
	}
	for _, id := range st.Explicit {
		if held(id) && !slices.Contains(kept, id) {
			kept = append(kept, id)
		}
	}
	return take, kept
}

func (k *Kernel) fireReady(queueID, baseRef string) (fireReadiness, error) {
	var r fireReadiness
	if err := k.notHeldYet(queueID); err != nil {
		return r, err
	}
	q, events, err := k.load(queueID)
	if err != nil {
		return r, err
	}
	r.queue, r.events = q, events
	if _, err := k.openQueue(queueID); err != nil {
		return r, err
	}
	if q.Trigger == nil {
		return r, fmt.Errorf("queue %s has no trigger", queueID)
	}
	t := *q.Trigger
	r.trigger = &t
	st := item.FoldQueue(events)
	if !t.Overlaps() {
		if run, open := k.lastOpenRun(st); open {
			return r, fmt.Errorf("%w: %s is %s", ErrRunOpen, run.ID, run.Status)
		}
	}
	m, err := k.membership(q, st)
	if err != nil {
		return r, err
	}
	r.members = m.Members
	if t.Overlaps() {
		held, unsettled, err := k.holdings(st)
		if err != nil {
			return r, err
		}
		r.members, r.kept = untaken(st, m.Members, held)
		if unsettled != nil {
			r.unsettled = unsettled
			return r, unsettledFire(queueID, unsettled)
		}
	}
	if len(r.members) == 0 {
		return r, nil
	}
	if baseRef == "" {
		baseRef = t.Base
	}
	r.baseRef = baseRef
	if baseRef != "" {
		if _, err := k.resolveBase(k.self(), baseRef); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (k *Kernel) Fire(ref, baseRef, cause, actor string) (FireOutcome, error) {
	queueID, def := k.ResolveQueue(ref)
	out := FireOutcome{Queue: queueID, Cause: cause, Inputs: []string{}}
	if def != nil {
		out.Name = def.Name
	}
	r, err := k.fireReady(queueID, baseRef)
	if r.trigger != nil {
		out.Task, out.Workflow = r.trigger.Task, r.trigger.Workflow
	}
	if len(r.members) > 0 {
		out.Inputs = r.members
	}
	if err != nil {
		if r.unsettled != nil {
			out.Inputs = []string{}
			if _, _, cerr := k.startFireRun(r.queue, *r.trigger, r.unsettled.Run, r.members, r.trigger.Base, actor); cerr != nil {
				return out, fmt.Errorf("queue %s claimed its members for %s, but the run was not created: %w", queueID, r.unsettled.Run, cerr)
			}
		}
		return out, err
	}
	if len(r.members) == 0 {
		return out, nil
	}
	q, events, t, members, baseRef := r.queue, r.events, *r.trigger, r.members, r.baseRef
	if err := k.checkFireTargets(t, members); err != nil {
		return out, err
	}
	runID := item.NewID(k.Settings.Prefix(ExecutionKind))
	prev := events[len(events)-1].Hash
	claim := item.QueueFire{Run: runID, Task: t.Task, Workflow: t.Workflow, Cause: cause, Kept: r.kept}
	if _, err := k.appendAfter(queueID, item.KindQueueFired, actor, prev, claim); err != nil {
		var stale *ledger.StaleTail
		if errors.As(err, &stale) {
			return out, ErrQueueMoved
		}
		return out, err
	}
	run, rec, err := k.startFireRun(q, t, runID, members, baseRef, actor)
	if err != nil {
		return out, fmt.Errorf("queue %s claimed its members for %s, but the run was not created: %w", queueID, runID, err)
	}
	out.Inputs = rec.Inputs
	out.Run, out.Status, out.Base, out.BaseRef = run.ID, run.Status, run.Base, rec.BaseRef
	return out, nil
}

func (k *Kernel) checkFireTargets(t item.Trigger, members []string) error {
	if err := k.CheckTargets(members...); err != nil {
		return err
	}
	if t.Task != "" {
		return k.CheckTargets(t.Task)
	}
	return nil
}

func (k *Kernel) startFireRun(q item.Item, t item.Trigger, runID string, members []string, baseRef, actor string) (item.Item, TaskRunRecord, error) {
	kind := ExecutionKind
	f := item.Fields{Kind: &kind}
	rec := TaskRunRecord{Task: t.Task, Inputs: members, Queue: q.ID}
	if err := k.checkFireTargets(t, members); err != nil {
		return item.Item{}, rec, err
	}
	if t.Task != "" {
		task, err := k.Get(t.Task)
		if err != nil {
			return item.Item{}, rec, err
		}
		f.Title, f.Parent = &task.Title, &t.Task
	} else {
		wf := t.Workflow
		f.Title, f.Parent, f.Workflow = &q.Title, &q.ID, &wf
	}
	return k.startRun(runID, f, baseRef, rec, actor)
}
