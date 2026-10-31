package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A story's ledger events leave a push by compare-and-append (sty_9bdf08c4): one
// Chain/Append per story, naming the last event the server accepted from this
// checkout (the story's head in the work-state cursor). The server takes the
// events only while that head is still the story's latest; otherwise nothing is
// stored and the agent is told the story moved. Nothing here merges, overwrites
// or holds. The bulk Apply path no longer carries story-scoped ledger entries.

// storyAppend is one story's local chain and where its unpublished events begin.
type storyAppend struct {
	id    string
	order []ledger.Entry // the whole local chain, first event to last
	from  int            // index of the first event the server has not accepted
	// headKnown is true when the saved head is an event of order. Without it
	// (no head saved, or a rebuilt store) the hosted chain is read first, so no
	// event the server already holds is sent again.
	headKnown bool
	err       error // the local chain cannot be sent as it is
}

// base is the event the unpublished events build on; empty when none was published.
func (p storyAppend) base() string {
	if p.from == 0 {
		return ""
	}
	return p.order[p.from-1].ID
}

// planStoryAppends lists the stories with events past their head. It is not driven
// by the bulk ledger mark: a story the server refused stays listed on every push
// until it is accepted. scope narrows it to the stories a scoped push names.
//
// Every work item with ledger events is listed, tasks and executions as well as
// stories: the bulk path carried all of them before, and the server keys a chain by
// the item id it is given without reading its kind. The fold-drift check is
// narrower (workstate_validate.go validates KindStory items only) because only a
// story's status is folded from its events; that bears on what is withheld, not on
// what is published.
func planStoryAppends(ctx context.Context, a *app.App, scope scopeSet, heads map[string]string) ([]storyAppend, error) {
	ids, err := a.Store.Ledger.StoryIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list story chains: %w", err)
	}
	var plan []storyAppend
	for _, id := range ids {
		if !scope.HasLedger(id) {
			continue
		}
		events, err := a.Store.Ledger.StoryChain(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read the chain of %s: %w", id, err)
		}
		order, err := chainOrder(events)
		if err != nil {
			plan = append(plan, storyAppend{id: id, err: err})
			continue
		}
		// A head this store no longer holds (the store was replaced or restored) is
		// treated as none: the server compares the base and either resumes from its
		// own prefix of this chain or reports the story as moved, so nothing is
		// guessed and nothing is stuck.
		from, known := 0, false
		if head := heads[id]; head != "" {
			if i := slices.IndexFunc(order, func(e ledger.Entry) bool { return e.ID == head }); i >= 0 {
				from, known = i+1, true
			}
		}
		if from < len(order) {
			plan = append(plan, storyAppend{id: id, order: order, from: from, headKnown: known})
		}
	}
	return plan, nil
}

// chainOrder puts a story's events in chain order, each built on the one before.
// Insertion order is not it: a chain pulled from another machine can arrive out of
// order. A chain that forks, or has events no path from the first reaches, cannot
// be sent as one and is reported, never guessed at.
func chainOrder(events []ledger.Entry) ([]ledger.Entry, error) {
	byBase := make(map[string][]ledger.Entry, len(events))
	for _, e := range events {
		byBase[e.BaseID] = append(byBase[e.BaseID], e)
	}
	order := make([]ledger.Entry, 0, len(events))
	for base := ""; ; {
		next := byBase[base]
		if len(next) == 0 {
			break
		}
		if len(next) > 1 {
			return nil, fmt.Errorf("the local chain forks at %s: %d events build on it", forkLabel(base), len(next))
		}
		order = append(order, next[0])
		base = next[0].ID
	}
	if len(order) != len(events) {
		return nil, fmt.Errorf("the local chain is broken: %d of %d events are not reached from its first event", len(events)-len(order), len(events))
	}
	return order, nil
}

func forkLabel(base string) string {
	if base == "" {
		return "its start"
	}
	return base
}

func chainEvents(entries []ledger.Entry) ([]hosted.ChainEvent, error) {
	out := make([]hosted.ChainEvent, 0, len(entries))
	for _, e := range entries {
		raw, err := marshalWorkstateLedger(e)
		if err != nil {
			return nil, err
		}
		out = append(out, hosted.ChainEvent{ID: e.ID, BaseID: e.BaseID, WorkflowVersion: e.WorkflowHash, Payload: raw})
	}
	return out, nil
}

