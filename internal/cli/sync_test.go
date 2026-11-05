package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
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
	out = must(t, "project", "list", "--server", srv.URL)
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

func TestSyncRoundTripWithAttachments(t *testing.T) {
	srv := fakeserver.Start(t, testToken)

	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "from a", "--criteria", "it travels")
	if err := os.WriteFile("plan.md", []byte("# the plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--file", "plan.md")
	out := must(t, "sync", "push")
	if !strings.Contains(out, "on 1 item(s) and 1 attachment(s)") {
		t.Errorf("push = %q", out)
	}
	if got := srv.Items("shared"); len(got) != 1 || got[0].GetId() != id || got[0].GetTitle() != "from a" {
		t.Errorf("server items = %v", got)
	}
	if atts := srv.Attachments(); len(atts) != 1 || string(atts[0].GetBody()) != "# the plan\n" {
		t.Errorf("server attachments = %v", atts)
	}
	if out := must(t, "sync", "push"); !strings.Contains(out, "Pushed 0 event(s) on 0 item(s) and 0 attachment(s)") {
		t.Errorf("second push = %q, want nothing pushed", out)
	}

	b := newMachine(t)
	must(t, "project", "bind", "shared", "--server", srv.URL)
	out = must(t, "sync", "status")
	if !strings.Contains(out, id) || !strings.Contains(out, "remote only") {
		t.Errorf("status before pull = %q", out)
	}
	out = must(t, "sync", "pull")
	if !strings.Contains(out, "and 1 attachment(s)") {
		t.Errorf("pull = %q", out)
	}
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil || it.Title != "from a" {
		t.Fatalf("pulled item = %+v, %v", it, err)
	}
	var atts []item.Attachment
	if err := json.Unmarshal([]byte(must(t, "story", "attachments", id, "--json")), &atts); err != nil || len(atts) != 1 {
		t.Fatalf("pulled attachments = %v, %v", atts, err)
	}
	if body, err := runtimeLedger(t).GetBlob(atts[0].SHA256); err != nil || string(body) != "# the plan\n" {
		t.Errorf("pulled body = %q, %v", body, err)
	}
	must(t, "ledger", "verify")

	// B changes the item and pushes; A pulls the change.
	must(t, "story", "set", id, "--title", "edited on b")
	if out := must(t, "sync", "status"); !strings.Contains(out, "ahead") {
		t.Errorf("status after a local edit = %q", out)
	}
	must(t, "sync", "push")
	a.use(t)
	if out := must(t, "sync", "status"); !strings.Contains(out, "behind") {
		t.Errorf("status on a = %q, want behind", out)
	}
	must(t, "sync", "pull")
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil || it.Title != "edited on b" {
		t.Fatalf("a after pull = %+v, %v", it, err)
	}
	if out := must(t, "sync", "status"); !strings.Contains(out, "in sync") {
		t.Errorf("status after pull = %q", out)
	}
	_ = b
}

func TestPushReportsAStaleHeadConflict(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	a := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "base", "--criteria", "c")
	must(t, "sync", "push")

	b := newMachine(t)
	must(t, "project", "bind", "shared", "--server", srv.URL)
	must(t, "sync", "pull")
	must(t, "story", "set", id, "--title", "b's title")
	must(t, "sync", "push")

	a.use(t)
	must(t, "story", "set", id, "--title", "a's title")
	out, err := run(t, "", "sync", "push")
	if err == nil {
		t.Fatalf("push over a moved head succeeded:\n%s", out)
	}
	if !strings.Contains(out, "conflict: "+id) || !strings.Contains(out, "local ") || !strings.Contains(out, "remote ") ||
		!strings.Contains(out, "item.updated") {
		t.Errorf("push output = %q, want the diverged events of %s", out, id)
	}
	// Nothing was rewritten: the server keeps b's chain, a keeps its own.
	if ev := srv.Events("shared", id); len(ev) != 2 {
		t.Errorf("server chain has %d events, want b's 2", len(ev))
	}
	if out := must(t, "sync", "status"); !strings.Contains(out, "diverged") {
		t.Errorf("status = %q, want diverged", out)
	}
	if _, err := run(t, "", "sync", "pull"); err == nil {
		t.Error("pull over a diverged chain succeeded")
	}
	must(t, "ledger", "verify")
	_ = b
}

func TestSyncNeedsABinding(t *testing.T) {
	newMachine(t)
	if _, err := run(t, "", "sync", "push"); err == nil || !strings.Contains(err.Error(), "project bind") {
		t.Errorf("unbound push = %v", err)
	}
}
