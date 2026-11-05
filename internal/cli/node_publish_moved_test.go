package cli

// A node's end-of-run push re-reads the hosted chain of each story it appends to
// and plans on that head (sty_6ac9a6f1): events satelle.dev appended to the
// subject during the run, a status move among them, never refuse it, and nobody is
// told to rechain a node that has ended.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

const movedNodeServer = "http://node.example"

// movedRun is a node run whose subject satelle.dev moved on between the node's
// pre-run fetch and its push.
type movedRun struct {
	node      *nodeSync
	run       nodeRun
	published []string       // the subject's hosted chain at the fetch
	ownRows   []string       // the node's rows on the subject, in order
	foreign   []ledger.Entry // what satelle.dev appended after the fetch
}

// seedMovedRun publishes the subject's chain up to the node's rows, runs the
// node's fetch, saves the subject's head only when saveHead, and appends seven
// foreign events of mixed kinds, a status_transition among them, to the hosted
// chain. With statusMove the node's own rows end in a status_transition.
func seedMovedRun(t *testing.T, saveHead, statusMove bool) movedRun {
	t.Helper()
	fx := newHoldFixture(t, appendOnlyToml)
	run := seedNodeRun(t)
	if statusMove {
		withRepoStore(t, func(db *store.DB) {
			if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: run.subject, Kind: ledger.KindStatusTransition, Actor: "remote-node",
				Body: "backlog → in_progress", Payload: json.RawMessage(`{"from":"backlog","to":"in_progress"}`)}, time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
	local := localChain(t, run.subject)
	cut := slices.IndexFunc(local, func(e ledger.Entry) bool { return e.Kind == ledger.KindExecutionLink })
	if cut <= 0 {
		t.Fatalf("the subject's chain has no execution link after its first event: %v", idsOf(local))
	}
	m := movedRun{node: newNodeSync(t), run: run, ownRows: idsOf(local[cut:])}
	for _, e := range local[:cut] {
		m.node.f.chain.seed("probe", run.subject, hostedEvent(t, e))
		m.published = append(m.published, e.ID)
	}
	if saveHead {
		if err := hosted.SaveStoryHeads(movedNodeServer, "probe", fx.repo, map[string]string{run.subject: m.published[cut-1]}); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := nodeFetchOn(t, movedNodeServer, run.subject); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	now := time.Now().UTC()
	link, _ := json.Marshal(ledger.ExecutionLinkPayload{Execution: "exe_other", Task: "tsk_other", Remote: "nod_other", Outcome: "accept"})
	kinds := []ledger.Entry{
		workingOnAt(t, "loc_elsewhere"),
		{Kind: ledger.KindComment, Body: "a comment made elsewhere"},
		{Kind: ledger.KindRemoteRun, Body: "remote run", Payload: json.RawMessage(`{"node":"nod_killed","state":"killed"}`)},
		{Kind: ledger.KindExecutionLink, Payload: link},
		{Kind: ledger.KindStatusTransition, Body: "backlog → plan", Payload: json.RawMessage(`{"from":"backlog","to":"plan"}`)},
		{Kind: ledger.KindComment, Body: "another comment"},
		workingOnAt(t, "loc_elsewhere"),
	}
	base := m.published[cut-1]
	for _, e := range kinds {
		e.ID, e.StoryID, e.BaseID, e.Actor, e.CreatedAt = ledger.NewID(), run.subject, base, "satelle.dev", now
		m.node.f.chain.seed("probe", run.subject, hostedEvent(t, e))
		m.foreign = append(m.foreign, e)
		base = e.ID
	}
	return m
}

// sty_6ac9a6f1 AC1, AC2, AC3: the node's rows on a subject satelle.dev moved on
// during the run, a status_transition among its events, are rebased by the node and
// published on the new head, with no rechain advice, whether or not the pre-run
// fetch left a head saved.
func TestNodePublishRebasesOnAHeadMovedDuringTheRun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		saveHead bool
	}{{"saved head", true}, {"no saved head", false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := seedMovedRun(t, tc.saveHead, false)

			rep, log, err := runNodePublishReport(t, nodeClient(movedNodeServer, "node-tok"), movedNodeServer, "probe", "nod_eq", m.run)
			if err != nil {
				t.Fatalf("node publish: %v\n%s", err, log)
			}
			for _, banned := range []string{"rechain", "moved since"} {
				if strings.Contains(log, banned) {
					t.Errorf("the node's output carries %q:\n%s", banned, log)
				}
			}
			if !strings.Contains(log, "the story "+m.run.subject+" moved on the server: 7 event(s) applied here") {
				t.Errorf("the node did not rebase the subject:\n%s", log)
			}
			if len(rep.Unpublished) != 0 {
				t.Errorf("unpublished = %v, want none", rep.Unpublished)
			}
			want := slices.Concat(m.published, idsOf(m.foreign), m.ownRows)
			if got := m.node.f.chain.stored("probe", m.run.subject); !slices.Equal(got, want) {
				t.Errorf("the subject's hosted chain = %v, want the fetched chain, the foreign events, then the node's rows %v", got, want)
			}
			reqs := m.node.f.chain.appendsFor(m.run.subject)
			last := reqs[len(reqs)-1]
			if top := m.foreign[len(m.foreign)-1].ID; last.GetBaseId() != top || !slices.Equal(eventIDs(last.GetEvents()), m.ownRows) {
				t.Errorf("the accepted Append sent %v on %s, want the node's rows %v on the last foreign event %s", eventIDs(last.GetEvents()), last.GetBaseId(), m.ownRows, top)
			}
		})
	}
}

