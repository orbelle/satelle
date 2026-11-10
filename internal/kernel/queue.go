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

// A fire that cannot take the queue now. Both are refusals, not failures: the
// queue is unchanged and the members wait for the next fire.
var (
	// ErrRunOpen: the run the last fire started is not in a terminal state,
	// so its members are still being worked.
	ErrRunOpen = errors.New("the queue's last run is still open")
	// ErrQueueMoved: the queue's chain moved between reading its members and
	// claiming them (a concurrent enqueue or fire).
	ErrQueueMoved = errors.New("queue changed concurrently; re-run satelle queue fire")
)

// QueueOutcome is what an enqueue did: the items it added, the ones already
// queued, and the fire it caused, or why the fire was held.
type QueueOutcome struct {
	Queue   string       `json:"queue"`
	Name    string       `json:"name,omitempty"` // the queue file's name, when one defines it
	Added   []string     `json:"added"`
	Already []string     `json:"already,omitempty"`
	Fire    *FireOutcome `json:"fire,omitempty"`
	Held    string       `json:"held,omitempty"`
}

// FireOutcome is a fire: the run it started (none when the queue had no
// members), and what that run took.
type FireOutcome struct {
	Queue    string   `json:"queue"`
	Name     string   `json:"name,omitempty"` // the queue file's name, when one defines it
	Run      string   `json:"run,omitempty"`
	Status   string   `json:"status,omitempty"`
	Task     string   `json:"task,omitempty"`
	Workflow string   `json:"workflow,omitempty"`
	Inputs   []string `json:"inputs"`
	Base     string   `json:"base,omitempty"`
	BaseRef  string   `json:"base_ref,omitempty"`
	Cause    string   `json:"cause"`
}

// ResolveQueue maps a queue reference to its chain id: a queue file's name
// is its chain, anything else is taken as an id. def is the file defining the
// chain, or nil.
func (k *Kernel) ResolveQueue(ref string) (id string, def *workflow.Queue) {
	if q := k.Queues[ref]; q != nil {
		return q.ChainID(k.Settings.Prefix(item.QueueKind)), q
	}
	return ref, k.queueFiles[ref]
}

// QueueFile returns the file defining the queue chain id, or nil.
func (k *Kernel) QueueFile(id string) *workflow.Queue { return k.queueFiles[id] }

// overlayQueue sets a file-defined queue's definition on its folded item.
func (k *Kernel) overlayQueue(it *item.Item) {
	if it.Kind != item.QueueKind {
		return
	}
	if def := k.queueFiles[it.ID]; def != nil {
		def.Apply(it)
	}
}

// queueMirror is the payload of an event the kernel writes to mirror a queue
// file onto its chain: the file's definition, and the file as its cause. The
// cause is the kernel's only; a user update cannot carry one.
type queueMirror struct {
	item.Fields
	Cause string `json:"cause"`
}

// materializeQueues mirrors each queue file onto its chain, so a reader
// folding the chain alone (a synced replica, satelle.dev) sees the file's
// definition; the file stays its only owner, and local reads still overlay it.
// A name-derived chain (que_<name>) that does not exist yet is created once,
// its genesis carrying the queue's identity (kind, workflow) and the whole
// definition. The genesis is built from its inputs alone (queueFileEvent at
// queueGenesisAt, empty prev), so two runtimes that read the same file under
// the same tracked configuration before either has synced write the same
// event and never fork the chain at event 0 (sto_2b0b59cd). A file that adopts
// an id names a chain that already exists elsewhere: when this runtime does
// not hold it yet nothing is written, and it arrives with the next ledger pull
// (sto_5c706973). A queue chain that exists gets one item.updated with the
// whole definition when its raw fold's title, inputs or trigger differ from
// the file's, and nothing when they agree. That event is deterministic too, so
// two runtimes mirroring the same file onto the same head write the same
// event. Every write is a compare-and-append on the tail it read, by actor
// satelle with cause "queue file <path>": a process that wrote first wins, and
// the next open compares again.
func (k *Kernel) materializeQueues() error {
	for _, id := range sortedKeys(k.queueFiles) {
		def := k.queueFiles[id]
		events, err := k.Ledger.Read(id)
		if err != nil {
			return err
		}
		p := queueMirror{Fields: def.Fields(), Cause: "queue file " + k.RelPath(def.File)}
		if len(events) == 0 {
			if def.ID != "" {
				continue // adopted: the chain arrives with the next pull
			}
			qk := item.QueueKind
			name, start, werr := k.WorkflowFor(item.Item{Kind: qk, Title: def.Name})
			if werr != nil {
				return fmt.Errorf("%s: %w", k.RelPath(def.File), werr)
			}
			p.Kind, p.Workflow, p.Status = &qk, &name, &start
			err = k.appendFileEvent(id, item.KindCreated, "", queueGenesisAt, p)
		} else {
			it, ferr := item.Fold(events) // raw: never the overlay, which is the file
			if ferr != nil || it.Kind != item.QueueKind || !def.Differs(it) {
				continue
			}
			last := events[len(events)-1]
			err = k.appendFileEvent(id, item.KindUpdated, last.Hash, last.At, p)
		}
		if err != nil {
			var stale *ledger.StaleTail
			if errors.As(err, &stale) {
				continue
			}
			return fmt.Errorf("%s: mirroring queue %s: %w", k.RelPath(def.File), id, err)
		}
	}
	return nil
}

