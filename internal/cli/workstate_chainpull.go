package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A story's events do not ride the workstate page: they leave a push by Chain/Append
// and are read back by Chain/Read. A pull therefore brings each story it applies
// under its hosted chain before the row is folded, so the story is never seeded
// with a genesis of its own — one the next push would be refused for, because it
// forks the hosted chain from an empty base (sty_9b1dcf4b).

// conflictChainForked is why a pull left a story's hosted chain out: this
// machine's own events fork it.
const conflictChainForked = "pull left the hosted chain out: local events fork it"

// chainReader is the Chain/Read a pull needs; *hosted.ChainConn is the real one.
type chainReader interface {
	ReadStory(ctx context.Context, project, storyID, after string) ([]hosted.ChainEvent, string, error)
}

// chainPull is one pass's reading of hosted chains. The Chain connection is opened
// on first use and shared by every page; the heads it learns are written with it
// closes. A nil *chainPull reads nothing.
type chainPull struct {
	client                *hosted.Client
	server, project, root string
	errw                  io.Writer

	conn        chainReader
	closer      io.Closer
	unsupported bool
	reads       int
	heads       map[string]string // story -> last event this checkout holds that the server has
	pending     map[string]string
	taken       map[string]bool // chains this pass has read

	// held, set in hosted mode, names the stories whose hosted chain a pass reads;
	// every other story arrives as its row alone (sty_0dc600e1). Nil reads all.
	held func(it workitem.Item) bool
	// authored, set in hosted mode, names the stories whose row this machine wrote
	// and the server has not yet accepted: only those keep a newer local row.
	authored map[string]bool
}

// keepsLocal reports whether a newer local row of it may stand against the hosted
// one. In hosted mode only a row this machine authored does.
func (c *chainPull) keepsLocal(it workitem.Item) bool {
	return c == nil || c.authored == nil || it.Kind != workitem.KindStory || c.authored[it.ID]
}

// remote reports a story whose history the pass leaves on the server.
func (c *chainPull) remote(it workitem.Item) bool {
	return c != nil && c.held != nil && it.Kind == workitem.KindStory && !c.held(it)
}

func newChainPull(client *hosted.Client, server, project, root string, errw io.Writer) *chainPull {
	return &chainPull{client: client, server: server, project: project, root: root, errw: errw, pending: map[string]string{}}
}

// reader returns the shared connection, opening it first when needed.
func (c *chainPull) reader(ctx context.Context) (chainReader, error) {
	if c.conn != nil {
		return c.conn, nil
	}
	conn, err := c.client.OpenChain(ctx)
	if err != nil {
		return nil, fmt.Errorf("open the story chain: %w", err)
	}
	c.conn, c.closer = conn, conn
	return conn, nil
}

// head is the last event of the story this checkout holds that the server has.
func (c *chainPull) head(id string) (string, error) {
	if c.heads == nil {
		cur, err := hosted.LoadWorkstateCursor(c.server, c.project, c.root)
		if err != nil {
			return "", fmt.Errorf("load story heads: %w", err)
		}
		c.heads = cur.StoryHeads
	}
	return c.heads[id], nil
}

// close writes the heads the pass learned and releases the connection.
func (c *chainPull) close() {
	if c == nil {
		return
	}
	if c.closer != nil {
		_ = c.closer.Close()
		c.conn, c.closer = nil, nil
	}
	c.flush()
}

// flush writes the pending heads. A head that is lost costs a re-read of the
// story's chain and nothing else: the events already held are skipped.
func (c *chainPull) flush() {
	if len(c.pending) == 0 {
		return
	}
	if err := hosted.SaveStoryHeads(c.server, c.project, c.root, c.pending); err != nil {
		fmt.Fprintf(c.errw, "workstate: %d story head(s) were not saved: %v\n", len(c.pending), err)
		return
	}
	for id, head := range c.pending {
		if c.heads != nil {
			c.heads[id] = head
		}
	}
	c.pending = map[string]string{}
}

// ledgerEntryOf decodes a hosted event with the codec a page row uses: its payload
// is the entry as marshalWorkstateLedger wrote it, and the event's own base and
// workflow version fill what the payload leaves out.
func ledgerEntryOf(storyID string, ev hosted.ChainEvent) (ledger.Entry, error) {
	e, err := parseWorkstateLedger(hosted.WorkstateLedgerRow{ID: ev.ID, StoryID: storyID, Record: ev.Payload})
	if err != nil {
		return ledger.Entry{}, err
	}
	e.ID, e.StoryID, e.BaseID = ev.ID, storyID, ev.BaseID
	if e.WorkflowHash == "" {
		e.WorkflowHash = ev.WorkflowVersion
	}
	return e, nil
}

