package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
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
	m := newMachineWithUI(t)
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
