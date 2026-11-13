package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

// boundPair is two machines with their own satelled, bound to one project on
// srv (sto_5c706973).
func boundPair(t *testing.T, srv *fakeserver.Server) (a, b machine) {
	t.Helper()
	a = newMachineWithUI(t)
	runOn(t, a, "project", "create", "shared", "--bind", "--server", srv.URL)
	b = newMachineWithUI(t)
	runOn(t, b, "project", "bind", "shared", "--server", srv.URL)
	return a, b
}

// autosync runs one automatic pass on m. Only the verb's own flush reaches
// m's satelled: nothing else is flushed.
func autosync(t *testing.T, m machine) string {
	t.Helper()
	m.use(t)
	return must(t, "ledger", "autosync")
}

// serverItem is the summary satelle.dev lists for id in project "shared".
func serverItem(t *testing.T, srv *fakeserver.Server, id string) (title, status string, ok bool) {
	t.Helper()
	for _, it := range srv.Items("shared") {
		if it.GetId() == id {
			return it.GetTitle(), it.GetStatus(), true
		}
	}
	return "", "", false
}

func storyOn(t *testing.T, m machine, id string) item.Item {
	t.Helper()
	m.use(t)
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

// syncStateOf is the recorded sync outcome of the current machine's repo.
func syncStateOf(t *testing.T) syncstate.State {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := syncstate.Load(config.GlobalDir(), uiRepoRoot(r))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// AC1: a create, an update and a move on A reach satelle.dev at A's pass and
// B's CLI and satelled at B's pass, with no replication command run.
func TestAutosyncCarriesCreateUpdateMove(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a, b := boundPair(t, srv)

	var created item.Item
	if err := json.Unmarshal([]byte(runOn(t, a, "story", "create", "--title", "made on a", "--criteria", "travels", "--json")), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ID

	steps := []struct {
		name  string
		args  []string
		title string
		check func(it item.Item, srvTitle, srvStatus string) bool
	}{
		{"create", nil, "made on a", func(it item.Item, st, _ string) bool { return it.Title == "made on a" && st == "made on a" }},
		{"update", []string{"story", "set", id, "--title", "renamed on a"}, "renamed on a",
			func(it item.Item, st, _ string) bool { return it.Title == "renamed on a" && st == "renamed on a" }},
		{"move", []string{"story", "set", id, "--status", "done"}, "renamed on a",
			func(it item.Item, _, ss string) bool { return it.Status == "done" && ss == "done" }},
	}
	for _, step := range steps {
		if step.args != nil {
			runOn(t, a, step.args...)
		}
		before := ""
		if step.name != "create" {
			before = storiesOn(t, b)
		}
		autosync(t, a)
		srvTitle, srvStatus, ok := serverItem(t, srv, id)
		if !ok {
			t.Fatalf("%s: satelle.dev does not list %s after A's pass", step.name, id)
		}
		if step.name == "create" {
			if strings.Contains(storiesOn(t, b), "made on a") {
				t.Fatalf("create: B's satelled shows the story before B's pass")
			}
		} else if after := storiesOn(t, b); after != before {
			t.Fatalf("%s: B's satelled changed before B's pass", step.name)
		}
		switch step.name {
		case "update":
			if !strings.Contains(before, "made on a") || strings.Contains(before, "renamed on a") {
				t.Fatalf("update: B's satelled shows the new title before B's pass:\n%s", before)
			}
		case "move":
			if strings.Contains(before, `data-status="done"`) {
				t.Fatalf("move: B's satelled shows the move before B's pass:\n%s", before)
			}
		}
		autosync(t, b)
		it := storyOn(t, b, id)
		if !step.check(it, srvTitle, srvStatus) {
			t.Fatalf("%s: B holds %q/%s, satelle.dev %q/%s", step.name, it.Title, it.Status, srvTitle, srvStatus)
		}
		b.use(t)
		if list := must(t, "story", "list"); !strings.Contains(list, id) {
			t.Fatalf("%s: story list on B lacks %s:\n%s", step.name, id, list)
		}
		frag := storiesOn(t, b)
		if !strings.Contains(frag, step.title) || !strings.Contains(frag, id) {
			t.Fatalf("%s: B's satelled lacks the story after B's pass:\n%s", step.name, frag)
		}
		if step.name == "move" && !strings.Contains(frag, `data-status="done"`) {
			t.Fatalf("move: B's satelled does not show the story done:\n%s", frag)
		}
	}
}

// AC2: a write made while satelle.dev is down is kept and recorded as a
// failing sync, then pushed by the next pass once it is up again.
func TestAutosyncRecoversAfterOutage(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	srv.SetDown(true)
	id := createID(t, "story", "create", "--title", "made offline", "--criteria", "kept")
	if out, err := run(t, "", "ledger", "autosync"); err == nil {
		t.Fatalf("autosync with the store down exited 0:\n%s", out)
	}
	if out := must(t, "story", "get", id); !strings.Contains(out, "made offline") {
		t.Fatalf("the offline write is gone:\n%s", out)
	}
	failed := syncStateOf(t)
	if !strings.Contains(failed.SyncReason, "Unavailable") || failed.SyncLastFailure.IsZero() {
		t.Fatalf("recorded state after the outage: %+v", failed)
	}

	srv.SetDown(false)
	must(t, "ledger", "autosync")
	if _, _, ok := serverItem(t, srv, id); !ok {
		t.Fatalf("satelle.dev lacks %s after the store came back", id)
	}
	ok := syncStateOf(t)
	if ok.SyncReason != "" || !ok.SyncLastSuccess.After(failed.SyncLastFailure) || !strings.HasPrefix(ok.SyncStore, "satelle.dev project ") {
		t.Fatalf("recorded state after the recovery: %+v", ok)
	}
}

func storyIDs(t *testing.T, m machine) []string {
	t.Helper()
	m.use(t)
	var items []item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "list", "--json")), &items); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	slices.Sort(ids)
	return ids
}