// readHosted reads the story's hosted events after the head this checkout holds
// when the local chain has it, and from the start otherwise or when the server does
// not know the head.
func (c *chainPull) readHosted(ctx context.Context, rd chainReader, id string, local []ledger.Entry) (events []hosted.ChainEvent, latest, after string, err error) {
	head, err := c.head(id)
	if err != nil {
		return nil, "", "", err
	}
	if head != "" && slices.IndexFunc(local, func(e ledger.Entry) bool { return e.ID == head }) >= 0 {
		after = head
	}
	if c.reads > 0 && !pause(ctx, appendPace) {
		return nil, "", "", ctx.Err()
	}
	c.reads++
	events, latest, err = rd.ReadStory(ctx, c.project, id, after)
	if err != nil && after != "" && !errors.Is(err, hosted.ErrChainUnsupported) && !errors.Is(err, hosted.ErrLoginRequired) {
		after = ""
		events, latest, err = rd.ReadStory(ctx, c.project, id, "")
	}
	return events, latest, after, err
}

// materialize brings every story the page applied under its hosted chain, in chain
// order. A story the server holds no events of keeps the chain it has here; the
// pull seeds nothing (sty_199ad82b).
//
// Nothing merges. A hosted chain that this machine's own events fork is left out
// and, when the fork is more than a seeded genesis, recorded as a conflict; a
// fork that is only a genesis an earlier pull seeded is named for
// `sync workstate rechain` instead.
func (c *chainPull) materialize(ctx context.Context, a *app.App, optIn map[string]bool, applied []workitem.Item, out *storyOutcome, now time.Time) error {
	if c == nil || !optIn["ledger"] || c.unsupported {
		return nil
	}
	for _, it := range applied {
		// A task's chain is read like a story's (sty_0810c2a3): its events, an
		// archive among them, are published by the same compare-and-append.
		if (it.Kind != workitem.KindStory && it.Kind != workitem.KindTask) || c.remote(it) {
			continue
		}
		if done, err := c.take(ctx, a, it.ID, out, now); done || err != nil {
			return err
		}
	}
	c.flush()
	return nil
}

// take materializes one chain and remembers it was read this pass. done reports
// that the pass reads no more chains: the server has no chain service.
func (c *chainPull) take(ctx context.Context, a *app.App, id string, out *storyOutcome, now time.Time) (done bool, err error) {
	rd, err := c.reader(ctx)
	if err != nil {
		return true, err
	}
	if c.taken == nil {
		c.taken = map[string]bool{}
	}
	c.taken[id] = true
	_, err = c.materializeStory(ctx, a, rd, id, out, now)
	if errors.Is(err, hosted.ErrChainUnsupported) {
		c.unsupported = true
		fmt.Fprintln(c.errw, "workstate: the server has no story chain service; pulled stories keep the chain they have here")
		return true, nil
	}
	return err != nil, err
}

// sweepTasks reads the hosted chain of every local task the pass did not read
// whose saved head is not its local tip (sty_0810c2a3). A task archived here on
// a chain that never took its hosted events — an earlier pull read no task
// chain — is refused by every push as moved; reading it here brings the hosted
// events in and moves the local-only ones after them, so the next push
// publishes it. A task whose head is its tip, or whose chain forks here, is not
// read.
func (c *chainPull) sweepTasks(ctx context.Context, a *app.App, optIn map[string]bool, out *storyOutcome, now time.Time) error {
	if c == nil || !optIn["ledger"] || c.unsupported {
		return nil
	}
	ids, err := a.Store.Ledger.StoryIDs(ctx)
	if err != nil {
		return fmt.Errorf("list the local chains: %w", err)
	}
	for _, id := range ids {
		if c.taken[id] {
			continue
		}
		it, err := a.Store.Stories.Get(ctx, id)
		if errors.Is(err, workitem.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read item %s: %w", id, err)
		}
		if it.Kind != workitem.KindTask {
			continue
		}
		local, err := a.Store.Ledger.StoryChain(ctx, id)
		if err != nil {
			return fmt.Errorf("read the chain of %s: %w", id, err)
		}
		order, oerr := chainOrder(local)
		if oerr != nil || len(order) == 0 {
			continue
		}
		head, err := c.head(id)
		if err != nil {
			return err
		}
		if head == order[len(order)-1].ID {
			continue
		}
		if done, err := c.take(ctx, a, id, out, now); done || err != nil {
			return err
		}
	}
	c.flush()
	return nil
}

