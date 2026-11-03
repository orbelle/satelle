package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// setLocalChain replaces a story's local events with the given ones, each kept with
// the base it is given: the shapes an earlier pull left.
func setLocalChain(t *testing.T, id string, events []ledger.Entry) {
	t.Helper()
	withRepoStore(t, func(db *store.DB) {
		ctx := context.Background()
		cur, err := db.Ledger.StoryChain(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var gone []string
		for _, e := range cur {
			gone = append(gone, e.ID)
		}
		if len(gone) > 0 {
			if err := db.Ledger.RemoveEvents(ctx, id, gone, func(ledger.Entry) bool { return true }); err != nil {
				t.Fatal(err)
			}
		}
		for _, e := range events {
			if _, err := db.Ledger.Upsert(ctx, e, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// relinked is the events in the order given, each built on the one before and the
// first on none: a chain linked in the order its events arrived.
func relinked(events ...ledger.Entry) []ledger.Entry {
	out := make([]ledger.Entry, len(events))
	base := ""
	for i, e := range events {
		e.BaseID = base
		out[i] = e
		base = e.ID
	}
	return out
}

func spliced(parts ...[]ledger.Entry) []ledger.Entry {
	var out []ledger.Entry
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// chainShape is a chain's events and bases, in chain order.
func chainShape(chain []ledger.Entry) []string {
	var out []string
	for _, e := range chain {
		out = append(out, e.ID+"<-"+e.BaseID)
	}
	return out
}

func localWork(id string) ledger.Entry {
	return ledger.Entry{ID: "evt_localwork", StoryID: id, Kind: ledger.KindComment, Actor: "bob", Body: "local work"}
}

// forkAll has a pull that cannot read the hosted chains seed every story it pulls
// with a genesis of its own, as the pull that forked them did.
func forkAll(t *testing.T, ts *httptest.Server, f *fakeWorkstateServer) {
	t.Helper()
	f.chain.unreadable = true
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	f.chain.unreadable = false
}

var rechainLine = regexp.MustCompile(`(?m)^(rechained|would rechain|refused|would refuse|nothing to rechain) (\S+): (.*)$`)

// rechainOutcomes reads the one line each story gets, the dry run's wording and the
// real run's made the same.
func rechainOutcomes(t *testing.T, out string) map[string]string {
	t.Helper()
	norm := strings.NewReplacer("rechained", "rechain", "would rechain", "rechain", "would refuse", "refuse", "refused", "refuse", "nothing to rechain", "nothing")
	got := map[string]string{}
	for _, m := range rechainLine.FindAllStringSubmatch(out, -1) {
		if _, dup := got[m[2]]; dup {
			t.Fatalf("%s is reported twice:\n%s", m[2], out)
		}
		got[m[2]] = norm.Replace(m[1]) + ": " + m[3]
	}
	return got
}

func noAppendsAfterPush(t *testing.T, ts *httptest.Server, f *fakeWorkstateServer, id string) {
	t.Helper()
	before := len(f.chain.appendsFor(id))
	if out, err := pushRaw(t, ts.URL); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if n := len(f.chain.appendsFor(id)); n != before {
		t.Errorf("the push sent %d Append(s) for %s after rechain, want none", n-before, id)
	}
}

// sty_8fc70019 AC1: the seeded pair sits after the first hosted events and later
// hosted events are linked onto it; rechain repairs it all the same, the local chain
// becoming the hosted chain event for event.
func TestRechainRepairsMidChainSeed(t *testing.T) {
	ts, f, id, want, repo := forkedByOldPull(t)
	seeds := localChain(t, id)
	shape := spliced(want[:2], seeds, want[2:])
	setLocalChain(t, id, relinked(shape...))
	got := localChain(t, id)
	if !slices.Equal(idsOf(got), idsOf(shape)) || got[2+len(seeds)].BaseID != seeds[len(seeds)-1].ID || want[2].BaseID == seeds[len(seeds)-1].ID {
		t.Fatalf("fixture chain = %v, want later hosted events linked onto the seeded pair", chainShape(got))
	}

	out, err := runRoot(t, "sync", "workstate", "rechain", "--dry-run", "--server", ts.URL)
	if err != nil || !strings.Contains(out, "would rechain "+id) {
		t.Fatalf("rechain --dry-run = %v\n%s", err, out)
	}
	if !slices.Equal(chainShape(localChain(t, id)), chainShape(got)) {
		t.Fatal("--dry-run changed the chain")
	}
	out, err = runRoot(t, "sync", "workstate", "rechain", "--server", ts.URL)
	if err != nil || !strings.Contains(out, "rechained "+id) {
		t.Fatalf("rechain = %v\n%s", err, out)
	}
	sameChain(t, localChain(t, id), want)
	if g, w := chainShape(localChain(t, id)), chainShape(want); !slices.Equal(g, w) {
		t.Errorf("bases after rechain = %v, want %v", g, w)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts after rechain = %v, want none", c)
	}
	if head := storyHeads(t, ts.URL, "probe", repo)[id]; head != want[len(want)-1].ID {
		t.Errorf("head after rechain = %q, want the hosted latest %q", head, want[len(want)-1].ID)
	}
	noAppendsAfterPush(t, ts, f, id)
}

// sty_8fc70019 AC2: local work anywhere in the chain, not only at its end, refuses
// the story by name and leaves its chain as it was, whether run or dry-run.
func TestRechainRefusesMidChainLocalWork(t *testing.T) {
	ts, _, id, want, _ := forkedByOldPull(t)
	seeds := localChain(t, id)
	setLocalChain(t, id, relinked(spliced(want[:2], seeds, []ledger.Entry{localWork(id)}, want[2:])...))
	before := chainShape(localChain(t, id))

	for _, args := range [][]string{{"--dry-run"}, {}} {
		out, err := runRoot(t, append([]string{"sync", "workstate", "rechain", id, "--server", ts.URL}, args...)...)
		if err == nil || !strings.Contains(out, id+": local events the hosted chain lacks are not only a seeded genesis") || !strings.Contains(out, "evt_localwork (comment)") {
			t.Fatalf("rechain %v = %v\n%s, want the story refused by name with the event", args, err, out)
		}
		if got := chainShape(localChain(t, id)); !slices.Equal(got, before) {
			t.Errorf("rechain %v changed the chain: %v, want %v", args, got, before)
		}
	}
}

// reconciledFork is the sty_4be32144 shape: a story whose hosted chain folds to
// done, forked by an old pull (a seeded pair in the middle of its local chain) and
// then repaired by "story reconcile --repair", which left a status_reconcile to
// reconciledTo after the pair.
func reconciledFork(t *testing.T, reconciledTo string) (ts *httptest.Server, id string, want []ledger.Entry, repo string) {
	t.Helper()
	ts, f, ids, wants, repo := publishedStoriesWith(t, func(id string) []ledger.AppendInput {
		return []ledger.AppendInput{{StoryID: id, Kind: ledger.KindStatusTransition, Actor: "bob", Body: "backlog → done", Payload: json.RawMessage(`{"from":"backlog","to":"done"}`)}}
	}, "reconciled")
	forkAll(t, ts, f)
	id, want = ids[0], wants[ids[0]]
	r := reconcile("evt_reconcile", "status", reconciledTo)
	r.StoryID = id
	setLocalChain(t, id, relinked(spliced(want[:2], localChain(t, id), []ledger.Entry{r}, want[2:])...))
	if got := localChain(t, id); !slices.Contains(idsOf(got), r.ID) || len(got) <= len(want)+1 {
		t.Fatalf("fixture chain = %v, want the seeded pair and the reconcile among the hosted events", idsOf(got))
	}
	return ts, id, want, repo
}

// sty_7f977031 AC1, AC3: a local status_reconcile to the status the hosted chain
// folds to is discarded with the seeded pair; the dry run reports what the run does
// and changes nothing, and the story then pushes nothing.
func TestRechainDiscardsMatchingStatusReconcile(t *testing.T) {
	ts, id, want, repo := reconciledFork(t, "done")
	before := chainShape(localChain(t, id))

	dryOut, err := runRoot(t, "sync", "workstate", "rechain", id, "--dry-run", "--server", ts.URL)
	if err != nil || !strings.Contains(dryOut, "would rechain "+id) {
		t.Fatalf("rechain --dry-run = %v\n%s", err, dryOut)
	}
	if got := chainShape(localChain(t, id)); !slices.Equal(got, before) {
		t.Fatalf("--dry-run changed the chain: %v, want %v", got, before)
	}
	runOut, err := runRoot(t, "sync", "workstate", "rechain", id, "--server", ts.URL)
	if err != nil || !strings.Contains(runOut, "rechained "+id) || !strings.Contains(runOut, "1 reconcile") {
		t.Fatalf("rechain = %v\n%s, want the reconcile counted", err, runOut)
	}
	if dry, run := rechainOutcomes(t, dryOut), rechainOutcomes(t, runOut); dry[id] != run[id] {
		t.Errorf("dry run says %q, run says %q", dry[id], run[id])
	}
	sameChain(t, localChain(t, id), want)
	if g, w := chainShape(localChain(t, id)), chainShape(want); !slices.Equal(g, w) {
		t.Errorf("bases after rechain = %v, want %v", g, w)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Errorf("conflicts after rechain = %v, want none", c)
	}
}

// sty_7f977031 AC2, AC3: a local status_reconcile to a status the hosted chain does
// not fold to refuses the story by name with both statuses and leaves its chain as
// it was, whether run or dry-run, and each says the same.
func TestRechainRefusesMismatchedStatusReconcile(t *testing.T) {
	ts, id, _, _ := reconciledFork(t, "in_progress")
	before := chainShape(localChain(t, id))

	var reasons []string
	for _, args := range [][]string{{"--dry-run"}, {}} {
		out, err := runRoot(t, append([]string{"sync", "workstate", "rechain", id, "--server", ts.URL}, args...)...)
		reason := rechainOutcomes(t, out)[id]
		if err == nil || !strings.HasPrefix(reason, "refuse: ") || !strings.Contains(reason, "evt_reconcile sets status in_progress but the hosted chain folds to done") {
			t.Fatalf("rechain %v = %v\n%s, want the story refused naming both statuses", args, err, out)
		}
		reasons = append(reasons, reason)
		if got := chainShape(localChain(t, id)); !slices.Equal(got, before) {
			t.Errorf("rechain %v changed the chain: %v, want %v", args, got, before)
		}
	}
	if reasons[0] != reasons[1] {
		t.Errorf("dry run says %q, run says %q", reasons[0], reasons[1])
	}
}

// sty_8fc70019 AC6: a story whose local events are all the hosted ones, linked in
// another order and with its head in the middle, is found by comparison with the
// hosted chain though no conflict is recorded, and becomes the hosted chain.
func TestRechainRepairsReorderedChain(t *testing.T) {
	ts, f, id, want, repo := publishedElsewhere(t)
	if out, err := pullRaw(t, ts.URL); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	sameChain(t, localChain(t, id), want)
	setLocalChain(t, id, relinked(spliced([]ledger.Entry{want[1], want[0]}, want[2:])...))
	if err := hosted.SaveStoryHeads(ts.URL, "probe", repo, map[string]string{id: want[0].ID}); err != nil {
		t.Fatal(err)
	}
	if c := storyConflicts(t, repo); len(c) != 0 {
		t.Fatalf("conflicts = %v, want none recorded", c)
	}

	out, err := runRoot(t, "sync", "workstate", "rechain", "--server", ts.URL)
	if err != nil || !strings.HasPrefix(rechainOutcomes(t, out)[id], "rechain: ") {
		t.Fatalf("rechain = %v\n%s, want the reordered story rechained", err, out)
	}
	sameChain(t, localChain(t, id), want)
	if g, w := chainShape(localChain(t, id)), chainShape(want); !slices.Equal(g, w) {
		t.Errorf("bases after rechain = %v, want %v", g, w)
	}
	if head := storyHeads(t, ts.URL, "probe", repo)[id]; head != want[len(want)-1].ID {
		t.Errorf("head after rechain = %q, want the hosted latest %q", head, want[len(want)-1].ID)
	}
	noAppendsAfterPush(t, ts, f, id)
}

func ent(id, base, kind string) ledger.Entry {
	return ledger.Entry{ID: id, BaseID: base, StoryID: "sty_x", Kind: kind, Actor: "bob"}
}

func seedEvents(version, snapshot, base string) []ledger.Entry {
	v := ent(version, base, ledger.KindWorkflowVersion)
	v.Refs = json.RawMessage(`{"workflow_version":"h"}`)
	s := ent(snapshot, version, ledger.KindDefinitionSnapshot)
	s.Actor = "satelle"
	s.Refs = json.RawMessage(`{"definition_snapshot":"sty_x"}`)
	return []ledger.Entry{v, s}
}

// reconcile is the status_reconcile event "story reconcile --repair" appends: the
// one field it repaired and the chain value it rebuilt the row to.
func reconcile(id, field, chain string) ledger.Entry {
	e := ent(id, "", ledger.KindStatusReconcile)
	e.Actor = "reconcile"
	e.Payload = json.RawMessage(`{"fields":[{"field":"` + field + `","row":"in_progress","chain":"` + chain + `"}]}`)
	return e
}

func hev(id, base string) hosted.ChainEvent { return hosted.ChainEvent{ID: id, BaseID: base} }

func hostedOf(ids ...string) []hosted.ChainEvent {
	var out []hosted.ChainEvent
	base := ""
	for _, id := range ids {
		out = append(out, hev(id, base))
		base = id
	}
	return out
}

func hostedIDs(events []hosted.ChainEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func TestPlanRechain(t *testing.T) {
	a, b := ent("a", "", ledger.KindStoryCreated), ent("b", "a", ledger.KindComment)
	seeds := seedEvents("v", "s", "b")
	cancel := ent("t", "", ledger.KindStatusTransition)
	cancel.Payload = json.RawMessage(`{"from":"backlog","to":"cancelled"}`)
	tests := []struct {
		name         string
		local        []ledger.Entry
		hosted       []hosted.ChainEvent
		outcome      string
		remove, take []string
		replay       []string // checked when set
		seeded       int
		reconciled   int
		hostedStatus string // what the hosted chain folds to
		reason       string
	}{
		{
			// sty_7f977031 AC1: the seeded pair mid-chain, a status_reconcile to done
			// after it, and a hosted chain that folds to done.
			name:         "mid-chain seed and a status_reconcile to the hosted status",
			local:        relinked(spliced([]ledger.Entry{a, b}, seeds, []ledger.Entry{reconcile("r", "status", "done")}, []ledger.Entry{ent("c", "", ledger.KindComment), ent("d", "", ledger.KindComment)})...),
			hosted:       hostedOf("a", "b", "c", "d"),
			hostedStatus: "done",
			outcome:      rechainDo, remove: []string{"v", "s", "r", "c", "d"}, take: []string{"c", "d"}, seeded: 2, reconciled: 1,
		},
		{
			name:         "a status_reconcile to the hosted status alone",
			local:        relinked(a, b, reconcile("r", "status", "done")),
			hosted:       hostedOf("a", "b"),
			hostedStatus: "done",
			outcome:      rechainDo, remove: []string{"r"}, reconciled: 1,
		},
		{
			// sty_7f977031 AC2: a different status is refused with both.
			name:         "a status_reconcile to another status is refused with both",
			local:        relinked(spliced([]ledger.Entry{a, b}, seeds, []ledger.Entry{reconcile("r", "status", "in_progress")})...),
			hosted:       hostedOf("a", "b"),
			hostedStatus: "done",
			outcome:      rechainRefused, reason: "local status_reconcile r sets status in_progress but the hosted chain folds to done",
		},
		{
			name:         "a status_reconcile that repaired no status is local work",
			local:        relinked(a, b, reconcile("r", "title", "t")),
			hosted:       hostedOf("a", "b"),
			hostedStatus: "done",
			outcome:      rechainRefused, reason: "not only a seeded genesis: r (status_reconcile)",
		},
		{
			// The seeded pair sits after the first hosted events and a hosted event is
			// linked onto it, with a child whose base is the hosted one.
			name:    "mid-chain seed with a wrong-base hosted event and its child",
			local:   relinked(spliced([]ledger.Entry{a, b}, seeds, []ledger.Entry{ent("c", "", ledger.KindComment), ent("d", "", ledger.KindComment)})...),
			hosted:  hostedOf("a", "b", "c", "d"),
			outcome: rechainDo, remove: []string{"v", "s", "c", "d"}, take: []string{"c", "d"}, seeded: 2,
		},
		{
			name:    "head seed",
			local:   relinked(seedEvents("v", "s", "")...),
			hosted:  hostedOf("a", "b"),
			outcome: rechainDo, remove: []string{"v", "s"}, take: []string{"a", "b"}, seeded: 2,
		},
		{
			name:    "reordered, nothing local-only",
			local:   relinked(b, a, ent("c", "", ledger.KindComment)),
			hosted:  hostedOf("a", "b", "c"),
			outcome: rechainDo, remove: []string{"b", "a", "c"}, take: []string{"a", "b", "c"},
		},
		{
			name:    "a start of the hosted chain takes the rest",
			local:   []ledger.Entry{a, b},
			hosted:  hostedOf("a", "b", "c"),
			outcome: rechainDo, take: []string{"c"},
		},
		{
			name:    "already the hosted chain",
			local:   []ledger.Entry{a, b},
			hosted:  hostedOf("a", "b"),
			outcome: rechainNothing,
		},
		{
			name:    "local work is refused by name",
			local:   relinked(a, ent("w", "", ledger.KindComment), b),
			hosted:  hostedOf("a", "b"),
			outcome: rechainRefused, reason: "not only a seeded genesis: w (comment)",
		},
		{
			name:    "a workflow_version alone is no seed",
			local:   relinked(seedEvents("v", "s", "")[:1]...),
			hosted:  hostedOf("a"),
			outcome: rechainRefused, reason: "not only a seeded genesis: v (workflow_version)",
		},
		{
			name:    "a hosted chain that does not link",
			local:   []ledger.Entry{a},
			hosted:  []hosted.ChainEvent{hev("a", ""), hev("c", "nowhere")},
			outcome: rechainRefused, reason: "does not link at c",
		},
		{
			// sty_937fd6cc AC2, exe_317cdf90's shape: a local backlog → cancelled on a
			// root of its own over a hosted chain with no status event, which folds
			// to no status. The empty fold is the chain's start: the move replays.
			name:         "a status move over a hosted chain that folds to no status replays",
			local:        relinked(ent("w", "", ledger.KindComment), cancel),
			hosted:       hostedOf("a", "b"),
			hostedStatus: "",
			outcome:      rechainDo, take: []string{"a", "b"}, replay: []string{"w", "t"},
		},
		{
			// The guard still holds when the hosted chain folds to a status.
			name:         "a status move over a hosted chain that folds to another status is refused",
			local:        relinked(ent("w", "", ledger.KindComment), cancel),
			hosted:       hostedOf("a", "b"),
			hostedStatus: "plan",
			outcome:      rechainRefused, reason: "local status_transition t moves from backlog but the hosted chain folds to plan",
		},
		{
			name:    "a local chain that forks",
			local:   []ledger.Entry{a, b, ent("x", "a", ledger.KindComment)},
			hosted:  hostedOf("a", "b"),
			outcome: rechainRefused, reason: "forks at a",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planRechain(tc.local, tc.hosted, func() (string, error) { return tc.hostedStatus, nil })
			if got.outcome != tc.outcome || !strings.Contains(got.reason, tc.reason) {
				t.Fatalf("plan = %q %q, want %q containing %q", got.outcome, got.reason, tc.outcome, tc.reason)
			}
			if got.outcome == rechainRefused {
				return
			}
			if !slices.Equal(got.remove, tc.remove) || !slices.Equal(hostedIDs(got.take), tc.take) || got.seeded != tc.seeded || len(got.reconciled) != tc.reconciled {
				t.Errorf("remove %v take %v seeded %d reconciled %d, want %v %v %d %d", got.remove, hostedIDs(got.take), got.seeded, len(got.reconciled), tc.remove, tc.take, tc.seeded, tc.reconciled)
			}
			if tc.replay != nil && !slices.Equal(got.replay, tc.replay) {
				t.Errorf("replay %v, want %v", got.replay, tc.replay)
			}
			// Nothing that stays builds on an event that goes, so the ledger cannot
			// refuse the removal.
			gone := map[string]bool{}
			for _, id := range got.remove {
				gone[id] = true
			}
			for _, e := range tc.local {
				if !gone[e.ID] && gone[e.BaseID] {
					t.Errorf("%s stays but builds on a removed event %s", e.ID, e.BaseID)
				}
			}
		})
	}
}

func TestDivergent(t *testing.T) {
	a, b := ent("a", "", ledger.KindStoryCreated), ent("b", "a", ledger.KindComment)
	tests := []struct {
		name   string
		local  []ledger.Entry
		hosted []hosted.ChainEvent
		want   bool
	}{
		{"in sync", []ledger.Entry{a, b}, hostedOf("a", "b"), false},
		{"ahead of hosted", []ledger.Entry{a, b, ent("x", "b", ledger.KindComment)}, hostedOf("a", "b"), false},
		{"behind hosted", []ledger.Entry{a}, hostedOf("a", "b"), false},
		{"unpublished", []ledger.Entry{a, b}, nil, false},
		{"reordered", relinked(b, a), hostedOf("a", "b"), true},
		{"wrong base", []ledger.Entry{a, ent("b", "", ledger.KindComment)}, hostedOf("a", "b"), true},
		{"head seed", relinked(seedEvents("v", "s", "")...), hostedOf("a", "b"), true},
		{"mid-chain seed", relinked(spliced([]ledger.Entry{a}, seedEvents("v", "s", "a"), []ledger.Entry{b})...), hostedOf("a", "b"), true},
	}
	for _, tc := range tests {
		if got := divergent(tc.local, tc.hosted); got != tc.want {
			t.Errorf("%s: divergent = %v, want %v", tc.name, got, tc.want)
		}
	}
}