// publishStoryEvents appends each planned story that is not withheld and returns
// how many stories it published. A story the server refused or that cannot be sent
// is reported on w and does not stop the others; the returned error counts and
// names them so the push exits non-zero and says which.
func publishStoryEvents(ctx context.Context, w io.Writer, a *app.App, client *hosted.Client, server, project string, plan []storyAppend, held map[string]string) (published int, err error) {
	var conn *hosted.ChainConn
	heads := &headSaver{server: server, project: project, root: a.RepoRoot, pending: map[string]string{}}
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
		// The heads are written together, whichever way the loop ends. One that is
		// lost costs nothing but a resume: the next push is refused as a stale base
		// whose latest event is in the local chain, and continues from it.
		if serr := heads.flush(w); serr != nil && err == nil {
			err = serr
		}
	}()
	var failed []string
	sent := 0
	for _, p := range plan {
		if _, withheld := held[p.id]; withheld {
			continue
		}
		if p.err != nil {
			fmt.Fprintf(w, "the story %s was not published: %v\n", p.id, p.err)
			failed = append(failed, p.id)
			continue
		}
		if conn == nil {
			var err error
			if conn, err = client.OpenChain(ctx); err != nil {
				return published, fmt.Errorf("open the story chain: %w", err)
			}
		}
		if sent > 0 && !pause(ctx, appendPace) {
			return published, ctx.Err()
		}
		sent++
		err := appendStory(ctx, w, a, conn, project, p, heads)
		switch {
		case errors.Is(err, hosted.ErrLoginRequired):
			return published, err
		case errors.Is(err, errUpToDate):
		case err != nil:
			failed = append(failed, p.id)
		default:
			published++
		}
		if sent%headFlushEvery == 0 {
			_ = heads.flush(io.Discard) // kept pending on failure; the final flush reports it
		}
	}
	if len(failed) > 0 {
		return published, fmt.Errorf("%d stor(y/ies) not published (%s) — see the reasons on stderr", len(failed), strings.Join(failed, ", "))
	}
	return published, nil
}

// withoutStoryEntries drops the ledger entries that belong to a story: those go by
// Chain/Append. Entries with no story stay on the bulk path.
func withoutStoryEntries(raws []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(raws))
	for _, raw := range raws {
		var w struct {
			StoryID string `json:"story_id"`
		}
		if json.Unmarshal(raw, &w) == nil && w.StoryID != "" {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// appendStory sends one story's unpublished events. When the server's latest event
// is one this checkout already holds — its own earlier publish, whose head was
// never saved — it resumes from that event once; the same chain is continued, not
// merged. When the server holds events this checkout has not seen (another
// machine's, made while this one was offline) after an event published from
// here, they are applied locally and the unpublished events re-appended after
// them, once (sty_0dc600e1): events are
// append-only and status is the fold of chain order, so nothing is merged. A base
// stale again after that is the agent's to read and decide.
//
// The events go in batches of about appendBatchBytes, in chain order, each built on
// the latest event the server accepted before it; the head is handed to the saver
// after every accepted batch, and a head lost to an interrupted push is recovered
// by the resume above on the next one. A stale base
// part-way through is handled as at the start, from the batch it refused.
func appendStory(ctx context.Context, w io.Writer, a *app.App, conn *hosted.ChainConn, project string, p storyAppend, heads *headSaver) error {
	from, resumed, rebased := p.from, false, false
	if !p.headKnown {
		latest, missing, err := hostedLatest(ctx, conn, project, p)
		switch {
		case errors.Is(err, hosted.ErrChainUnsupported):
			// No Read to ask: the stale-base resume below finds the prefix instead.
		case err != nil:
			fmt.Fprintf(w, "the story %s was not published: %v\n", p.id, err)
			return err
		case latest == "":
		default:
			i := slices.IndexFunc(p.order, func(e ledger.Entry) bool { return e.ID == latest })
			if i < 0 {
				return reportMoved(w, a, &hosted.StaleBaseError{StoryID: p.id, Base: p.base(), LatestID: latest, Missing: missing})
			}
			heads.set(p.id, latest)
			if i+1 == len(p.order) {
				fmt.Fprintf(w, "the story %s is already published up to %s: nothing to send\n", p.id, latest)
				_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, nil, []string{p.id}, time.Now().UTC())
				return errUpToDate
			}
			fmt.Fprintf(w, "the story %s is already published up to %s: resuming from it\n", p.id, latest)
			from, resumed = i+1, true
		}
	}
	// events are the encoded events of p.order[start:], made once and again only
	// when a resume reaches back before them.
	start := from
	events, err := chainEvents(p.order[start:])
	if err != nil {
		fmt.Fprintf(w, "the story %s was not published: %v\n", p.id, err)
		return err
	}
	for from < len(p.order) {
		base := ""
		if from > 0 {
			base = p.order[from-1].ID
		}
		pending := events[from-start:]
		n := batchLen(project, p.id, base, pending)
		_, err := conn.AppendStory(ctx, project, p.id, base, pending[:n])
		if err == nil {
			from += n
			heads.set(p.id, p.order[from-1].ID)
			continue
		}
		var stale *hosted.StaleBaseError
		if !errors.As(err, &stale) {
			fmt.Fprintf(w, "the story %s was not published: %v\n", p.id, err)
			return err
		}
		i := slices.IndexFunc(p.order, func(e ledger.Entry) bool { return e.ID == stale.LatestID })
		var rebaseErr error
		// A story nothing was published from here is no rebase: its local chain
		// starts a fork of its own, which is `sync workstate rechain`'s to repair.
		if i < 0 && from > 0 && !resumed && !rebased {
			order, at, rerr := rebaseStory(ctx, a, conn, project, p.id, p.order, from)
			if errors.Is(rerr, hosted.ErrLoginRequired) {
				return rerr
			}
			rebaseErr = rerr
			if rerr == nil {
				fmt.Fprintf(w, "the story %s moved on the server: %d event(s) applied here, %d re-appended after %s\n", p.id, at-from, len(order)-at, order[at-1].ID)
				heads.set(p.id, order[at-1].ID)
				p.order, from, start, rebased = order, at, at, true
				if events, err = chainEvents(p.order[start:]); err != nil {
					fmt.Fprintf(w, "the story %s was not published: %v\n", p.id, err)
					return err
				}
				continue
			}
		}
		if resumed || rebased || i < 0 {
			reason := fmt.Sprintf("moved since %s: re-read and decide (server is at %s, %d event(s) not seen)", stale.Since(), stale.LatestID, stale.Missing)
			if rebaseErr != nil {
				reason += fmt.Sprintf("; not rebased: %v", rebaseErr)
			}
			fmt.Fprintf(w, "the story %s %s\n", p.id, reason)
			_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, map[string]string{p.id: reason}, nil, time.Now().UTC())
			return err
		}
		fmt.Fprintf(w, "the story %s is already published up to %s: resuming from it\n", p.id, stale.LatestID)
		heads.set(p.id, stale.LatestID)
		from, resumed = i+1, true
		if from < start {
			start = from
			if events, err = chainEvents(p.order[start:]); err != nil {
				fmt.Fprintf(w, "the story %s was not published: %v\n", p.id, err)
				return err
			}
		}
	}
	_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, nil, []string{p.id}, time.Now().UTC())
	return nil
}

