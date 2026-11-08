package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const testToken = "tok-ada"

// machine is one local satelle: its own home and repo.
type machine struct {
	home, dir string
}

// newMachine isolates a fresh home and repo, initialises it and signs it in
// with the session token the fake accepts.
func newMachine(t *testing.T) machine {
	t.Helper()
	dir := isolate(t)
	t.Setenv("SATELLE_TOKEN", testToken)
	must(t, "init")
	return machine{home: os.Getenv("HOME"), dir: dir}
}

// use makes m the current machine.
func (m machine) use(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", m.home)
	t.Setenv("SATELLE_HOME", filepath.Join(m.home, ".satelle"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(m.home, "xdg"))
	t.Chdir(m.dir)
}

func runtimeLedger(t *testing.T) *ledger.Store {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(r.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestWhoamiAndProjects(t *testing.T) {
	newMachine(t)
	srv := fakeserver.Start(t, testToken)

	out := must(t, "whoami", "--server", srv.URL)
	if !strings.Contains(out, "Ada <ada@example.com>") {
		t.Errorf("whoami = %q", out)
	}
	must(t, "project", "create", "alpha", "--name", "Alpha", "--server", srv.URL)
	must(t, "project", "create", "beta", "--bind", "--server", srv.URL)
	out = must(t, "project", "list", "--hosted", "--server", srv.URL)
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "*  beta") {
		t.Errorf("project list = %q, want both projects with beta bound", out)
	}
	// The binding carries the server: no --server needed from here on.
	out = must(t, "project", "show")
	if !strings.Contains(out, "slug:   beta") {
		t.Errorf("project show = %q", out)
	}
	if _, err := run(t, "", "project", "create", "alpha", "--server", srv.URL); err == nil {
		t.Error("creating an existing slug succeeded")
	}
	for _, c := range srv.Calls() {
		if c.Auth != testToken {
			t.Errorf("call %s carried bearer %q, want SATELLE_TOKEN", c.Method, c.Auth)
		}
	}

	t.Setenv("SATELLE_TOKEN", "")
	if _, err := run(t, "", "whoami", "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "satelle login") {
		t.Errorf("whoami signed out = %v, want a login prompt", err)
	}
}

// Signed in with no [ledger] store set, the store is satelle.dev: push,
// status and pull carry items, their chains and attachment bodies between
// two machines bound to one project.
func TestLedgerDevRoundTripWithAttachments(t *testing.T) {
	srv := fakeserver.Start(t, testToken)

	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "from a", "--criteria", "it travels")
	if err := os.WriteFile("plan.md", []byte("# the plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--file", "plan.md")
	out := must(t, "ledger", "push")
	if !strings.Contains(out, "ledger store: satelle.dev project ") || !strings.Contains(out, "1 blob(s) written on 1 item(s)") {
		t.Errorf("push = %q", out)
	}
	if got := srv.Items("shared"); len(got) != 1 || got[0].GetId() != id || got[0].GetTitle() != "from a" {
		t.Errorf("server items = %v", got)
	}
	if atts := srv.Attachments(); len(atts) != 1 || string(atts[0].GetBody()) != "# the plan\n" {
		t.Errorf("server attachments = %v", atts)
	}
	if out := must(t, "ledger", "push"); !strings.Contains(out, "push: 0 event(s) and 0 blob(s) written on 0 item(s)") {
		t.Errorf("second push = %q, want nothing pushed", out)
	}

	b := newMachine(t)
	must(t, "project", "bind", "shared", "--server", srv.URL)
	out = must(t, "ledger", "status")
	if !strings.Contains(out, id) || !strings.Contains(out, "store only") {
		t.Errorf("status before pull = %q", out)
	}
	out = must(t, "ledger", "pull")
	if !strings.Contains(out, "on 1 item(s) and 1 blob(s)") {
		t.Errorf("pull = %q", out)
	}
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil || it.Title != "from a" {
		t.Fatalf("pulled item = %+v, %v", it, err)
	}
	atts := attachments(t, id)
	if len(atts) != 1 {
		t.Fatalf("pulled attachments = %v", atts)
	}
	if body, err := runtimeLedger(t).GetBlob(atts[0].SHA256); err != nil || string(body) != "# the plan\n" {
		t.Errorf("pulled body = %q, %v", body, err)
	}
	must(t, "ledger", "verify")
	if out := must(t, "ledger", "status"); !strings.Contains(out, "in sync") {
		t.Errorf("status after pull = %q", out)
	}

	// B changes the item and pushes; A pulls the change.
	must(t, "story", "set", id, "--title", "edited on b")
	if out := must(t, "ledger", "status"); !strings.Contains(out, "ahead") {
		t.Errorf("status after a local edit = %q", out)
	}
	must(t, "ledger", "push")
	a.use(t)
	if out := must(t, "ledger", "status"); !strings.Contains(out, "behind") {
		t.Errorf("status on a = %q, want behind", out)
	}
	must(t, "ledger", "pull")
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil || it.Title != "edited on b" {
		t.Fatalf("a after pull = %+v, %v", it, err)
	}
	if out := must(t, "ledger", "status"); !strings.Contains(out, "in sync") {
		t.Errorf("status after pull = %q", out)
	}
	var rows []struct{ Item, State string }
	if err := json.Unmarshal([]byte(must(t, "ledger", "status", "--json")), &rows); err != nil || len(rows) != 1 || rows[0].State != "in sync" {
		t.Errorf("status --json = %+v, %v", rows, err)
	}
	_ = b
}

// A push over a moved head on satelle.dev is a divergence: push and pull
// exit 1 naming the item, status says diverged, nothing is rewritten.
func TestLedgerDevDivergence(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "base", "--criteria", "c")
	must(t, "ledger", "push")

	b := newMachine(t)
	must(t, "project", "bind", "shared", "--server", srv.URL)
	must(t, "ledger", "pull")
	must(t, "story", "set", id, "--title", "b's title")
	must(t, "ledger", "push")

	a.use(t)
	must(t, "story", "set", id, "--title", "a's title")
	out, err := run(t, "", "ledger", "push")
	if ExitCodeOf(err) != 1 || !strings.Contains(out, "diverged: "+id+" at event 1: ") || !strings.Contains(err.Error(), id) {
		t.Fatalf("push over a moved head: %v\n%s", err, out)
	}
	if ev := srv.Events("shared", id); len(ev) != 2 {
		t.Errorf("server chain has %d events, want b's 2", len(ev))
	}
	if out := must(t, "ledger", "status"); !strings.Contains(out, "diverged at 1") {
		t.Errorf("status = %q, want diverged", out)
	}
	for _, args := range [][]string{{"ledger", "pull"}, {"ledger", "pull", id}} {
		out, err := run(t, "", args...)
		if ExitCodeOf(err) != 1 || !strings.Contains(out, "diverged: "+id+" at event 1: ") || !strings.Contains(out, "nothing appended") {
			t.Errorf("%v over a diverged chain: %v\n%s", args, err, out)
		}
	}
	must(t, "ledger", "verify")
	_ = b
}

// A pull of every item and of the one item, whose local chain is ahead of
// the server's (a pure prefix of it), is no conflict: exit 0, nothing
// imported.
func TestLedgerDevPullOfAPurePrefix(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "base", "--criteria", "c")
	must(t, "ledger", "push")
	must(t, "story", "set", id, "--title", "ahead here")
	for _, args := range [][]string{{"ledger", "pull"}, {"ledger", "pull", id}} {
		out := must(t, args...)
		if !strings.Contains(out, "pull: 0 event(s)") || strings.Contains(out, "diverged") {
			t.Errorf("%v = %q", args, out)
		}
	}
	if out := must(t, "ledger", "status"); !strings.Contains(out, "ahead") {
		t.Errorf("status = %q", out)
	}
}

// satelle remote status pulls only its run's item, through satelle-server.
func TestRemoteStatusPullsOnlyItsItem(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
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
}