// AC3: two machines that each made a story the other never saw converge on
// their next passes: A, B, A.
func TestAutosyncTwoMachinesDisjointStories(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a, b := boundPair(t, srv)
	runOn(t, a, "story", "create", "--title", "only on a", "--criteria", "c")
	runOn(t, b, "story", "create", "--title", "only on b", "--criteria", "c")
	autosync(t, a)
	if st := syncStateOf(t); st.SyncPushed != 1 || st.SyncPulled != 0 {
		t.Errorf("A's first pass recorded pushed %d, pulled %d; want 1, 0", st.SyncPushed, st.SyncPulled)
	}
	autosync(t, b)
	if st := syncStateOf(t); st.SyncPushed != 1 || st.SyncPulled != 1 {
		t.Errorf("B's pass recorded pushed %d, pulled %d; want 1, 1", st.SyncPushed, st.SyncPulled)
	}
	autosync(t, a)

	onA, onB := storyIDs(t, a), storyIDs(t, b)
	var onServer []string
	for _, it := range srv.Items("shared") {
		onServer = append(onServer, it.GetId())
	}
	slices.Sort(onServer)
	if len(onA) != 2 || !slices.Equal(onA, onB) || !slices.Equal(onA, onServer) {
		t.Fatalf("A %v, B %v, satelle.dev %v", onA, onB, onServer)
	}
	for _, m := range []machine{a, b} {
		frag := storiesOn(t, m)
		if !strings.Contains(frag, "only on a") || !strings.Contains(frag, "only on b") {
			t.Fatalf("a satelled lacks a story:\n%s", frag)
		}
	}
}