// errUpToDate is appendStory's answer for a story the server already holds whole:
// nothing was sent, and nothing failed.
var errUpToDate = errors.New("already published")

// hostedLatest reads the story's hosted chain: its latest event, and how many of
// its events the local chain does not hold.
func hostedLatest(ctx context.Context, conn *hosted.ChainConn, project string, p storyAppend) (latest string, missing int, err error) {
	events, latest, err := conn.ReadStory(ctx, project, p.id, "")
	if err != nil {
		return "", 0, err
	}
	for _, e := range events {
		if !slices.ContainsFunc(p.order, func(l ledger.Entry) bool { return l.ID == e.ID }) {
			missing++
		}
	}
	return latest, missing, nil
}

// reportMoved tells the agent the story moved on the server and records the
// conflict; it returns stale so a caller can hand it back as the story's error.
func reportMoved(w io.Writer, a *app.App, stale *hosted.StaleBaseError) error {
	reason := fmt.Sprintf("moved since %s: re-read and decide (server is at %s, %d event(s) not seen)", stale.Since(), stale.LatestID, stale.Missing)
	fmt.Fprintf(w, "the story %s %s\n", stale.StoryID, reason)
	_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, map[string]string{stale.StoryID: reason}, nil, time.Now().UTC())
	return stale
}

