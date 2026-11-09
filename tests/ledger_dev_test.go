//go:build integration

package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

const devToken = "tok-integration"

// signedIn is a sandbox initialised and signed in to the fake server.
func signedIn(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	s.env["SATELLE_TOKEN"] = devToken
	s.must("init")
	return s
}

// hashes returns every event hash of the sandbox's ledger, by item.
func (s *sandbox) hashes() map[string][]string {
	s.t.Helper()
	var evs []struct {
		Item string `json:"item"`
		Hash string `json:"hash"`
	}
	s.mustJSON(&evs, "ledger", "list")
	out := map[string][]string{}
	for _, e := range evs {
		out[e.Item] = append(out[e.Item], e.Hash)
	}
	return out
}

// Two machines share a project on the fake server, the store signed in with
// no [ledger] store set: what one pushes the other pulls, items, ledger and
// attachments alike; a push over a moved head is a divergence that exits 1
// and rewrites nothing.
func TestLedgerDevBetweenTwoMachines(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)

	a := signedIn(t)
	a.must("project", "create", "shared", "--bind", "--server", srv.URL)
	s1 := a.create("story", "--title", "from a", "--criteria", "it travels")
	s2 := a.create("story", "--title", "second", "--tag", "x")
	a.must("story", "set", s2.ID, "--status", "done")
	a.write("plan.md", "# the plan\n")
	a.must("story", "attach", s1.ID, "--file", "plan.md")
	if out := a.must("ledger", "push"); !strings.Contains(out, "1 blob(s) written on 2 item(s)") {
		t.Errorf("push: %s", out)
	}

	b := signedIn(t)
	b.must("project", "bind", "shared", "--server", srv.URL)
	if out := b.must("ledger", "status"); !strings.Contains(out, "store only") {
		t.Errorf("status before pull: %s", out)
	}
	if out := b.must("ledger", "pull"); !strings.Contains(out, "and 1 blob(s)") {
		t.Errorf("pull: %s", out)
	}

	var la, lb []item.Item
	a.mustJSON(&la, "story", "list")
	b.mustJSON(&lb, "story", "list")
	ja, _ := json.Marshal(la)
	jb, _ := json.Marshal(lb)
	if string(ja) != string(jb) {
		t.Errorf("items differ\na: %s\nb: %s", ja, jb)
	}
	ha, hb := a.hashes(), b.hashes()
	if len(ha) != 2 || len(ha[s1.ID]) == 0 {
		t.Fatalf("a's ledger = %v", ha)
	}
	for id, chain := range ha {
		if strings.Join(chain, ",") != strings.Join(hb[id], ",") {
			t.Errorf("ledger of %s differs\na: %v\nb: %v", id, chain, hb[id])
		}
	}
	if out := b.must("story", "attachment", s1.ID, "plan.md"); out != "# the plan\n" {
		t.Errorf("pulled attachment = %q", out)
	}
	b.must("ledger", "verify")
	if out := b.must("ledger", "status"); !strings.Contains(out, "in sync") {
		t.Errorf("status after pull: %s", out)
	}

	// b moves the head; a's edit of the same item now diverges.
	b.must("story", "set", s1.ID, "--title", "b's title")
	b.must("ledger", "push")
	a.must("story", "set", s1.ID, "--title", "a's title")
	r := a.run("ledger", "push")
	if r.code != 1 {
		t.Fatalf("a push over a moved head exited %d:\n%s", r.code, r.out)
	}
	if all := r.out + r.err; !strings.Contains(all, "diverged: "+s1.ID+" at event ") || !strings.Contains(all, "not pushed past it") {
		t.Errorf("divergence report:\n%s", all)
	}
	if r := a.run("ledger", "pull", s1.ID); r.code != 1 || !strings.Contains(r.out+r.err, "nothing appended") {
		t.Errorf("a pull of the diverged item exited %d:\n%s%s", r.code, r.out, r.err)
	}
	if out := a.must("ledger", "status"); !strings.Contains(out, "diverged at") {
		t.Errorf("status after the conflict: %s", out)
	}
	if got := len(srv.Events("shared", s1.ID)); got != len(hb[s1.ID])+1 {
		t.Errorf("server chain has %d events, want b's %d", got, len(hb[s1.ID])+1)
	}
	a.must("ledger", "verify")
}

// Every version of every attachment survives a push and a pull, each body
// identical by sha256 and readable by its sha on the other machine.
func TestLedgerDevCarriesEveryAttachmentVersion(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)
	a := signedIn(t)
	a.must("project", "create", "versions", "--bind", "--server", srv.URL)
	s := a.create("story", "--title", "versioned")
	bodies := []struct{ name, body string }{
		{"plan", "# plan v1\n"}, {"plan", "# plan v2\n"}, {"plan", "# plan v3, the newest\n"},
		{"evidence", "\x00\x01binary\xff evidence"}, {"notes.md", "notes\n"},
	}
	for _, x := range bodies {
		if r := a.exec(satelleBin, x.body, "story", "attach", s.ID, "--name", x.name, "--file", "-"); r.code != 0 {
			t.Fatalf("attach %s: %s%s", x.name, r.out, r.err)
		}
	}
	a.must("ledger", "push")

	b := signedIn(t)
	b.must("project", "bind", "versions", "--server", srv.URL)
	b.must("ledger", "pull")

	var atts []item.Attachment
	a.mustJSON(&atts, "story", "attachments", s.ID)
	if len(atts) != len(bodies) {
		t.Fatalf("a has %d attachment versions", len(atts))
	}
	added := map[string]bool{}
	for _, e := range b.ledger(s.ID, "attachment.added") {
		var p struct {
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		added[p.SHA256] = true
	}
	for i, at := range atts {
		got := b.must("story", "attachment", s.ID, at.Name, "--sha", at.SHA256)
		sum := sha256.Sum256([]byte(got))
		if hex.EncodeToString(sum[:]) != at.SHA256 || got != bodies[i].body {
			t.Errorf("%s v%d on b differs from a", at.Name, i)
		}
		if !added[at.SHA256] {
			t.Errorf("b's ledger has no attachment.added for %s", at.SHA256)
		}
	}
	if got := b.must("story", "attachment", s.ID, "plan"); got != "# plan v3, the newest\n" {
		t.Errorf("newest plan on b = %q", got)
	}
}