// AC4: a fresh machine whose queue file adopts a chain the store holds pulls
// that chain instead of starting its own, and mirrors the file onto it
// without a fork on either machine.
func TestAutosyncAdoptedQueueDoesNotFork(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	que := createID(t, "queue", "create", "--title", "release")
	sto := createID(t, "story", "create", "--title", "s", "--criteria", "c")
	must(t, "queue", "add", que, sto)
	autosync(t, a)

	b := newMachine(t)
	must(t, "project", "bind", "shared", "--server", srv.URL)
	file := filepath.Join(b.dir, ".satelle", "queues", "release.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("id = \""+que+"\"\ntitle = \"Release queue\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		autosync(t, b)
		if st := syncStateOf(t); st.SyncReason != "" {
			t.Fatalf("B's pass %d: %s", pass, st.SyncReason)
		}
	}
	held := runtimeLedger(t)
	events, err := held.Read(que)
	if err != nil || len(events) < 3 || events[len(events)-1].Actor != "satelle" {
		t.Fatalf("B's queue chain: %d events, %v; want A's chain and the file's mirror", len(events), err)
	}
	if n := len(srv.Events("shared", que)); n != len(events) {
		t.Fatalf("satelle.dev holds %d events of %s, B %d", n, que, len(events))
	}

	autosync(t, a)
	if st := syncStateOf(t); st.SyncReason != "" {
		t.Fatalf("A's pass: %s", st.SyncReason)
	}
	if got, _ := runtimeLedger(t).Read(que); len(got) != len(events) || got[len(got)-1].Hash != events[len(events)-1].Hash {
		t.Fatalf("A holds %d events of %s, B %d", len(got), que, len(events))
	}
}

// localWithUI is a machine with its own satelled whose repo is added with
// satelle project add (its store is local), which seeds the mirror and its
// snapshot hash. It returns the machine and the partition's repo_key.
func localWithUI(t *testing.T) (machine, string) {
	t.Helper()
	return localOn(t, newMachineWithUI)
}

// localWithHomeUI is localWithUI whose satelled serves this home's own mirror
// database, the one the pass's partition prune reads.
func localWithHomeUI(t *testing.T) (machine, string) {
	t.Helper()
	return localOn(t, newMachineWithHomeUI)
}

// localOn builds the machine with newM and adds its repo with satelle project
// add, returning the machine and the partition's repo_key.
func localOn(t *testing.T, newM func(*testing.T) machine) (machine, string) {
	t.Helper()
	m := newM(t)
	m.use(t)
	must(t, "project", "add")
	parts, err := m.ms.ListPartitions(context.Background())
	if err != nil || len(parts) != 1 {
		t.Fatalf("satelled partitions after project add = %d, %v; want 1", len(parts), err)
	}
	return m, parts[0].RepoKey
}

// partitionOn is m's satelled's partition key.
func partitionOn(t *testing.T, m machine, key string) mirror.Partition {
	t.Helper()
	p, ok, err := m.ms.GetPartition(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("partition %s: ok=%v err=%v", key, ok, err)
	}
	return p
}