// sty_6ac9a6f1 AC2, AC3: the one refusal, a node row that is itself a status move
// while a foreign status move is unseen, is reported in the publish's report
// naming the story and each foreign event by id and kind, and nowhere tells
// anyone to rechain.
func TestNodePublishReportsAConflictingStatusMoveWithoutRechain(t *testing.T) {
	m := seedMovedRun(t, true, true)

	rep, log, err := runNodePublishReport(t, nodeClient(movedNodeServer, "node-tok"), movedNodeServer, "probe", "nod_eq", m.run)
	if err == nil {
		t.Fatalf("a node status move over a foreign one must not be published:\n%s", log)
	}
	if !strings.Contains(err.Error(), m.run.subject) {
		t.Errorf("error = %v, want it to name %s", err, m.run.subject)
	}
	if len(rep.Unpublished) != 1 {
		t.Fatalf("unpublished = %v, want the subject alone", rep.Unpublished)
	}
	got := rep.Unpublished[0]
	if !strings.HasPrefix(got, m.run.subject+": ") || !strings.Contains(got, "the node's status move") {
		t.Errorf("unpublished = %q, want the subject and the conflicting status move", got)
	}
	for _, e := range m.foreign {
		if !strings.Contains(got, e.ID+" "+e.Kind) {
			t.Errorf("unpublished = %q, want the foreign event %s %s named", got, e.ID, e.Kind)
		}
	}
	for what, text := range map[string]string{"error": err.Error(), "output": log, "report": strings.Join(rep.Unpublished, "\n")} {
		if strings.Contains(text, "rechain") {
			t.Errorf("the node's %s carries rechain advice:\n%s", what, text)
		}
	}
	want := slices.Concat(m.published, idsOf(m.foreign))
	if got := m.node.f.chain.stored("probe", m.run.subject); !slices.Equal(got, want) {
		t.Errorf("the subject's hosted chain = %v, want it unchanged by the refused rows: %v", got, want)
	}
}

// sty_6ac9a6f1 AC2: a local push keeps sty_9c782f0e's refusal of any unseen status
// move, with its text; a node's push refuses only when its own rows move status.
func TestRefuseMovedElsewhereChecksANodesOwnRows(t *testing.T) {
	a := &app.App{RepoRoot: t.TempDir()}
	seen := []ledger.Entry{{ID: "evt_c", Kind: ledger.KindComment}, {ID: "evt_s", Kind: ledger.KindStatusTransition}}
	quiet := []ledger.Entry{{ID: "evt_link", Kind: ledger.KindExecutionLink}, {ID: "evt_note", Kind: ledger.KindComment}}
	moves := append(slices.Clone(quiet), ledger.Entry{ID: "evt_mine", Kind: ledger.KindStatusTransition})

	if err := refuseMovedElsewhere(a, seen, quiet, false); err == nil || err.Error() != "another location moved its status" {
		t.Errorf("local push = %v, want today's refusal", err)
	}
	if err := refuseMovedElsewhere(a, seen, quiet, true); err != nil {
		t.Errorf("node rows that move no status = %v, want them rebased", err)
	}
	err := refuseMovedElsewhere(a, seen, moves, true)
	if err == nil || !strings.Contains(err.Error(), "evt_mine") {
		t.Errorf("a node status move over a foreign one = %v, want it refused naming evt_mine", err)
	}
	if err := refuseMovedElsewhere(a, seen[:1], moves, true); err != nil {
		t.Errorf("a node status move with no foreign one = %v, want it rebased", err)
	}
}
