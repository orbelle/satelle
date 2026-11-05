package verb

// Hosted mode and the local history cache (sty_0dc600e1, sty_7c01ca14). When the
// user is signed in and the repo bound, the server is the store of record. In team
// scope the local ledger is a cache: it holds a story's full history while the
// story is in a performing state of its governing route, whichever location works
// it. Every other story keeps its row, and its history is fetched again when it is
// opened. Personal and local scope keep every chain; nothing here trims them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// RepoBound is config.RepoBound: the repo names its hosted project explicitly.
func RepoBound(cfg config.Config) bool { return config.RepoBound(cfg) }

// HostedMode reports whether the server is the store of record for this repo: the
// user is signed in and the repo is bound.
func HostedMode(cfg config.Config) bool {
	return SignedIn() && RepoBound(cfg)
}

// MachineSet returns the stories this machine engaged: those with a KindWorkingOn
// row naming location. An empty location is this machine's own location as the
// location resolver names it. A row with no location is counted too: a story
// worktree engaged before its checkout resolved to the main tree's location wrote
// one (sty_9ece9394), and the ledger is append-only, so those rows stay as they
// are. A row naming another location is not this machine's. The row is read to
// choose what stays cached, never to refuse an engage.
func MachineSet(ctx context.Context, led *ledger.Store, location string) (map[string]bool, error) {
	if led == nil {
		return nil, fmt.Errorf("verb: machine set needs a ledger")
	}
	if location == "" && locationResolver != nil {
		location, _ = locationResolver()
	}
	out := map[string]bool{}
	err := led.ForEachKind(ctx, "", ledger.KindWorkingOn, func(e ledger.Entry) error {
		var p WorkingOnPayload
		if json.Unmarshal(e.Payload, &p) == nil && (p.Location == location || p.Location == "") && e.StoryID != "" {
			out[e.StoryID] = true
		}
		return nil
	})
	return out, err
}

// StoryPerforming returns the predicate that reports whether a story's folded
// status is a performing state — a non-terminal engaging state — of its governing
// route (wfgovern.ClockFor, the one owner of that rule). The workflows are read
// once. A story whose governance cannot be resolved counts as performing, so the
// store keeps its history rather than guessing.
func StoryPerforming(ctx context.Context) func(workitem.Item) bool {
	var wfs []docindex.Doc
	idx, err := requireDocIndex()
	if err == nil {
		wfs, err = idx.List(ctx, "workflows")
	}
	if err != nil {
		return func(workitem.Item) bool { return true }
	}
	return func(it workitem.Item) bool {
		spec, _, _, err := wfgovern.SpecFor(wfs, it)
		return err != nil || slices.Contains(spec.NonTerminalEngagingStates(), it.Status)
	}
}

// HeldStories returns the stories whose full history a team-scope local store
// keeps: every story with events whose folded row is performing (StoryPerforming),
// whichever location engaged it. A chain with no row is held too.
func HeldStories(ctx context.Context, led *ledger.Store, stories *workitem.Store) (map[string]bool, error) {
	if led == nil || stories == nil {
		return nil, fmt.Errorf("verb: held stories needs a ledger and a story store")
	}
	ids, err := led.StoryIDs(ctx)
	if err != nil {
		return nil, err
	}
	performing := StoryPerforming(ctx)
	held := map[string]bool{}
	for _, id := range ids {
		it, err := stories.Get(ctx, id)
		if errors.Is(err, workitem.ErrNotFound) {
			held[id] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if performing(it) {
			held[id] = true
		}
	}
	return held, nil
}

// TrimStoryHistory drops the history of every story with events that is not in
// held, and returns how many it dropped. A story is dropped only when the server
// holds all of it: its chain ends at heads[id], the last event the server accepted
// from this checkout, and its row is not waiting on an Apply. Everything else is
// kept as it is.
func TrimStoryHistory(ctx context.Context, led *ledger.Store, held map[string]bool, heads map[string]string) (int, error) {
	if led == nil {
		return 0, fmt.Errorf("verb: trim needs a ledger")
	}
	ids, err := led.StoryIDs(ctx)
	if err != nil {
		return 0, err
	}
	pending, err := led.Marked(ctx, ledger.MarkItemPending)
	if err != nil {
		return 0, err
	}
	dropped := 0
	for _, id := range ids {
		if held[id] || pending[id] || heads[id] == "" {
			continue
		}
		ok, err := led.DropStoryHistory(ctx, id, heads[id])
		if err != nil {
			return dropped, err
		}
		if ok {
			dropped++
		}
	}
	return dropped, nil
}
