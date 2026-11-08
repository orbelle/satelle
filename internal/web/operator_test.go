package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// The operator fixture: a workflow whose state check is the operator's
// (recording verdict), with moves on to fin or back to work. The names are the
// test's own.
const opWorkflowTOML = `name = "flow"
[states.work]
start = true
[states.check]
performer = { operator = "verdict" }
[states.fin]
terminal = true
[[transition]]
from = ["work"]
to = "check"
[[transition]]
from = ["check"]
to = "fin"
[[transition]]
from = ["check"]
to = "work"
`

var opMirrorWorkflow = mirror.Workflow{
	Name: "flow",
	States: []mirror.WorkflowState{
		{Name: "work", Start: true},
		{Name: "check", PerformerAgent: mirror.PerformerOperator},
		{Name: "fin", Terminal: true},
	},
	Transitions: []mirror.WorkflowTransition{
		{From: []string{"work"}, To: "check"},
		{From: []string{"check"}, To: "fin"},
		{From: []string{"check"}, To: "work"},
	},
}

// opFixture is a repo root with the workflow file and a ledger in its runtime
// dir holding sto_a at check and sto_b at work, and the snapshot that pushes
// them, sto_a with a brief.
func opFixture(t *testing.T) (*httptest.Server, *ledger.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".satelle", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".satelle", "workflows", "flow.toml"), []byte(opWorkflowTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	snap := mirror.Snapshot{
		RepoKey: "rk", Slug: "proj",
		Stories: []json.RawMessage{
			raw(mirror.Item{ID: "sto_a", Kind: "story", Title: "Awaiting Story", Status: "check", Workflow: "flow", CreatedAt: t0, UpdatedAt: t0}),
			raw(mirror.Item{ID: "sto_b", Kind: "story", Title: "Working Story", Status: "work", Workflow: "flow", CreatedAt: t0, UpdatedAt: t0}),
		},
		StoryDocs: []json.RawMessage{
			raw(mirror.Attachment{ID: "sto_a/old-summary", Item: "sto_a", Name: "old-summary", MediaType: "text/markdown; type=brief", At: t0, Body: "# Old\n\nstale text"}),
			raw(mirror.Attachment{ID: "sto_a/result-summary", Item: "sto_a", Name: "result-summary", MediaType: "text/markdown; type=brief", At: t0.Add(time.Minute), Body: "## Changes\n\nthe **new** thing"}),
			raw(mirror.Attachment{ID: "sto_b/result-summary", Item: "sto_b", Name: "result-summary", MediaType: "text/markdown; type=brief", At: t0, Body: "not awaiting"}),
		},
		Docs: []json.RawMessage{
			raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: "flow", Body: opWorkflowTOML, Workflow: &opMirrorWorkflow}),
		},
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: root}),
	}
	// mirrorWith isolates the home, so the runtime dir is resolved after it.
	srv := mirrorWith(t, snap)
	l, err := ledger.Open(config.RuntimeDir(root))
	if err != nil {
		t.Fatal(err)
	}
	s := func(v string) *string { return &v }
	for id, status := range map[string]string{"sto_a": "check", "sto_b": "work"} {
		if _, err := l.Append(id, item.KindCreated, "op", item.Fields{Kind: s("story"), Title: s("title " + id), Workflow: s("flow"), Status: s("work")}); err != nil {
			t.Fatal(err)
		}
		if status != "work" {
			if _, err := l.Append(id, item.KindTransitioned, "op", item.Move{From: "work", To: status}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return srv, l, root
}

// AC5: the page lists only the items in an operator state, renders the newest
// brief, and shows whether the operator's word is recorded.
func TestOperatorPageListsAwaitingItems(t *testing.T) {
	srv, l, _ := opFixture(t)
	body := httpGetBody(t, srv.URL+"/r/proj/operator")
	if !strings.Contains(body, "Awaiting Story") || strings.Contains(body, "Working Story") {
		t.Fatalf("listed the wrong items:\n%s", body)
	}
	if !strings.Contains(body, "the <strong>new</strong> thing") || strings.Contains(body, "stale text") || strings.Contains(body, "not awaiting") {
		t.Errorf("brief not the newest of the item's:\n%s", body)
	}
	if !strings.Contains(body, "awaiting your word") {
		t.Errorf("no record status:\n%s", body)
	}
	for _, want := range []string{`action="operator-record/sto_a"`, `value="fin"`, `value="work"`, `name="retype"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %s", want)
		}
	}
	// The project's tab and its live fragment list it too.
	if page := httpGetBody(t, srv.URL+"/r/proj/"); !strings.Contains(page, `data-panel="operator"`) || !strings.Contains(page, `href="operator#op-sto_a"`) {
		t.Errorf("project page has no Awaiting-you tab row")
	}
	if frag := httpGetBody(t, srv.URL+"/r/proj/fragment/operator"); !strings.Contains(frag, "sto_a") || strings.Contains(frag, "sto_b") {
		t.Errorf("fragment:\n%s", frag)
	}
	if _, _, err := item.AppendAttachment(l, "sto_a", item.Attachment{Name: "verdict", MediaType: "text/markdown; type=verdict", Operator: true, To: "fin"}, []byte("ok"), "ann"); err != nil {
		t.Fatal(err)
	}
	if body := httpGetBody(t, srv.URL+"/r/proj/operator"); !strings.Contains(body, "ann chose fin at") {
		t.Errorf("recorded word not shown:\n%s", body)
	}
}

// postWord posts the operator form with the given headers.
func postWord(t *testing.T, srv *httptest.Server, id string, form url.Values, hdr map[string]string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/r/proj/operator-record/"+id, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b), resp.Header.Get("Location")
}

// AC6 and AC7: a same-origin loopback form records the word; every refusal
// writes nothing.
func TestOperatorRecordRefusals(t *testing.T) {
	srv, l, _ := opFixture(t)
	t.Setenv("SATELLE_SESSION", "")
	same := map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"}
	good := url.Values{"to": {"fin"}, "word": {"tried it, works"}, "retype": {"sto_a"}}
	length := func() int {
		events, err := l.Read("sto_a")
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	n := length()
	with := func(k, v string) url.Values {
		f := url.Values{}
		for key, vals := range good {
			f[key] = vals
		}
		f.Set(k, v)
		return f
	}
	for _, c := range []struct {
		name string
		form url.Values
		hdr  map[string]string
		code int
		want string
	}{
		{"no origin", good, map[string]string{"Sec-Fetch-Site": "same-origin"}, 403, "same-origin"},
		{"foreign origin", good, map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "same-origin"}, 403, "same-origin"},
		{"cross-site", good, map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "cross-site"}, 403, "same-origin"},
		{"mistyped id", with("retype", "sto_b"), same, 400, "is not sto_a"},
		{"undeclared move", with("to", "nowhere"), same, 409, "does not declare"},
		{"empty word", with("word", "  "), same, 409, "is empty"},
	} {
		code, body, _ := postWord(t, srv, "sto_a", c.form, c.hdr)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %q", c.name, code, body)
		}
	}
	t.Setenv("SATELLE_SESSION", "s-1")
	if code, body, _ := postWord(t, srv, "sto_a", good, same); code != 403 || !strings.Contains(body, "agent session") {
		t.Errorf("under a session: %d %q", code, body)
	}
	t.Setenv("SATELLE_SESSION", "")
	if got := length(); got != n {
		t.Fatalf("a refused POST wrote %d events", got-n)
	}
	// Not from this machine (the handler itself; the test server is loopback).
	st, err := mirror.Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ms := NewMirrorWithInstance(st, "test")
	req, _ := http.NewRequest(http.MethodPost, "http://satelled.test/r/proj/operator-record/sto_a", strings.NewReader(good.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Every other guard would pass: only the address refuses it.
	req.Header.Set("Origin", "http://satelled.test")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.RemoteAddr = "192.0.2.7:5555"
	rec := httptest.NewRecorder()
	ms.Handler.ServeHTTP(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "loopback") || length() != n {
		t.Errorf("non-loopback: %d %q", rec.Code, rec.Body.String())
	}

	code, _, loc := postWord(t, srv, "sto_a", good, same)
	if code != http.StatusSeeOther || loc != "/r/proj/operator?recorded=sto_a" {
		t.Fatalf("record: %d %q", code, loc)
	}
	events, _ := l.Read("sto_a")
	atts := item.FoldAttachments(events)
	if len(atts) != 1 || !atts[0].Operator || atts[0].To != "fin" || atts[0].Name != "verdict" || atts[0].MediaType != "text/markdown; type=verdict" {
		t.Fatalf("recorded %+v", atts)
	}
}

// satelled's only ledger write is the operator's word through
// internal/operator: no other web code appends, imports or stores a blob.
func TestWebWritesOnlyTheOperatorsWord(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// logfile.Append writes satelled's request log, not the ledger.
		src := strings.ReplaceAll(string(b), "logfile.Append(", "")
		for _, w := range []string{".Append(", ".AppendRaw(", ".PutBlob(", ".Import(", "AppendAttachment("} {
			if strings.Contains(src, w) {
				t.Errorf("%s calls %s: satelled's one write is operator.Record", f, w)
			}
		}
	}
	b, err := os.ReadFile("operator.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "operator.Record(") != 1 {
		t.Error("operator.go should record the word in exactly one place")
	}
}
