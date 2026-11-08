//go:build !windows

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func fireCalls(srv *fakeserver.Server) int { return len(calledAny(srv, 0, "RemoteService/FireRemote")) }

// remote fire pushes only the fired item's chain to the bound project,
// whatever [ledger] store names.
func TestRemoteFirePushesOnlyTheFiredItem(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)
	calls := recordingStore(t, m.dir)
	id1 := createID(t, "story", "create", "--title", "fire me")
	id2 := createID(t, "story", "create", "--title", "stay here")
	out := must(t, "remote", "fire", "ci", id1)
	if !strings.Contains(out, "fired ci for "+id1+": run") {
		t.Fatalf("fire = %q", out)
	}
	if len(srv.Events("acme", id1)) == 0 || len(srv.Events("acme", id2)) != 0 {
		t.Fatalf("server holds %d event(s) of the fired item and %d of the other, want some and none",
			len(srv.Events("acme", id1)), len(srv.Events("acme", id2)))
	}
	if fireCalls(srv) != 1 {
		t.Fatal("FireRemote was not called")
	}
	noStoreCall(t, calls)
}

// A fired item that diverged from the project is refused with exit 1, and
// nothing is fired.
func TestRemoteFireRefusesADivergedItem(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a := newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "base", "--criteria", "c")
	must(t, "ledger", "push")
	b := newMachine(t)
	must(t, "project", "bind", "acme", "--server", srv.URL)
	must(t, "ledger", "pull")
	must(t, "story", "set", id, "--title", "b's title")
	must(t, "ledger", "push")
	a.use(t)
	must(t, "story", "set", id, "--title", "a's title")
	out, err := run(t, "", "remote", "fire", "ci", id)
	if ExitCodeOf(err) != 1 || !strings.Contains(out, "diverged: "+id+" at event") ||
		!strings.Contains(err.Error(), "remote fire: "+id+" diverged from the project") || !strings.Contains(err.Error(), "satelle ledger pull "+id) {
		t.Fatalf("fire of a diverged item: %v\n%s", err, out)
	}
	if fireCalls(srv) != 0 {
		t.Fatal("FireRemote was called for a diverged item")
	}
	if ev := srv.Events("acme", id); len(ev) != 2 {
		t.Errorf("server holds %d events, want b's 2", len(ev))
	}
	_ = b
}

// holeObjects is a store holding event 1 of one item and not event 0.
type holeObjects struct{ key string }

func (h holeObjects) PutIfAbsent(context.Context, string, []byte) ([]byte, bool, error) {
	return nil, true, nil
}
func (h holeObjects) Get(context.Context, string) ([]byte, bool, error) { return nil, false, nil }
func (h holeObjects) List(_ context.Context, prefix, _ string) ([]string, error) {
	if strings.HasPrefix(h.key, prefix) {
		return []string{h.key}, nil
	}
	return nil, nil
}

// A fired item the store refuses (here, a chain with a missing sequence) is
// remote fire's replicationFailure: exit 1, and nothing is fired.
func TestRemoteFireRefusesARefusedItem(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "holed")
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	c, err := dial(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	store := &ledgerrepl.ObjectStore{Objects: holeObjects{key: ledgerrepl.ChainKey(id, 1)}, Name: "hole"}
	err = fireOnServer(cmd, k, bound{Client: c, Project: "acme", Repl: newReplicator(k, store, &out)}, []string{"ci", id})
	if ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "remote fire: 1 item(s) refused") || !strings.Contains(out.String(), "has no event at 0") {
		t.Fatalf("fire of a refused item: %v\n%s", err, out.String())
	}
	if fireCalls(srv) != 0 {
		t.Fatal("FireRemote was called for a refused item")
	}
}

// satelle remote status pulls only its run's item, through satelle-server,
// whatever [ledger] store names.
func TestRemoteStatusPullsOnlyItsItem(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	calls := recordingStore(t, m.dir)
	mine := srv.SeedItem("shared", map[string]any{"kind": "story", "title": "the run's"})
	other := srv.SeedItem("shared", map[string]any{"kind": "story", "title": "another"})
	srv.SetItemRun("ci", mine, &satellepb.ItemRun{State: remotenode.RunConcluded, Verdict: "accept"})
	out := must(t, "remote", "status", "ci", mine)
	if !strings.Contains(out, "pulled 1 event(s)") {
		t.Errorf("remote status = %q", out)
	}
	l := runtimeLedger(t)
	if got, _ := l.Read(mine); len(got) == 0 {
		t.Error("the run's item was not pulled")
	}
	if got, _ := l.Read(other); len(got) != 0 {
		t.Error("another item was pulled")
	}
	noStoreCall(t, calls)
}