// rebaseStory reads the server's events after order[from-1] that this checkout has
// not seen, applies them, and moves order[from:] (the unpublished events) after
// them. It returns the new local chain and the index of its first unpublished
// event. A base the server does not know is read again from the start, keeping
// only the events not already held.
func rebaseStory(ctx context.Context, a *app.App, conn *hosted.ChainConn, project, storyID string, order []ledger.Entry, from int) ([]ledger.Entry, int, error) {
	base := ""
	if from > 0 {
		base = order[from-1].ID
	}
	remote, latest, err := conn.ReadStory(ctx, project, storyID, base)
	if err != nil && base != "" && !errors.Is(err, hosted.ErrChainUnsupported) && !errors.Is(err, hosted.ErrLoginRequired) {
		remote, latest, err = conn.ReadStory(ctx, project, storyID, "")
	}
	if err != nil {
		return nil, 0, err
	}
	held := make(map[string]bool, len(order))
	for _, e := range order {
		held[e.ID] = true
	}
	var seen []ledger.Entry
	for _, ev := range remote {
		if held[ev.ID] {
			continue
		}
		e, perr := ledgerEntryOf(storyID, ev)
		if perr != nil {
			return nil, 0, perr
		}
		seen = append(seen, e)
	}
	if len(seen) == 0 || latest == "" || seen[len(seen)-1].ID != latest {
		return nil, 0, fmt.Errorf("the server's events after %s do not end at its latest %s", forkLabel(base), latest)
	}
	if seen[0].BaseID != base {
		return nil, 0, fmt.Errorf("the server's chain does not build on %s, the last event published from here", forkLabel(base))
	}
	if err := refuseMovedElsewhere(a, seen); err != nil {
		return nil, 0, err
	}
	unpublished := make([]string, 0, len(order)-from)
	for _, e := range order[from:] {
		unpublished = append(unpublished, e.ID)
	}
	if err := a.Store.Ledger.RebaseEvents(ctx, storyID, seen, unpublished, latest); err != nil {
		return nil, 0, err
	}
	out := make([]ledger.Entry, 0, len(order)+len(seen))
	out = append(append(out, order[:from]...), seen...)
	at := len(out)
	out = append(out, order[from:]...)
	out[at].BaseID = latest
	if err := rebuildRebased(ctx, a, storyID); err != nil {
		return nil, 0, err
	}
	return out, at, nil
}

// refuseMovedElsewhere keeps the chain's base-check refusal standing when the
// events this checkout has not seen change the story's status (sty_9c782f0e):
// another location moved the story since this checkout's base, so splicing this
// checkout's events after that move would fold two drivers' moves into one chain.
// Events that move no status (a comment, a task's result) are still rebased. The
// refusal names the other location's latest working_on presence when the events
// carry one; that presence is advisory and decides nothing here — the status move
// does.
func refuseMovedElsewhere(a *app.App, seen []ledger.Entry) error {
	if !slices.ContainsFunc(seen, func(e ledger.Entry) bool { return e.Kind == ledger.KindStatusTransition }) {
		return nil
	}
	self, _ := thisLocation(a)
	if r, ok := presenceIn(seen, self); ok {
		return fmt.Errorf("another location moved its status: %s", presenceText(r))
	}
	return errors.New("another location moved its status")
}

// rebuildRebased brings a rebased story's row under its new chain.
func rebuildRebased(ctx context.Context, a *app.App, storyID string) error {
	row, err := a.Store.Stories.Get(ctx, storyID)
	if errors.Is(err, workitem.ErrNotFound) {
		return nil
	}
	if err != nil || row.Kind != workitem.KindStory {
		return err
	}
	if _, err := verb.RebuildRow(ctx, a.Store.Stories, a.Store.Ledger, row, time.Now().UTC()); err != nil {
		return fmt.Errorf("rebuild %s from its rebased chain: %w", storyID, err)
	}
	return nil
}

// appendBatchBytes caps the encoded size of one Append: well under the server's
// 8 MiB request limit and, with the echo of the stored events, the 16 MiB the
// client reads back. A var so a test can lower it.
var appendBatchBytes = 3 << 20

// batchLen is how many of events, taken from the front, go in one Append. An event
// that alone passes the cap still goes alone: the server's answer to it is the
// story's to report.
func batchLen(project, storyID, base string, events []hosted.ChainEvent) int {
	size, n := hosted.AppendFrameSize(project, storyID, base), 0
	for _, e := range events {
		size += hosted.AppendEventSize(storyID, e)
		if n > 0 && size > appendBatchBytes {
			break
		}
		n++
	}
	return n
}

// appendPace is the wait between one story's Appends and the next, so a push of
// many stories does not trip the rate limit of the proxy in front of the server. A
// var so tests run without waiting.
var appendPace = 25 * time.Millisecond

// headFlushEvery is how many stories a push publishes between writes of their heads.
const headFlushEvery = 100

// pause waits d, or until ctx ends, and reports whether the wait completed.
func pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// headSaver collects the latest event the server accepted for each story during a
// push and writes them in one update of the state file, which is shared by every
// checkout and rewritten whole: one write per story is a rewrite per story.
type headSaver struct {
	server, project, root string
	pending               map[string]string
}

func (h *headSaver) set(storyID, head string) { h.pending[storyID] = head }

// flush writes the pending heads. They stay pending when the write fails, so a
// later flush tries again.
func (h *headSaver) flush(w io.Writer) error {
	if len(h.pending) == 0 {
		return nil
	}
	if err := hosted.SaveStoryHeads(h.server, h.project, h.root, h.pending); err != nil {
		fmt.Fprintf(w, "%d stor(y/ies) were published but their heads were not saved: %v\n", len(h.pending), err)
		return fmt.Errorf("save story heads: %w", err)
	}
	h.pending = map[string]string{}
	return nil
}