// materializeStory reads one story's hosted chain into the local store. It reports
// whether the hosted chain has events and is now this machine's chain for the story.
func (c *chainPull) materializeStory(ctx context.Context, a *app.App, rd chainReader, id string, out *storyOutcome, now time.Time) (bool, error) {
	local, err := a.Store.Ledger.StoryChain(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read the chain of %s: %w", id, err)
	}
	events, latest, after, err := c.readHosted(ctx, rd, id, local)
	if err != nil {
		if errors.Is(err, hosted.ErrChainUnsupported) || errors.Is(err, hosted.ErrLoginRequired) {
			return false, err
		}
		return false, fmt.Errorf("read the hosted chain of %s: %w — re-run is safe", id, err)
	}
	if latest == "" {
		return false, nil // the server holds no events of it
	}
	fresh, perr := takePlan(local, events)
	var unlinked unlinkedError
	switch {
	case errors.Is(perr, errTakeForked):
		if isTask(ctx, a, id) {
			return c.rebaseTask(ctx, a, rd, id, local, events, latest, after, out, now)
		}
		c.forked(id, local, events, after, out)
		return false, nil
	case errors.As(perr, &unlinked):
		fmt.Fprintf(c.errw, "workstate: the hosted chain of %s %v; left as it is\n", id, perr)
		return false, nil
	}
	if err := takeEvents(ctx, a, id, fresh, now); err != nil {
		return false, err
	}
	// The head moves only when the chain now ends where the server's does; a story
	// with later local work keeps its head, and those events publish from it.
	chain, err := a.Store.Ledger.StoryChain(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read the chain of %s: %w", id, err)
	}
	if order, oerr := chainOrder(chain); oerr == nil && len(order) > 0 && order[len(order)-1].ID == latest {
		c.pending[id] = latest
	}
	return true, nil
}

// errTakeForked is takePlan declining: a hosted event builds on an event of the
// local chain that already has another local child.
var errTakeForked = errors.New("a hosted event builds on an event the local chain builds on differently")

// unlinkedError is takePlan declining: a hosted event builds on neither the local
// chain nor an event before it.
type unlinkedError struct{ event string }

func (e unlinkedError) Error() string { return "does not link at " + e.event }

// takePlan decides whether a hosted chain can be brought under a local chain and
// returns the hosted events the local chain lacks, in chain order. It declines when
// one does not link, or when one builds on an event the local chain already builds
// on differently: nothing merges. It reads nothing and writes nothing, so a pull
// and a rechain judge a take by the same rule.
func takePlan(local []ledger.Entry, events []hosted.ChainEvent) ([]hosted.ChainEvent, error) {
	held := make(map[string]bool, len(local))
	children := map[string][]string{} // base -> local events built on it
	for _, e := range local {
		held[e.ID] = true
		children[e.BaseID] = append(children[e.BaseID], e.ID)
	}
	var fresh []hosted.ChainEvent
	linked := map[string]bool{"": true}
	for _, e := range events {
		if held[e.ID] {
			linked[e.ID] = true
			continue
		}
		if !linked[e.BaseID] && !held[e.BaseID] {
			return nil, unlinkedError{event: e.ID}
		}
		fresh = append(fresh, e)
		linked[e.ID] = true
	}
	for _, e := range fresh {
		if other := slices.IndexFunc(children[e.BaseID], func(x string) bool { return x != e.ID }); other >= 0 {
			return nil, errTakeForked
		}
	}
	return fresh, nil
}

// takeEvents writes the hosted events takePlan returned, keeping each base it
// arrived with.
func takeEvents(ctx context.Context, a *app.App, id string, fresh []hosted.ChainEvent, now time.Time) error {
	for _, e := range fresh {
		entry, err := ledgerEntryOf(id, e)
		if err != nil {
			return fmt.Errorf("decode the hosted event %s of %s: %w — re-run is safe", e.ID, id, err)
		}
		if _, err := a.Store.Ledger.Upsert(ctx, entry, now); err != nil {
			return fmt.Errorf("take the hosted event %s of %s: %w — re-run is safe", e.ID, id, err)
		}
	}
	return nil
}

// forked reports a story whose local events and hosted chain disagree. A local
// chain that is only a seeded genesis is the fork an earlier pull made, which
// `sync workstate rechain` repairs; anything else is a conflict for the person to
// read.
func (c *chainPull) forked(id string, local []ledger.Entry, events []hosted.ChainEvent, after string, out *storyOutcome) {
	onHosted := map[string]bool{}
	for _, e := range events {
		onHosted[e.ID] = true
	}
	if after != "" {
		if order, err := chainOrder(local); err == nil {
			for _, e := range order {
				onHosted[e.ID] = true
				if e.ID == after {
					break
				}
			}
		}
	}
	localOnly := map[string]bool{}
	for _, e := range local {
		if !onHosted[e.ID] {
			localOnly[e.ID] = true
		}
	}
	if _, seeded := storyfold.SeededEvents(local, localOnly); seeded {
		fmt.Fprintf(c.errw, "workstate: %s holds a seeded genesis the hosted chain does not; run `satelle sync workstate rechain %s`\n", id, id)
		return
	}
	out.conflict(id, conflictChainForked)
}