// agePartition backdates key's last confirmation past StaleAfter.
func agePartition(t *testing.T, m machine, key string) {
	t.Helper()
	if err := m.ms.MarkFresh(context.Background(), key, time.Now().Add(-mirror.StaleAfter-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !partitionOn(t, m, key).Stale(time.Now()) {
		t.Fatal("setup: the aged partition must read stale")
	}
}

// closedEndpoint is the URL of a satelled that is no longer answering.
func closedEndpoint(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

// AC3 (sto_128803b3): a quiet repo with the timer passing stays fresh. Each
// pass confirms the mirror with only a digest: the partition reads fresh,
// seq does not move (no snapshot applied), and the recorded snapshot success
// moves. The project page carries no stale marker.
func TestAutosyncConfirmsQuietMirror(t *testing.T) {
	m, key := localWithUI(t)
	for i := range 3 {
		agePartition(t, m, key)
		before := partitionOn(t, m, key)
		prev := syncStateOf(t).SnapshotLastSuccess
		time.Sleep(2 * time.Millisecond) // time-subject: timestamp separation, so the recorded success visibly moves
		autosync(t, m)
		after := partitionOn(t, m, key)
		if after.Stale(time.Now()) {
			t.Fatalf("pass %d: a quiet repo reads stale after a pass", i)
		}
		if after.Seq != before.Seq {
			t.Fatalf("pass %d: a quiet repo's confirm applied a snapshot: seq %d → %d", i, before.Seq, after.Seq)
		}
		if st := syncStateOf(t); !st.SnapshotLastSuccess.After(prev) || st.SnapshotReason != "" {
			t.Fatalf("pass %d: recorded snapshot state %+v, want a success after %s", i, st, prev)
		}
	}
	resp, err := http.Get(m.ui + "/r/" + partitionOn(t, m, key).Slug + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(page), `class="stale-flag"`) {
		t.Errorf("a confirmed project page carries the stale marker:\n%s", page)
	}
}

// AC1 (sto_128803b3): a push a command dropped (satelled not answering) is
// repaired by the next pass: the digest differs, so the full snapshot is sent.
func TestAutosyncRepairsADroppedPush(t *testing.T) {
	m, key := localWithUI(t)
	t.Setenv("SATELLE_SERVER_ENDPOINT", closedEndpoint(t))
	id := createID(t, "story", "create", "--title", "dropped push", "--criteria", "repaired")
	flushUI(context.Background())
	if strings.Contains(storiesOn(t, m), id) {
		t.Fatal("setup: the story reached satelled although its push was dropped")
	}
	before := partitionOn(t, m, key)
	autosync(t, m)
	if frag := storiesOn(t, m); !strings.Contains(frag, id) || !strings.Contains(frag, "dropped push") {
		t.Fatalf("the pass did not repair the dropped push:\n%s", frag)
	}
	after := partitionOn(t, m, key)
	if after.Seq == before.Seq {
		t.Error("repairing a dropped push must apply a snapshot")
	}
	if after.Stale(time.Now()) {
		t.Error("a repaired partition reads stale")
	}
}

// AC3 (sto_128803b3): a pass that finds satelled not answering confirms
// nothing: the partition stays stale and unchanged, and no snapshot outcome
// is recorded.
func TestAutosyncSatelledDownConfirmsNothing(t *testing.T) {
	m, key := localWithUI(t)
	agePartition(t, m, key)
	before := partitionOn(t, m, key)
	st := syncStateOf(t)
	t.Setenv("SATELLE_SERVER_ENDPOINT", closedEndpoint(t))
	must(t, "ledger", "autosync")
	after := partitionOn(t, m, key)
	if after.UpdatedAt != before.UpdatedAt || after.Seq != before.Seq {
		t.Errorf("a pass with satelled down changed the partition: %+v → %+v", before, after)
	}
	if !after.Stale(time.Now()) {
		t.Error("a partition no pass confirmed must still read stale")
	}
	got := syncStateOf(t)
	if !got.SnapshotLastSuccess.Equal(st.SnapshotLastSuccess) || !got.SnapshotLastFailure.Equal(st.SnapshotLastFailure) || got.SnapshotReason != st.SnapshotReason {
		t.Errorf("a pass with satelled down recorded a snapshot outcome: %+v → %+v", st, got)
	}
}

// releaseFile is the queue file the adoption tests give a machine.
const releaseFile = "title = \"Release\"\n[filter]\nkind = \"story\"\n"

// legacyRelease appends a que_release genesis straight to the current
// machine's runtime ledger, as a queue made before file genesis was
// deterministic: a random id, the wall clock, title "old" (sto_80da4589).
func legacyRelease(t *testing.T) ledger.Event {
	t.Helper()
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	qk, title := item.QueueKind, "old"
	wf, start, err := k.WorkflowFor(item.Item{Kind: qk, Title: "release"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := k.Ledger.Append("que_release", item.KindCreated, "satelle", item.Fields{Kind: &qk, Workflow: &wf, Status: &start, Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// queueFileOn writes .satelle/queues/release.toml in m's repo.
func queueFileOn(t *testing.T, m machine, body string) {
	t.Helper()
	file := filepath.Join(m.dir, ".satelle", "queues", "release.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// releaseChain is the current machine's que_release chain.
func releaseChain(t *testing.T) []ledger.Event {
	t.Helper()
	events, err := runtimeLedger(t).Read("que_release")
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// releaseStatus is the que_release row of the current machine's ledger
// status: its local and store counts and its state.
func releaseStatus(t *testing.T) (local, store, state string) {
	t.Helper()
	out := must(t, "ledger", "status")
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 4 && f[0] == "que_release" {
			return f[1], f[2], strings.Join(f[3:], " ")
		}
	}
	t.Fatalf("ledger status has no que_release row:\n%s", out)
	return "", "", ""
}

// mirrorOnB binds a fresh machine B to shared, gives it body as the release
// queue file and lets one command write the file's genesis; it returns B and
// the chain B wrote, every event a mirror event by satelle.
func mirrorOnB(t *testing.T, srv *fakeserver.Server, bodies ...string) (machine, []ledger.Event) {
	t.Helper()
	b := newMachine(t)
	must(t, "project", "bind", "shared", "--server", srv.URL)
	for _, body := range bodies {
		queueFileOn(t, b, body)
		must(t, "queue", "show", "release")
	}
	events := releaseChain(t)
	if len(events) != len(bodies) {
		t.Fatalf("B wrote %d que_release events, want %d", len(events), len(bodies))
	}
	for _, e := range events {
		if e.Actor != "satelle" || !strings.Contains(string(e.Payload), `"cause":"queue file .satelle/queues/release.toml"`) {
			t.Fatalf("B's event %s by %s is not a file mirror: %s", e.Kind, e.Actor, e.Payload)
		}
	}
	return b, events
}

// adoptsA checks one pass on B took A's whole chain (aChain), none of B's own
// events remain, and B's chain ends with exactly one mirror event of the file
// (Release, filtered), pushed in the pass; ledger status reads in sync.
func adoptsA(t *testing.T, srv *fakeserver.Server, b machine, aChain, own []ledger.Event) {
	t.Helper()
	b.use(t)
	out := autosync(t, b)
	if st := syncStateOf(t); st.SyncReason != "" {
		t.Fatalf("B's pass: %s\n%s", st.SyncReason, out)
	}
	got := releaseChain(t)
	if len(got) != len(aChain)+1 {
		t.Fatalf("B holds %d events, want A's %d and one mirror event", len(got), len(aChain))
	}
	for i, e := range aChain {
		if got[i].Hash != e.Hash {
			t.Fatalf("B's event %d is %s, the store's %s", i, got[i].Hash, e.Hash)
		}
	}
	for _, mine := range own {
		for _, e := range got {
			if e.Hash == mine.Hash {
				t.Fatalf("B's own event %s survived the adoption", mine.Hash)
			}
		}
	}
	last := got[len(got)-1]
	var p struct {
		Title string `json:"title"`
		Cause string `json:"cause"`
	}
	if err := json.Unmarshal(last.Payload, &p); err != nil || last.Kind != item.KindUpdated || last.Actor != "satelle" ||
		p.Cause != "queue file .satelle/queues/release.toml" || p.Title != "Release" {
		t.Fatalf("B's last event %s by %s: %s", last.Kind, last.Actor, last.Payload)
	}
	if n := len(srv.Events("shared", "que_release")); n != len(got) {
		t.Fatalf("satelle.dev holds %d events of que_release, B %d: the mirror event was not pushed in the pass", n, len(got))
	}
	if _, _, state := releaseStatus(t); state != "in sync" {
		t.Fatalf("B's que_release is %s, want in sync", state)
	}
}

// legacyA is machine A on project shared holding a legacy que_release with
// one story queued, synced; it returns A, its story and its chain.
func legacyA(t *testing.T, srv *fakeserver.Server) (machine, string, []ledger.Event) {
	t.Helper()
	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	legacyRelease(t)
	sto := createID(t, "story", "create", "--title", "from a", "--criteria", "c")
	must(t, "queue", "add", "que_release", sto)
	autosync(t, a)
	return a, sto, releaseChain(t)
}

// AC1, AC2 (sto_80da4589): a fresh runtime whose queue file wrote que_release
// before its first pull takes the store's chain at its first pass, mirrors
// the file onto it as one item.updated and pushes it in the same pass.
func TestAutosyncFileQueueAdoptsStoreChain(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a, sto, aChain := legacyA(t, srv)
	b, own := mirrorOnB(t, srv, releaseFile)
	adoptsA(t, srv, b, aChain, own)
	if out := must(t, "queue", "show", "release"); !strings.Contains(out, sto) {
		t.Fatalf("B's queue show lacks A's story %s:\n%s", sto, out)
	}
	if out := autosync(t, b); !strings.Contains(out, "pushed 0 event(s) on 0 item(s), pulled 0 event(s) on 0 item(s)") {
		t.Fatalf("B's second pass wrote:\n%s", out)
	}
	bHead := releaseChain(t)
	autosync(t, a)
	if st := syncStateOf(t); st.SyncReason != "" {
		t.Fatalf("A's pass: %s", st.SyncReason)
	}
	if got := releaseChain(t); len(got) != len(bHead) || got[len(got)-1].Hash != bHead[len(bHead)-1].Hash {
		t.Fatalf("A holds %d events of que_release, B %d", len(got), len(bHead))
	}
}

// AC1 (sto_80da4589): a local chain of a genesis and a mirror item.updated,
// both the file's, is adopted the same way.
func TestAutosyncFileQueueAdoptsOverSeveralMirrorEvents(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	_, _, aChain := legacyA(t, srv)
	b, own := mirrorOnB(t, srv, "title = \"Draft\"\n", releaseFile)
	if own[0].Kind != item.KindCreated || own[1].Kind != item.KindUpdated {
		t.Fatalf("B's chain is %s, %s", own[0].Kind, own[1].Kind)
	}
	adoptsA(t, srv, b, aChain, own)
}

// AC2 (sto_80da4589): when the adopted chain already carries the file's
// definition, nothing is appended and the pass pushes nothing.
func TestAutosyncFileQueueAdoptsMatchingChainAppendsNothing(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	legacyRelease(t)
	queueFileOn(t, a, releaseFile)
	must(t, "queue", "show", "release") // A's open mirrors the file onto the legacy genesis
	autosync(t, a)
	aChain := releaseChain(t)
	if len(aChain) != 2 {
		t.Fatalf("A holds %d que_release events, want the legacy genesis and one mirror", len(aChain))
	}
	stored := len(srv.Events("shared", "que_release"))

	b, _ := mirrorOnB(t, srv, releaseFile)
	out := autosync(t, b)
	if st := syncStateOf(t); st.SyncReason != "" {
		t.Fatalf("B's pass: %s\n%s", st.SyncReason, out)
	}
	if !strings.Contains(out, "pushed 0 event(s) on 0 item(s)") {
		t.Fatalf("B's pass pushed:\n%s", out)
	}
	got := releaseChain(t)
	if len(got) != len(aChain) {
		t.Fatalf("B holds %d events, A %d", len(got), len(aChain))
	}
	for i := range aChain {
		if got[i].Hash != aChain[i].Hash {
			t.Fatalf("B's event %d is not A's", i)
		}
	}
	if n := len(srv.Events("shared", "que_release")); n != stored {
		t.Fatalf("satelle.dev holds %d events of que_release, was %d", n, stored)
	}
	if _, _, state := releaseStatus(t); state != "in sync" {
		t.Fatalf("B's que_release is %s", state)
	}
}

// AC3 (sto_80da4589): satelle-server's observed shape, a local que_release of
// one file-mirror event against a store chain of 22 with another genesis,
// reads in sync with 22 events after one autosync pass and no other command.
func TestAutosyncReleaseQueueShapeSyncsUnattended(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	legacyRelease(t)
	queueFileOn(t, a, releaseFile)
	ids := []string{"queue", "add", "que_release"}
	for i := range 20 {
		ids = append(ids, createID(t, "story", "create", "--title", "s"+strconv.Itoa(i), "--criteria", "c"))
	}
	must(t, ids...)
	aChain := releaseChain(t)
	if len(aChain) != 22 {
		t.Fatalf("A holds %d que_release events, want 22", len(aChain))
	}
	autosync(t, a)

	b, _ := mirrorOnB(t, srv, releaseFile)
	if local, store, state := releaseStatus(t); local != "1" || store != "22" || state != "diverged at 0" {
		t.Fatalf("B's que_release before the pass: %s %s %s, want 1 22 diverged at 0", local, store, state)
	}
	autosync(t, b)
	if st := syncStateOf(t); st.SyncReason != "" {
		t.Fatalf("B's pass: %s", st.SyncReason)
	}
	got := releaseChain(t)
	if len(got) != 22 || got[21].Hash != aChain[21].Hash {
		t.Fatalf("B holds %d events, want A's 22", len(got))
	}
	if local, store, state := releaseStatus(t); local != "22" || store != "22" || state != "in sync" {
		t.Fatalf("B's que_release after the pass: %s %s %s", local, store, state)
	}
}

// AC4 (sto_80da4589): a file-defined chain that holds any other event (here a
// queue add on B) is never dropped: the pass leaves its bytes and the store
// as they were and names the divergence.
func TestAutosyncNeverDropsWorkedFileQueue(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	_, _, aChain := legacyA(t, srv)
	b, _ := mirrorOnB(t, srv, releaseFile)
	mine := createID(t, "story", "create", "--title", "on b", "--criteria", "c")
	must(t, "queue", "add", "release", mine)
	if got := releaseChain(t); len(got) != 2 || got[1].Kind != item.KindQueueAdded {
		t.Fatalf("B's chain is %d events", len(got))
	}
	path := runtimeLedger(t).Path("que_release")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b.use(t)
	if _, err := run(t, "", "ledger", "autosync"); err == nil {
		t.Fatal("autosync of a worked fork succeeded")
	}
	if st := syncStateOf(t); !strings.Contains(st.SyncReason, "que_release (diverged at 0)") {
		t.Fatalf("B's pass reason: %q", st.SyncReason)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("B's worked que_release chain changed")
	}
	if n := len(srv.Events("shared", "que_release")); n != len(aChain) {
		t.Fatalf("satelle.dev holds %d events of que_release, A pushed %d", n, len(aChain))
	}
}

// seedPartition gives m's satelled the partition key, with an identity whose
// recorded repo_root is root ("" records no identity), as a push would.
func seedPartition(t *testing.T, m machine, key, root string) {
	t.Helper()
	ctx, now := context.Background(), time.Now()
	if _, err := m.ms.TouchPartition(ctx, key, key, now); err != nil {
		t.Fatal(err)
	}
	if root == "" {
		return
	}
	if err := m.ms.UpsertItem(ctx, key, "identity", "meta", mirror.IdentityMeta{ProjectName: key, RepoRoot: root}, now); err != nil {
		t.Fatal(err)
	}
}

// hasPartition reports whether m's satelled holds the partition key.
func hasPartition(t *testing.T, m machine, key string) bool {
	t.Helper()
	_, ok, err := m.ms.GetPartition(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// goneProject registers a temp project named 003 with satelle project add and
// then deletes its directory, as a test's temp project is.
func goneProject(t *testing.T) string {
	t.Helper()
	gone := filepath.Join(t.TempDir(), "003")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	must(t, "project", "add", gone)
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(gc.Projects.Paths, gone) {
		t.Fatalf("setup: %s not in [projects] paths %v", gone, gc.Projects.Paths)
	}
	return gone
}

// AC1 (sto_e3c6bfb6): a registered project whose path no longer exists is
// pruned by the automatic pass, from [projects] paths in config.toml and from
// this machine's satelled; every entry and partition whose path exists, or
// that records no path, is kept.
func TestAutosyncPrunesGoneProject(t *testing.T) {
	m, key := localWithHomeUI(t)
	gc, err := config.LoadGlobal()
	if err != nil || len(gc.Projects.Paths) != 1 {
		t.Fatalf("setup: [projects] paths %v, %v; want the machine's repo", gc.Projects.Paths, err)
	}
	repo := gc.Projects.Paths[0]
	gone := goneProject(t)
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedPartition(t, m, "rk-gone", gone)
	seedPartition(t, m, "rk-file", file)
	seedPartition(t, m, "rk-norecord", "")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	out := autosync(t, m)

	gc, err = config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(gc.Projects.Paths, gone) || !slices.Contains(gc.Projects.Paths, repo) {
		t.Errorf("[projects] paths after the pass = %v; want %s gone and %s kept", gc.Projects.Paths, gone, repo)
	}
	raw, err := os.ReadFile(config.GlobalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), gone) {
		t.Errorf("config.toml still names %s:\n%s", gone, raw)
	}
	if list := must(t, "project", "list"); strings.Contains(list, gone) {
		t.Errorf("project list still shows %s:\n%s", gone, list)
	}
	if !strings.Contains(out, gone) || !strings.Contains(out, "removed from this machine's projects") {
		t.Errorf("the pass did not report the removal:\n%s", out)
	}
	if strings.Contains(out, "skipped: no such directory") {
		t.Errorf("the pass still visited the pruned path:\n%s", out)
	}

	if hasPartition(t, m, "rk-gone") {
		t.Error("partition rk-gone, whose path is gone, survived the pass")
	}
	if !strings.Contains(out, "rk-gone") || !strings.Contains(out, "pruned") {
		t.Errorf("the pass did not report the partition prune:\n%s", out)
	}
	for _, k := range []string{key, "rk-file", "rk-norecord"} {
		if !hasPartition(t, m, k) {
			t.Errorf("partition %s, whose path exists or is not recorded, was pruned", k)
		}
	}
}

// AC1 (sto_e3c6bfb6): a pass whose satelled is not answering, or whose push
// is disabled, removes no partition (no direct-database fallback); the
// registry prune does not depend on satelled.
func TestAutosyncSatelledDownPrunesNoPartition(t *testing.T) {
	m, _ := localWithHomeUI(t)
	gone := goneProject(t)
	seedPartition(t, m, "rk-gone", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SATELLE_SERVER_ENDPOINT", closedEndpoint(t))
	out := must(t, "ledger", "autosync")
	if !hasPartition(t, m, "rk-gone") {
		t.Fatal("a pass with satelled not answering removed a partition")
	}
	if !strings.Contains(out, "partitions not pruned") {
		t.Errorf("the pass did not report the failed partition prune:\n%s", out)
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(gc.Projects.Paths, gone) {
		t.Errorf("[projects] paths %v still holds %s with satelled down", gc.Projects.Paths, gone)
	}

	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	must(t, "ledger", "autosync")
	if !hasPartition(t, m, "rk-gone") {
		t.Fatal("a pass with the push disabled removed a partition")
	}
}

// AC1 (sto_e3c6bfb6): a pass with no gone project keeps every entry and does
// not rewrite config.toml.
func TestAutosyncKeepsExistingProjects(t *testing.T) {
	m := newMachine(t)
	other := t.TempDir()
	must(t, "project", "add", m.dir)
	must(t, "project", "add", other)
	path := config.GlobalConfigPath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // time-subject: a rewrite would visibly move the modification time

	must(t, "ledger", "autosync")

	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{m.dir, other} {
		if !slices.Contains(gc.Projects.Paths, p) {
			t.Errorf("[projects] paths %v lost the existing %s", gc.Projects.Paths, p)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !stAfter.ModTime().Equal(stBefore.ModTime()) {
		t.Errorf("a pass with nothing to prune rewrote config.toml:\n%s\n→\n%s", before, after)
	}
}

// AC1 (sto_e3c6bfb6): the pass's partition read creates no mirror database
// when this home has none.
func TestAutosyncCreatesNoMirror(t *testing.T) {
	newMachine(t)
	t.Setenv("SATELLE_SERVER_ENDPOINT", closedEndpoint(t))
	must(t, "project", "add")
	must(t, "ledger", "autosync")
	if _, err := os.Stat(mirror.DefaultPath(config.GlobalDir())); !os.IsNotExist(err) {
		t.Errorf("the pass created %s (stat err %v)", mirror.DefaultPath(config.GlobalDir()), err)
	}
}

// AC1 (sto_e3c6bfb6): only a missing absolute path is gone.
func TestProjectPathGone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, path string
		want       bool
	}{
		{"missing absolute path", filepath.Join(dir, "missing"), true},
		{"existing directory", dir, false},
		{"existing file", file, false},
		{"empty string", "", false},
		{"missing relative path", "no-such-relative-dir", false},
	} {
		if got := projectPathGone(c.path); got != c.want {
			t.Errorf("%s: projectPathGone(%q) = %v, want %v", c.name, c.path, got, c.want)
		}
	}
}