// queueGenesisAt is the time of every file-built queue genesis. It is fixed,
// not the wall clock, so every runtime builds the same genesis from the same
// file; it must never change, since changing it changes every new genesis's
// hash. It is not Go's zero time (read as unset by the web and mirror layers)
// nor the Unix epoch (an all-zero protobuf Timestamp a server may read back as
// absent).
var queueGenesisAt = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// queueFileEvent is an event that mirrors payload p onto id's chain after the
// event whose hash is prevHash ("" for the genesis), built from its inputs
// alone: its id is the first 24 hex digits of sha256(item, prevHash, the
// payload's JSON) and its time is at (the previous event's time for a mirror
// item.updated, queueGenesisAt for the genesis), so every runtime mirroring
// the same file onto the same head builds the same event, with the same hash.
// VerifyChain checks links and hashes, not time order, so reusing a time is
// valid.
func queueFileEvent(id, kind, prevHash string, at time.Time, p queueMirror) (ledger.Event, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return ledger.Event{}, err
	}
	sum := sha256.Sum256([]byte(id + "\n" + prevHash + "\n" + string(b)))
	return ledger.NewEventWith(hex.EncodeToString(sum[:])[:24], at, id, kind, "satelle", json.RawMessage(b), prevHash)
}

// appendFileEvent appends the deterministic queue-file event after prevHash,
// as appendAfter does: a compare-and-append on the tail.
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

// notHeldYet refuses a queue verb on a queue whose file adopts an id this
// runtime does not hold a chain for yet.
func (k *Kernel) notHeldYet(id string) error {
	def := k.queueFiles[id]
	if def == nil || def.ID == "" {
		return nil
	}
	events, err := k.Ledger.Read(id)
	if err != nil || len(events) > 0 {
		return err
	}
	return fmt.Errorf("queue %s adopts %s, which this runtime does not hold yet; it arrives with the next ledger pull (or remove id from %s to start a new queue)", def.Name, id, k.RelPath(def.File))
}

