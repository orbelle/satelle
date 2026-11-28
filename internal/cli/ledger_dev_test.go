package cli

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

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

func TestLedgerPushCarriesAssignees(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if err := (hosted.FileStore{}).Save(hosted.Credential{ServerURL: hosted.ConfiguredServer(gc), AccessToken: "a", RefreshToken: "r", Email: "ada@x.io"}); err != nil {
		t.Fatal(err)
	}
	id := createID(t, "story", "create", "--title", "worked")
	must(t, "story", "seat", id, "--session", "s1")
	must(t, "ledger", "push")
	summary := func() (assignees, working []string) {
		t.Helper()
		for _, it := range srv.Items("shared") {
			if it.GetId() == id {
				return it.GetAssignees(), it.GetWorking()
			}
		}
		t.Fatalf("the server holds no summary of %s", id)
		return nil, nil
	}
	if a, w := summary(); !slices.Equal(a, []string{"ada@x.io"}) || !slices.Equal(w, []string{"ada@x.io"}) {
		t.Errorf("seated summary assignees %q working %q, want ada@x.io in both", a, w)
	}
	must(t, "story", "release", id, "--session", "s1")
	must(t, "ledger", "push")
	if a, w := summary(); len(a) != 0 || len(w) != 0 {
		t.Errorf("released summary assignees %q working %q, want none", a, w)
	}
}

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
		if ExitCodeOf(err) != 1 || !strings.Contains(out, "conflict: "+id+" at event 1: this runtime's item.updated does not commute with the store's item.updated (event 1)") || !strings.Contains(out, "nothing re-based") {
			t.Errorf("%v over a diverged chain: %v\n%s", args, err, out)
		}
	}
	must(t, "ledger", "verify")
	_ = b
}

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

func TestLedgerDevRebasesAQueueAddPastAFire(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	triage := []byte("name = \"triage\"\n[states.open]\nstart = true\n[states.closed]\nterminal = true\n[[transition]]\nfrom = [\"open\"]\nto = \"closed\"\n")
	writeTriage := func() {
		t.Helper()
		if err := os.WriteFile(".satelle/workflows/triage.toml", triage, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := newMachine(t)
	writeTriage()
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	s1 := createID(t, "story", "create", "--title", "one")
	s2 := createID(t, "story", "create", "--title", "two")
	que := createID(t, "queue", "create", "--title", "triage queue", "--trigger-workflow", "triage")
	must(t, "queue", "add", que, s1)
	must(t, "ledger", "push")

	b := newMachine(t)
	writeTriage()
	must(t, "project", "bind", "shared", "--server", srv.URL)
	must(t, "ledger", "pull")

	a.use(t)
	must(t, "queue", "fire", que)
	must(t, "ledger", "push")

	b.use(t)
	must(t, "queue", "add", que, s2)
	before, err := runtimeLedger(t).Read(que)
	if err != nil {
		t.Fatal(err)
	}
	added := before[len(before)-1]
	if added.Kind != item.KindQueueAdded {
		t.Fatalf("B's last event is %s", added.Kind)
	}
	out := must(t, "ledger", "autosync")
	if st := syncStateOf(t); st.SyncReason != "" {
		t.Fatalf("B's pass reason %q\n%s", st.SyncReason, out)
	}
	got, err := runtimeLedger(t).Read(que)
	if err != nil {
		t.Fatal(err)
	}
	stored := srv.Events("shared", que)
	if len(stored) != len(got) || got[len(got)-1].ID != added.ID || got[len(got)-2].Kind != item.KindQueueFired {
		t.Fatalf("B holds %d events ending %s %s, satelle.dev %d", len(got), got[len(got)-1].ID, got[len(got)-1].Kind, len(stored))
	}
	if status := must(t, "ledger", "status"); !strings.Contains(lineOf(status, que), "in sync") {
		t.Errorf("status:\n%s", status)
	}
	if entries, _ := os.ReadDir(runtimeLedger(t).Dir() + "/forks"); len(entries) != 0 {
		t.Errorf("set aside %d file(s)", len(entries))
	}
	members := func() string {
		t.Helper()
		show := must(t, "queue", "show", que)
		if !strings.Contains(show, "members (1)") || !strings.Contains(show, s2) {
			t.Fatalf("queue show:\n%s", show)
		}
		return show
	}
	shown := members()

	a.use(t)
	must(t, "ledger", "autosync")
	theirs, err := runtimeLedger(t).Read(que)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hashesOf(theirs), hashesOf(got)) {
		t.Fatalf("A's %s is not B's", que)
	}
	if again := members(); again != shown {
		t.Errorf("A shows\n%s\nB shows\n%s", again, shown)
	}
}

func lineOf(out, id string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), id) {
			return line
		}
	}
	return ""
}

func hashesOf(events []ledger.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Hash)
	}
	return out
}
