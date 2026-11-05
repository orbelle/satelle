package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

const syncToken = "tok-integration"

// signedIn is a sandbox initialised and signed in to the fake server.
func signedIn(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	s.env["SATELLE_TOKEN"] = syncToken
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

// Two machines share a project on the fake server: what one pushes the other
// pulls, items, ledger and attachments alike; a push over a moved head is
// reported as a conflict and rewrites nothing.
func TestSyncBetweenTwoMachines(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, syncToken)

	a := signedIn(t)
	a.must("project", "create", "shared", "--bind", "--server", srv.URL)
	s1 := a.create("story", "--title", "from a", "--criteria", "it travels")
	s2 := a.create("story", "--title", "second", "--tag", "x")
	a.must("story", "set", s2.ID, "--status", "done")
	a.write("plan.md", "# the plan\n")
	a.must("story", "attach", s1.ID, "--file", "plan.md")
	if out := a.must("sync", "push"); !strings.Contains(out, "on 2 item(s) and 1 attachment(s)") {
		t.Errorf("push: %s", out)
	}

	b := signedIn(t)
	b.must("project", "bind", "shared", "--server", srv.URL)
	if out := b.must("sync", "status"); !strings.Contains(out, "remote only") {
		t.Errorf("status before pull: %s", out)
	}
	if out := b.must("sync", "pull"); !strings.Contains(out, "1 attachment(s)") {
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
	if out := b.must("sync", "status"); !strings.Contains(out, "in sync") {
		t.Errorf("status after pull: %s", out)
	}

	// b moves the head; a's edit of the same item now conflicts.
	b.must("story", "set", s1.ID, "--title", "b's title")
	b.must("sync", "push")
	a.must("story", "set", s1.ID, "--title", "a's title")
	r := a.run("sync", "push")
	if r.code == 0 {
		t.Fatalf("a push over a moved head succeeded:\n%s", r.out)
	}
	if all := r.out + r.err; !strings.Contains(all, "conflict: "+s1.ID) || !strings.Contains(all, "item.updated") {
		t.Errorf("conflict report:\n%s", all)
	}
	if out := a.must("sync", "status"); !strings.Contains(out, "diverged") {
		t.Errorf("status after the conflict: %s", out)
	}
	if got := len(srv.Events("shared", s1.ID)); got != len(hb[s1.ID])+1 {
		t.Errorf("server chain has %d events, want b's %d", got, len(hb[s1.ID])+1)
	}
	a.must("ledger", "verify")
}

// Every version of every attachment survives a push and a pull, each body
// identical by sha256 and readable by its sha on the other machine.
func TestSyncCarriesEveryAttachmentVersion(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, syncToken)
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
	a.must("sync", "push")

	b := signedIn(t)
	b.must("project", "bind", "versions", "--server", srv.URL)
	b.must("sync", "pull")

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