// isTask reports whether id is a task in the local store.
func isTask(ctx context.Context, a *app.App, id string) bool {
	it, err := a.Store.Stories.Get(ctx, id)
	return err == nil && it.Kind == workitem.KindTask
}

// redundantSeed returns the local-only events a task rebase drops rather than
// moves: SeedItem's copy of a definition (rechain's replay drops it too), and a
// workflow_version event the hosted chain already records, which the local
// append wrote only because the local chain did not hold the hosted one. Any
// other workflow_version is moved like any other event.
func redundantSeed(seen []ledger.Entry) func(ledger.Entry) bool {
	versions := map[string]bool{}
	for _, e := range seen {
		if v := workflowVersionOf(e); v != "" {
			versions[v] = true
		}
	}
	return func(e ledger.Entry) bool {
		if e.Kind == ledger.KindDefinitionSnapshot {
			return storyfold.IsSeededEvent(e)
		}
		v := workflowVersionOf(e)
		return v != "" && versions[v]
	}
}

// workflowVersionOf is the version a workflow_version event records; empty for
// any other event.
func workflowVersionOf(e ledger.Entry) string {
	if !ledger.IsVersionEvent(e) {
		return ""
	}
	var refs map[string]string
	_ = json.Unmarshal(e.Refs, &refs)
	return refs["workflow_version"]
}

// rebaseTask brings a task's hosted chain under a local chain that forks it
// (sty_0810c2a3): the hosted events this checkout lacks are inserted, and the
// local-only events after the prefix both chains share are moved after the hosted
// latest, so the next push appends only them. A seeded genesis among the local-only
// events is dropped, never republished (redundantSeed). A task is driven by no workflow, so the
// hosted events are taken whatever they move; its row is not a fold of its chain,
// so no row is rebuilt. A shape this cannot read as prefix-then-divergence is
// reported as a story fork is.
func (c *chainPull) rebaseTask(ctx context.Context, a *app.App, rd chainReader, id string, local []ledger.Entry, events []hosted.ChainEvent, latest, after string, out *storyOutcome, now time.Time) (bool, error) {
	if after != "" {
		// The read began after the saved head; the shared prefix needs the whole chain.
		var err error
		if events, latest, err = rd.ReadStory(ctx, c.project, id, ""); err != nil {
			return false, fmt.Errorf("read the hosted chain of %s: %w — re-run is safe", id, err)
		}
	}
	order, err := chainOrder(local)
	if err != nil || len(events) == 0 || events[len(events)-1].ID != latest {
		c.forked(id, local, events, "", out)
		return false, nil
	}
	n := matchingPrefix(order, events)
	held := make(map[string]bool, len(order))
	for _, e := range order {
		held[e.ID] = true
	}
	onHosted := make(map[string]bool, len(events))
	for _, e := range events {
		onHosted[e.ID] = true
	}
	var seen []ledger.Entry
	for _, ev := range events[n:] {
		if held[ev.ID] {
			c.forked(id, local, events, "", out)
			return false, nil
		}
		e, perr := ledgerEntryOf(id, ev)
		if perr != nil {
			return false, fmt.Errorf("decode the hosted event %s of %s: %w — re-run is safe", ev.ID, id, perr)
		}
		seen = append(seen, e)
	}
	hostedEntries := append(append([]ledger.Entry(nil), order[:n]...), seen...)
	drop := redundantSeed(hostedEntries)
	var unpublished, seeded []string
	for _, e := range order[n:] {
		switch {
		case onHosted[e.ID]:
			c.forked(id, local, events, "", out)
			return false, nil
		case drop(e):
			seeded = append(seeded, e.ID)
		default:
			unpublished = append(unpublished, e.ID)
		}
	}
	if len(unpublished) > 0 {
		if err := a.Store.Ledger.RebaseEvents(ctx, id, seen, unpublished, latest); err != nil {
			return false, fmt.Errorf("rebase the task %s on its hosted chain: %w — re-run is safe", id, err)
		}
	}
	if len(seeded) > 0 {
		if err := a.Store.Ledger.RemoveEvents(ctx, id, seeded, drop); err != nil {
			return false, fmt.Errorf("drop the seeded genesis of %s: %w", id, err)
		}
	}
	if len(unpublished) == 0 {
		if err := takeEvents(ctx, a, id, events[n:], now); err != nil {
			return false, err
		}
	}
	fmt.Fprintf(c.errw, "workstate: the task %s moved on the server: %d event(s) applied here, %d re-appended after %s\n", id, len(seen), len(unpublished), latest)
	c.pending[id] = latest
	out.clear(id)
	_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, nil, []string{id}, now)
	return true, nil
}