// An item created server-side with no workflow (as satelle-server's MCP
// does) arrives placed by each repo's own rules, can be driven, and a second
// machine adds nothing; foreign ids are kept as they are.
func TestLedgerDevPlacesAnItemThatArrivesWithoutAWorkflow(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)
	config := func(s *sandbox) {
		s.write(".satelle/workflows/bugflow.toml", "[states.triage]\nstart = true\n[states.done]\nterminal = true\n[[transition]]\nfrom = [\"triage\"]\nto = \"done\"\n")
		s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[[workflows.rule]]\nworkflow = \"bugflow\"\ncategory = \"bug\"\n[items]\nprefix = { story = \"sty\" }\n")
		s.must("validate")
	}
	a := signedIn(t)
	config(a)
	a.must("project", "create", "shared", "--bind", "--server", srv.URL)
	parent := srv.SeedItem("shared", map[string]any{"kind": "story", "title": "parent"})
	id := srv.SeedItem("shared", map[string]any{"kind": "story", "title": "from the server", "body": "b",
		"criteria": []string{"one", "two"}, "tags": []string{"x"}, "parent": parent, "category": "bug", "assignee": "usr_1"})

	if out := a.must("ledger", "pull"); !strings.Contains(out, "Placed 2 item(s)") {
		t.Fatalf("pull: %s", out)
	}
	got := a.get(id)
	if got.ID != id || got.Workflow != "bugflow" || got.Status != "triage" || strings.Join(got.Criteria, ",") != "one,two" ||
		strings.Join(got.Tags, ",") != "x" || got.Parent != parent || got.Category != "bug" || !slices.Equal(got.Assignees, []string{"usr_1"}) {
		t.Fatalf("placed item = %+v", got)
	}
	if p := a.get(parent); p.Workflow != "default" || p.Status != "backlog" {
		t.Fatalf("parent = %s/%s", p.Workflow, p.Status)
	}
	type ev struct {
		ID, Actor string
		Payload   json.RawMessage
	}
	events := func(s *sandbox, kind string) []ev {
		var evs []ev
		var all []struct {
			ID      string          `json:"id"`
			Kind    string          `json:"kind"`
			Actor   string          `json:"actor"`
			Payload json.RawMessage `json:"payload"`
		}
		s.mustJSON(&all, "ledger", "list", id)
		for _, e := range all {
			var c bytes.Buffer
			_ = json.Compact(&c, e.Payload)
			e.Payload = c.Bytes()
			if kind == "" || e.Kind == kind {
				evs = append(evs, ev{e.ID, e.Actor, e.Payload})
			}
		}
		return evs
	}
	upd, mv := events(a, "item.updated"), events(a, "item.transitioned")
	if len(upd) != 1 || upd[0].Actor != "satelle" || string(upd[0].Payload) != `{"workflow":"bugflow"}` {
		t.Fatalf("item.updated = %+v", upd)
	}
	if len(mv) != 1 || mv[0].Actor != "satelle" || string(mv[0].Payload) != `{"from":"","to":"triage"}` {
		t.Fatalf("item.transitioned = %+v", mv)
	}
	if first := events(a, "")[0]; !strings.HasPrefix(first.ID, "evt_") || first.ID != srv.Events("shared", id)[0].GetId() {
		t.Fatalf("the server's event id was not kept: %s", first.ID)
	}
	if local := a.create("story", "--title", "made here"); !strings.HasPrefix(local.ID, "sty_") {
		t.Fatalf("a local story's id %s ignores the repo prefix", local.ID)
	}
	a.must("story", "set", id, "--status", "done")
	if a.get(id).Status != "done" {
		t.Fatal("the placed item could not be driven")
	}
	a.must("ledger", "push")
	a.must("ledger", "verify")

	b := signedIn(t)
	config(b)
	b.must("project", "bind", "shared", "--server", srv.URL)
	if out := b.must("ledger", "pull"); strings.Contains(out, "Placed") {
		t.Fatalf("b placed again: %s", out)
	}
	if strings.Join(b.hashes()[id], ",") != strings.Join(a.hashes()[id], ",") {
		t.Fatal("b's chain differs from a's")
	}
	bu, bm := events(b, "item.updated"), events(b, "item.transitioned")
	if len(bu) != 1 || bu[0].Actor != "satelle" || len(bm) != 2 || bm[0].Actor != "satelle" ||
		string(bm[0].Payload) != `{"from":"","to":"triage"}` || string(bm[1].Payload) != `{"from":"triage","to":"done"}` {
		t.Fatalf("b's placing events: updated %+v transitioned %+v", bu, bm)
	}
	if b.get(id).Status != "done" {
		t.Fatalf("b's status %s", b.get(id).Status)
	}
	b.must("ledger", "verify")
}
