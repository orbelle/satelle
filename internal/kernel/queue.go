package kernel

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

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

// materializeQueues creates the chain of each queue file that has none yet,
// once: a compare-and-create on the empty chain, so a concurrent process that
// created it first wins and nothing is written twice. The genesis carries the
// queue's identity (kind, its name as title, workflow); the file stays the
// only owner of its definition. A chain that exists (an adopted id) is never
// written to here.
func (k *Kernel) materializeQueues() error {
	for _, id := range sortedKeys(k.queueFiles) {
		if n, err := k.Ledger.Size(id); err != nil || n > 0 {
			if err != nil {
				return err
			}
			continue
		}
		def := k.queueFiles[id]
		kind, title := item.QueueKind, def.Name
		name, start, err := k.WorkflowFor(item.Item{Kind: kind, Title: title})
		if err != nil {
			return fmt.Errorf("%s: %w", k.RelPath(def.File), err)
		}
		f := item.Fields{Kind: &kind, Title: &title, Workflow: &name, Status: &start}
		if _, err := k.appendAfter(id, item.KindCreated, "satelle", "", f); err != nil {
			var stale *ledger.StaleTail
			if errors.As(err, &stale) {
				continue
			}
			return fmt.Errorf("%s: creating queue %s: %w", k.RelPath(def.File), id, err)
		}
	}
	return nil
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

// QueueMembers returns a queue's folded state and its members now.
func (k *Kernel) QueueMembers(ref string) (item.Item, item.QueueState, []string, error) {
	id, _ := k.ResolveQueue(ref)
	q, events, err := k.load(id)
	if err != nil {
		return q, item.QueueState{}, nil, err
	}
	if q.Kind != item.QueueKind {
		return q, item.QueueState{}, nil, fmt.Errorf("%s is a %s, not a %s", id, q.Kind, item.QueueKind)
	}
	st := item.FoldQueue(events)
	var candidates []item.Item
	if q.Inputs != nil {
		if candidates, err = k.selectInputs(*q.Inputs); err != nil {
			return q, st, nil, err
		}
	}
	return q, st, item.Members(q, st, candidates), nil
}

// Enqueue adds items to a queue explicitly, recording queue.added for each one
// not already there, then fires the trigger in this call when its minimum size
// is reached. A fire the queue cannot take now (its last run is open, or the
// queue moved) is reported in Held, not as an error: the items stay queued.
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
	for _, id := range ids {
		if id == queueID {
			return out, fmt.Errorf("a queue cannot hold itself")
		}
		if _, err := k.Get(id); err != nil {
			return out, err
		}
		if slices.Contains(st.Explicit, id) || slices.Contains(out.Added, id) {
			out.Already = append(out.Already, id)
			continue
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
	_, _, members, err := k.QueueMembers(queueID)
	if err != nil {
		return out, err
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

// Dequeue removes explicit members, recording queue.removed for each. An item
// the queue's filter selects cannot be removed: change the filter or the item.
func (k *Kernel) Dequeue(ref string, ids []string, actor string) ([]string, error) {
	queueID, _ := k.ResolveQueue(ref)
	q, st, members, err := k.QueueMembers(queueID)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, id := range ids {
		if !slices.Contains(st.Explicit, id) {
			if slices.Contains(members, id) {
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
	var candidates []item.Item
	if q.Inputs != nil {
		if candidates, err = k.selectInputs(*q.Inputs); err != nil {
			return out, err
		}
	}
	members := item.Members(q, st, candidates)
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
