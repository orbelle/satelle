package ledgersync

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// rules is a stub Chooser: category bug goes to bugflow at triage, anything
// else to default at backlog. before runs inside WorkflowFor, to stage races.
type rules struct{ before func() }

func (r *rules) WorkflowFor(it item.Item) (string, string, error) {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	if it.Category == "bug" {
		return "bugflow", "triage", nil
	}
	return "default", "backlog", nil
}

type world struct {
	srv     *fakeserver.Server
	client  *hosted.Client
	project string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	srv := fakeserver.Start(t, "tok")
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p, err := c.Projects.CreateProject(context.Background(), &satellepb.CreateProjectRequest{Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	return &world{srv: srv, client: c, project: p.GetId()}
}

// home is one machine: its own ledger, cache and sync state.
func (w *world) home(t *testing.T, choose Chooser) *Syncer {
	t.Helper()
	dir := t.TempDir()
	l, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &Syncer{Client: w.client, Project: w.project, Ledger: l, Cache: c, StatePath: filepath.Join(dir, "state.json"), Choose: choose}
}

func chainOf(t *testing.T, s *Syncer, id string) []ledger.Event {
	t.Helper()
	evs, err := s.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func hashes(evs []ledger.Event) string {
	var h []string
	for _, e := range evs {
		h = append(h, e.Hash)
	}
	return strings.Join(h, ",")
}

// A pulled item with no workflow is placed by the rules: item.updated
// {workflow} then item.transitioned {from "", to start}, both by satelle,
// on the server first; status never travels on item.updated.
func TestPullPlacesByRules(t *testing.T) {
	w := newWorld(t)
	bug := w.srv.SeedItem(w.project, map[string]any{"kind": "story", "title": "a bug", "category": "bug"})
	plain := w.srv.SeedItem(w.project, map[string]any{"kind": "story", "title": "plain"})
	a := w.home(t, &rules{})
	res, err := a.Pull(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Placed) != 2 {
		t.Fatalf("placed %v", res.Placed)
	}
	for id, want := range map[string][2]string{bug: {"bugflow", "triage"}, plain: {"default", "backlog"}} {
		evs := chainOf(t, a, id)
		if len(evs) != 3 || evs[1].Kind != item.KindUpdated || evs[2].Kind != item.KindTransitioned {
			t.Fatalf("%s chain = %v", id, evs)
		}
		var upd map[string]any
		if err := json.Unmarshal(evs[1].Payload, &upd); err != nil || upd["workflow"] != want[0] || upd["status"] != nil || len(upd) != 1 {
			t.Errorf("%s item.updated = %s", id, evs[1].Payload)
		}
		var mv item.Move
		if err := json.Unmarshal(evs[2].Payload, &mv); err != nil || mv.From != "" || mv.To != want[1] {
			t.Errorf("%s item.transitioned = %s", id, evs[2].Payload)
		}
		if evs[1].Actor != Actor || evs[2].Actor != Actor {
			t.Errorf("%s placing actors = %s, %s", id, evs[1].Actor, evs[2].Actor)
		}
		it, err := item.Fold(evs)
		if err != nil || it.Workflow != want[0] || it.Status != want[1] {
			t.Errorf("%s folds to %s/%s, %v", id, it.Workflow, it.Status, err)
		}
		var server []string
		for _, e := range w.srv.Events(w.project, id) {
			server = append(server, e.GetHash())
		}
		if strings.Join(server, ",") != hashes(evs) {
			t.Errorf("%s: the server's chain is not the local one", id)
		}
	}
	// A second pull places nothing.
	res, err = a.Pull(context.Background())
	if err != nil || len(res.Placed) != 0 || len(chainOf(t, a, bug)) != 3 {
		t.Fatalf("second pull placed %v (%v)", res.Placed, err)
	}
}

// A second home imports the first home's placement and adds nothing.
func TestSecondHomeAppliesNothing(t *testing.T) {
	w := newWorld(t)
	id := w.srv.SeedItem(w.project, map[string]any{"kind": "story", "title": "shared", "category": "bug"})
	a, b := w.home(t, &rules{}), w.home(t, &rules{})
	if _, err := a.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := b.Pull(context.Background())
	if err != nil || len(res.Placed) != 0 {
		t.Fatalf("b placed %v (%v)", res.Placed, err)
	}
	if hashes(chainOf(t, b, id)) != hashes(chainOf(t, a, id)) {
		t.Fatal("b's chain differs from a's")
	}
}

// When another home places between this home's read and its append, the
// append is refused as stale; this home imports the winner's placement, adds
// none of its own and verifies the whole chain.
func TestRaceLoserImportsWinnersPlacement(t *testing.T) {
	w := newWorld(t)
	id := w.srv.SeedItem(w.project, map[string]any{"kind": "story", "title": "raced", "category": "bug"})
	b := w.home(t, &rules{})
	a := w.home(t, &rules{before: func() {
		if _, err := b.Pull(context.Background()); err != nil {
			t.Errorf("b's pull: %v", err)
		}
	}})
	res, err := a.Pull(context.Background())
	if err != nil {
		t.Fatalf("a's pull: %v", err)
	}
	if len(res.Placed) != 0 {
		t.Errorf("the loser placed %v", res.Placed)
	}
	ae, be := chainOf(t, a, id), chainOf(t, b, id)
	if len(ae) != 3 || hashes(ae) != hashes(be) {
		t.Fatalf("a's chain %d events, equal to b's: %v", len(ae), hashes(ae) == hashes(be))
	}
	if err := ledger.VerifyChain(id, ae); err != nil {
		t.Fatal(err)
	}
	st, err := a.LoadState()
	if err != nil || st.Heads[id] != be[2].Hash {
		t.Fatalf("a's saved head %q, want b's tail %q (%v)", st.Heads[id], be[2].Hash, err)
	}
}

// An item that has local events not yet pushed is not placed over (that
// would fork its chain): it is reported unplaced.
func TestUnpushedLocalEventsAreNotPlacedOver(t *testing.T) {
	w := newWorld(t)
	id := w.srv.SeedItem(w.project, map[string]any{"kind": "story", "title": "edited"})
	a := w.home(t, nil)
	if _, err := a.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ledger.Append(id, "note.added", "me", map[string]string{"n": "local"}); err != nil {
		t.Fatal(err)
	}
	a.Choose = &rules{}
	res, err := a.Pull(context.Background())
	if err != nil || len(res.Placed) != 0 || len(res.Unplaced) != 1 || res.Unplaced[0] != id {
		t.Fatalf("placed %v unplaced %v (%v)", res.Placed, res.Unplaced, err)
	}
}