// RelPath is p relative to the working tree, when it is under it.
func (k *Kernel) RelPath(p string) string {
	if r, err := filepath.Rel(k.Dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// checkTrigger checks a trigger a create or update sets: only a queue carries
// one, its shape is valid, and its target exists (an open task, or a
// configured workflow).
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

// openQueue loads a queue that can take items: kind queue, not terminal.
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

// memberStatus resolves an explicit member's status and whether it is a
// terminal state of its workflow, for item.Members' lapse rule.
func (k *Kernel) memberStatus(id string) (string, bool, bool) {
	it, err := k.Get(id)
	if err != nil {
		return "", false, false
	}
	w := k.Workflows[it.Workflow]
	return it.Status, w != nil && w.IsTerminal(it.Status), true
}

// membership is a queue's members now, given its folded state.
func (k *Kernel) membership(q item.Item, st item.QueueState) (item.Membership, error) {
	var candidates []item.Item
	if q.Inputs != nil {
		var err error
		if candidates, err = k.selectInputs(*q.Inputs); err != nil {
			return item.Membership{}, err
		}
	}
	return item.Members(q, st, candidates, k.memberStatus), nil
}

// QueueMembers returns a queue's folded state and its membership now: its
// members and the explicit members that lapsed.
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

// Enqueue adds items to a queue explicitly, then fires the trigger in this
// call when its minimum size is reached. A state's enqueue (a cause written by
// item.StateCause) always records queue.added: the item was outside that state
// just before the move, so any earlier add it caused had lapsed. An add with
// an empty cause records queue.added unless the item is already a live
// explicit member. Any other cause (an external producer's) records it unless
// the item is a live explicit member or the chain already holds a queue.added
// for that item with that cause, before or after any fire, so a producer's
// retry never re-queues an item a run took; a skipped item is reported in
// Already. A non-state add that would record refuses an item already in a
// terminal state, which a queue does not hold. A fire the queue cannot take
// now (its last run is open, or the queue moved) is reported in Held, not as
// an error: the items stay queued.
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
	// The live explicit members: the explicit list less the lapsed, without
	// the filter (an item the filter selects is still added explicitly).
	live := item.Members(item.Item{ID: q.ID}, st, nil, k.memberStatus).Members
	for _, id := range ids {
		if id == queueID {
			return out, fmt.Errorf("a queue cannot hold itself")
		}
		it, err := k.Get(id)
		if err != nil {
			return out, err
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
	_, _, m, err := k.QueueMembers(queueID)
	if err != nil {
		return out, err
	}
	if len(m.Members) < t.MinSize {
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

// Dequeue removes explicit members, recording queue.removed for each. An item
// the queue's filter selects cannot be removed: change the filter or the item.
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

// Fire runs a queue's trigger over its members now: it claims them by
// recording queue.fired (a compare-and-append on the chain it read), then
// starts the run. A task target creates an execution under the task; a
// workflow target creates an execution under the queue in that workflow's
// start state. Either way the run's task.run lists the members it took. The
// base is baseRef, else the trigger's base, else HEAD. A queue with no members
// fires nothing (Run is ""). Satelle fires only here, in the calling process:
// nothing watches or schedules a queue.
func (k *Kernel) Fire(ref, baseRef, cause, actor string) (FireOutcome, error) {
	queueID, def := k.ResolveQueue(ref)
	out := FireOutcome{Queue: queueID, Cause: cause, Inputs: []string{}}
	if def != nil {
		out.Name = def.Name
	}
	if err := k.notHeldYet(queueID); err != nil {
		return out, err
	}
	q, events, err := k.load(queueID)
	if err != nil {
		return out, err
	}
	if _, err := k.openQueue(queueID); err != nil {
		return out, err
	}
	if q.Trigger == nil {
		return out, fmt.Errorf("queue %s has no trigger", queueID)
	}
	t := *q.Trigger
	out.Task, out.Workflow = t.Task, t.Workflow
	st := item.FoldQueue(events)
	if lf := st.LastFire; lf != nil && lf.Run != "" {
		if run, err := k.Get(lf.Run); err == nil {
			if w := k.Workflows[run.Workflow]; w == nil || !w.IsTerminal(run.Status) {
				return out, fmt.Errorf("%w: %s is %s", ErrRunOpen, run.ID, run.Status)
			}
		}
	}
	m, err := k.membership(q, st)
	if err != nil {
		return out, err
	}
	members := m.Members
	if len(members) == 0 {
		return out, nil
	}
	out.Inputs = members
	if baseRef == "" {
		baseRef = t.Base
	}
	if baseRef != "" { // a bad ref refuses before anything is claimed
		if _, err := k.resolveBase(baseRef); err != nil {
			return out, err
		}
	}
	runID := item.NewID(k.Settings.Prefix(ExecutionKind))
	prev := events[len(events)-1].Hash
	claim := item.QueueFire{Run: runID, Task: t.Task, Workflow: t.Workflow, Cause: cause}
	if _, err := k.appendAfter(queueID, item.KindQueueFired, actor, prev, claim); err != nil {
		var stale *ledger.StaleTail
		if errors.As(err, &stale) {
			return out, ErrQueueMoved
		}
		return out, err
	}
	kind := ExecutionKind
	f := item.Fields{Kind: &kind}
	rec := TaskRunRecord{Task: t.Task, Inputs: members, Queue: queueID}
	if t.Task != "" {
		task, err := k.Get(t.Task)
		if err != nil {
			return out, err
		}
		f.Title, f.Parent = &task.Title, &t.Task
	} else {
		wf := t.Workflow
		f.Title, f.Parent, f.Workflow = &q.Title, &q.ID, &wf
	}
	run, rec, err := k.startRun(runID, f, baseRef, rec, actor)
	if err != nil {
		return out, fmt.Errorf("queue %s claimed its members for %s, but the run was not created: %w", queueID, runID, err)
	}
	out.Run, out.Status, out.Base, out.BaseRef = run.ID, run.Status, run.Base, rec.BaseRef
	return out, nil
}
