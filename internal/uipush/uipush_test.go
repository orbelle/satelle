package uipush_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/uipush"
	"github.com/bobmcallan/satelle/internal/web"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The kinds and tokens satelled reads are the kernel's.
func TestWireConstantsAreTheKernels(t *testing.T) {
	for got, want := range map[string]string{
		mirror.EventGateRun:         gate.KindRun,
		mirror.EventTransitioned:    item.KindTransitioned,
		mirror.EventAttachmentAdded: item.KindAttachmentAdd,
		mirror.GateReject:           gate.Reject,
		mirror.GateError:            gate.Error,
		mirror.TransitionSep:        gate.TransitionKey("", ""),
	} {
		if got != want {
			t.Errorf("mirror has %q, the kernel %q", got, want)
		}
	}
}

// judge answers each gate from a queue of verdicts.
type judge struct {
	mu      sync.Mutex
	answers []string
}

func (j *judge) Dispatch(_ context.Context, _ agent.Role, _ agent.Request) (agent.Result, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.answers) == 0 {
		return agent.Result{}, errors.New("no answer")
	}
	a := j.answers[0]
	j.answers = j.answers[1:]
	return agent.Result{Text: a, TokensIn: 40, TokensOut: 9}, nil
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

// fixture is a repo whose one workflow gates its first move with a judge.
func fixture(t *testing.T) (*kernel.Kernel, uipush.Source) {
	t.Helper()
	repo := t.TempDir()
	root := filepath.Join(repo, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[items]\nprefix = { story = \"sto\" }\n")
	write(t, filepath.Join(root, "agents.toml"), "[agents.judge]\ncommand = \"claude\"\nmodel = \"m\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, "skills", "look.md"), "Look at it.\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.first]
start = true
[states.second]
[states.aside]
park = true
resumes = true
[states.last]
terminal = true
[gates.look]
skill = "look"
agent = "judge"
[[transition]]
from = ["first"]
to = "second"
groups = [{ gates = ["look"] }]
[[transition]]
from = ["second"]
to = "last"
[[transition]]
from = ["*"]
to = "aside"
`)
	k, err := kernel.Open(kernel.Options{Root: root, Runtime: filepath.Join(repo, "runtime"), Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	k.Git = nil
	k.Dispatcher = &judge{answers: []string{`{"verdict":"reject","notes":"not yet"}`, `{"verdict":"accept","notes":"fine"}`}}
	return k, uipush.Source{Ledger: k.Ledger, Cache: k.Cache, Workflows: k.Workflows, DataDir: root, RepoRoot: repo, RepoKey: "rk-test", Slug: "proj"}
}

func satelled(t *testing.T, instance string) (*httptest.Server, *mirror.Store) {
	t.Helper()
	t.Setenv("SATELLE_HOME", t.TempDir())
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := httptest.NewServer(web.NewMirrorWithInstance(s, instance).Handler)
	t.Cleanup(srv.Close)
	return srv, s
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, b)
	}
	return string(b)
}

// A push from the kernel lands in satelled: the first (a delta to an unseeded
// partition) falls back to a full snapshot, later ones merge.
func TestPushRoundTrip(t *testing.T) {
	ctx := context.Background()
	k, src := fixture(t)
	srv, store := satelled(t, "inst")
	c := uipush.Client{Endpoint: srv.URL, InstanceID: "inst"}

	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("Pushed"), Body: ptr("what to do"), Criteria: ptr([]string{"it lands"}), Assignee: ptr("ann")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Attach(it.ID, "notes.md", []byte("# Notes\n\nsome **text**"), "text/markdown", "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Attach(it.ID, "blob.bin", []byte{0, 1, 2}, "", "ann"); err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, it.ID, "second", "ann"); err != nil || res.Moved {
		t.Fatalf("first presentation: moved=%v err=%v", res.Moved, err)
	}
	if res, err := k.Transition(ctx, it.ID, "second", "ann"); err != nil || !res.Moved {
		t.Fatalf("second presentation: moved=%v err=%v", res.Moved, err)
	}
	if _, err := k.Seat(it.ID, "sess", "local:/x", "", "ann"); err != nil {
		t.Fatal(err)
	}
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	if seeded, _ := store.PartitionSeeded(ctx, "rk-test"); !seeded {
		t.Fatal("the fallback full snapshot did not seed the partition")
	}

	page := get(t, srv.URL+"/r/proj/story/"+it.ID)
	for _, want := range []string{
		"Pushed", "what to do", "<li>it lands</li>", "ann",
		"park · resumes", "outcome-refused", "not yet", "outcome-moved", "40 in · 9 out",
		"<h1>Notes</h1>", "text/markdown", "application/octet-stream", "body not pushed",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("story page missing %q", want)
		}
	}
	if i := strings.Index(page, `aria-current="step"`); i < 0 || !strings.HasPrefix(strings.TrimSpace(page[i+len(`aria-current="step">`):]), `<span class="wf-node">second</span>`) {
		t.Error("the route does not mark the item's current state")
	}
	// The kernel records no machine here, so the seat shows on Activity as
	// unverified rather than engaged (sto_e127b2e6).
	if home := get(t, srv.URL+"/r/proj/"); !strings.Contains(home, "sess · local:/x · unverified") {
		t.Error("the seat was not pushed")
	}
	if wf := get(t, srv.URL+"/r/proj/doc/workflows/flow"); !strings.Contains(wf, ".satelle/workflows/flow.toml") {
		t.Error("the workflow doc lacks its source")
	}
	if settings := get(t, srv.URL+"/r/proj/settings"); !strings.Contains(settings, "workflows.default") {
		t.Error("settings rows were not pushed")
	}

	// A later mutation merges: the new item and the edit both land.
	other, err := k.Create(item.Fields{Kind: ptr("task"), Title: ptr("Second item")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Update(it.ID, item.Fields{Title: ptr("Renamed")}, "ann"); err != nil {
		t.Fatal(err)
	}
	if err := k.Release(it.ID, "sess", "ann"); err != nil {
		t.Fatal(err)
	}
	if err := c.Push(ctx, src, []string{other.ID, it.ID}); err != nil {
		t.Fatal(err)
	}
	if list := get(t, srv.URL+"/r/proj/fragment/stories"); !strings.Contains(list, "Renamed") {
		t.Error("the edit did not merge")
	}
	if list := get(t, srv.URL+"/r/proj/fragment/tasks"); !strings.Contains(list, "Second item") {
		t.Error("the new item did not merge")
	}
	if home := get(t, srv.URL+"/r/proj/"); strings.Contains(home, "engaged") {
		t.Error("the released seat is still shown")
	}
}

// Seed pushes everything; a satelled of another home is refused, and Remove
// drops the partition.
func TestSeedInstanceAndRemove(t *testing.T) {
	ctx := context.Background()
	k, src := fixture(t)
	if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("Seeded")}, "ann"); err != nil {
		t.Fatal(err)
	}
	srv, store := satelled(t, "inst")
	if err := (uipush.Client{Endpoint: srv.URL, InstanceID: "elsewhere"}).Seed(ctx, src); !errors.Is(err, uipush.ErrOtherInstance) {
		t.Fatalf("seed into another home's satelled: %v", err)
	}
	c := uipush.Client{Endpoint: srv.URL, InstanceID: "inst"}
	if err := c.Seed(ctx, src); err != nil {
		t.Fatal(err)
	}
	if list := get(t, srv.URL+"/r/proj/fragment/stories"); !strings.Contains(list, "Seeded") {
		t.Error("seed did not land")
	}
	if err := c.Remove(ctx, "rk-test"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.GetPartition(ctx, "rk-test"); ok {
		t.Error("partition survived remove")
	}
	if err := (uipush.Client{Endpoint: "http://127.0.0.1:1"}).Push(ctx, src, nil); err == nil {
		t.Error("a push to nothing listening reported success")
	}
}

// A push carries who the CLI is signed in as, and satelled's top bar shows it.
func TestPushCarriesTheSignedInUser(t *testing.T) {
	ctx := context.Background()
	_, src := fixture(t)
	srv, store := satelled(t, "inst")
	src.User = &mirror.User{Name: "Ada", Email: "ada@example.com"}
	if err := (uipush.Client{Endpoint: srv.URL, InstanceID: "inst"}).Push(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if u := store.SignedIn(ctx); u == nil || *u != *src.User {
		t.Fatalf("satelled's signed-in user = %+v, want %+v", u, src.User)
	}
	if page := get(t, srv.URL+"/"); !strings.Contains(page, `aria-label="Signed in as Ada (ada@example.com)">A</span>`) {
		t.Errorf("the landing page's top bar does not show Ada")
	}
}

// A delta push (an ordinary change) carries who the CLI is signed in as, and
// replaces it: a sign-in shows on satelled's next change, a sign-out clears it.
func TestDeltaPushCarriesTheSignedInUser(t *testing.T) {
	ctx := context.Background()
	k, src := fixture(t)
	srv, store := satelled(t, "inst")
	c := uipush.Client{Endpoint: srv.URL, InstanceID: "inst"}
	if err := c.Push(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if seeded, _ := store.PartitionSeeded(ctx, "rk-test"); !seeded || store.SignedIn(ctx) != nil {
		t.Fatalf("seeded %v, signed in %+v", seeded, store.SignedIn(ctx))
	}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("After sign-in")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	src.User = &mirror.User{Name: "Ada", Email: "ada@example.com"}
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	if u := store.SignedIn(ctx); u == nil || *u != *src.User {
		t.Fatalf("after the delta, signed in = %+v", u)
	}
	if page := get(t, srv.URL+"/"); !strings.Contains(page, `aria-label="Signed in as Ada (ada@example.com)">A</span>`) {
		t.Error("the top bar does not show Ada after a delta push")
	}
	if rows := get(t, srv.URL+"/r/proj/fragment/stories"); !strings.Contains(rows, "After sign-in") {
		t.Error("the delta's change did not merge")
	}
	src.User = nil
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	if u := store.SignedIn(ctx); u != nil {
		t.Fatalf("after a signed-out delta, signed in = %+v", u)
	}
}

// Delta lists identity and the workflow docs among the kinds it replaces and
// carries the whole identity blob and every workflow.
func TestDeltaListsIdentityAndWorkflows(t *testing.T) {
	_, src := fixture(t)
	src.User = &mirror.User{Name: "Ada", Email: "ada@example.com"}
	snap, err := uipush.Delta(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snap.Kinds, "identity") || !slices.Contains(snap.Kinds, "doc") {
		t.Fatalf("delta kinds = %v", snap.Kinds)
	}
	if len(snap.Docs) != len(src.Workflows) {
		t.Fatalf("delta carries %d workflow docs, the repo has %d", len(snap.Docs), len(src.Workflows))
	}
	for name := range src.Workflows {
		if !slices.ContainsFunc(snap.Docs, func(d json.RawMessage) bool {
			var doc mirror.Doc
			return json.Unmarshal(d, &doc) == nil && doc.Kind == mirror.DocKindWorkflows && doc.Name == name
		}) {
			t.Errorf("delta lacks workflow %q", name)
		}
	}
	var meta mirror.IdentityMeta
	if err := json.Unmarshal(snap.Identity, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ProjectName != "proj" || meta.RepoRoot != src.RepoRoot || meta.User == nil || *meta.User != *src.User {
		t.Fatalf("delta identity = %+v", meta)
	}
}

// storyRow is the stories fragment's row for id, from its <tr to its </tr>.
func storyRow(body, id string) string {
	at := strings.Index(body, `data-expand-url="fragment/story/`+id+`"`)
	if at < 0 {
		return ""
	}
	start := strings.LastIndex(body[:at], "<tr")
	end := strings.Index(body[at:], "</tr>")
	if start < 0 || end < 0 {
		return ""
	}
	return body[start : at+end]
}

// reloadWorkflows re-reads the repo's workflow files, as every CLI push does.
func reloadWorkflows(t *testing.T, k *kernel.Kernel, src *uipush.Source) {
	t.Helper()
	wfs, err := workflow.Load(filepath.Join(src.DataDir, kernel.WorkflowsDir))
	if err != nil {
		t.Fatal(err)
	}
	src.Workflows = wfs
	k.Workflows = wfs
}

// A workflow file added after the seed reaches satelled on the next delta
// push, so its terminal rows are marked terminal; a workflow file changed
// after the seed is refreshed the same way.
func TestDeltaPushCarriesAnAddedOrChangedWorkflow(t *testing.T) {
	ctx := context.Background()
	k, src := fixture(t)
	srv, store := satelled(t, "inst")
	c := uipush.Client{Endpoint: srv.URL, InstanceID: "inst"}
	if err := c.Seed(ctx, src); err != nil {
		t.Fatal(err)
	}
	if seeded, _ := store.PartitionSeeded(ctx, "rk-test"); !seeded {
		t.Fatal("the seed did not seed the partition")
	}

	write(t, filepath.Join(src.DataDir, "workflows", "breakglass.toml"), `
[states.open]
start = true
[states.closed]
terminal = true
[[transition]]
from = ["open"]
to = "closed"
`)
	reloadWorkflows(t, k, &src)
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("Breakglass done"), Workflow: ptr("breakglass")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, it.ID, "closed", "ann"); err != nil || !res.Moved {
		t.Fatalf("close: moved=%v err=%v", res.Moved, err)
	}
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	row := storyRow(get(t, srv.URL+"/r/proj/fragment/stories"), it.ID)
	if row == "" {
		t.Fatal("the delta's item has no row in the stories fragment")
	}
	for _, want := range []string{`data-status="closed"`, `data-workflow="breakglass"`, `data-terminal="1"`, `st-terminal`} {
		if !strings.Contains(row, want) {
			t.Errorf("the added workflow's terminal row lacks %s: %s", want, row)
		}
	}

	if wf := get(t, srv.URL+"/r/proj/fragment/workflow/flow"); strings.Contains(wf, ">approved<") {
		t.Fatal("the seeded flow already has an approved state")
	}
	flow := filepath.Join(src.DataDir, "workflows", "flow.toml")
	b, err := os.ReadFile(flow)
	if err != nil {
		t.Fatal(err)
	}
	write(t, flow, string(b)+"[states.approved]\n")
	reloadWorkflows(t, k, &src)
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	if wf := get(t, srv.URL+"/r/proj/fragment/workflow/flow"); !strings.Contains(wf, ">approved<") {
		t.Error("the changed workflow's new state did not reach satelled")
	}
}

// A full push (the seed) of an item that re-attached a name succeeds, sending
// the newest version with its count; a later delta keeps one row per name.
func TestSeedPushesTheNewestVersionOfEachAttachment(t *testing.T) {
	ctx := context.Background()
	k, src := fixture(t)
	srv, store := satelled(t, "inst")
	c := uipush.Client{Endpoint: srv.URL, InstanceID: "inst"}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("Re-planned")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ name, body string }{{"plan.md", "# Plan\n\nfirst draft"}, {"plan.md", "# Plan\n\nsecond draft"}, {"notes.md", "# Notes\n\nonce"}} {
		if _, err := k.Attach(it.ID, a.name, []byte(a.body), "text/markdown", "ann"); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Seed(ctx, src); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n, _ := store.CountItems(ctx, "rk-test", "story_doc"); n != 2 {
		t.Fatalf("story_doc rows = %d, want 2", n)
	}
	page := get(t, srv.URL+"/r/proj/story/"+it.ID)
	if !strings.Contains(page, "second draft") || !strings.Contains(page, "2 versions") || strings.Contains(page, "first draft") {
		t.Errorf("the page does not show the newest plan with its count")
	}
	if strings.Count(page, " versions") != 1 {
		t.Errorf("a single-version attachment shows a count")
	}
	if _, err := k.Attach(it.ID, "plan.md", []byte("# Plan\n\nthird draft"), "text/markdown", "ann"); err != nil {
		t.Fatal(err)
	}
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	page = get(t, srv.URL+"/r/proj/story/"+it.ID)
	if !strings.Contains(page, "third draft") || !strings.Contains(page, "3 versions") {
		t.Errorf("after the delta the page does not show the third version")
	}
	if n, _ := store.CountItems(ctx, "rk-test", "story_doc"); n != 2 {
		t.Errorf("after the delta, story_doc rows = %d, want 2", n)
	}
}

// Report sees every snapshot satelled refuses, and a delta's fallback once as
// a full snapshot; a satelled that is not listening attempts none.
func TestReportSeesRefusedSnapshot(t *testing.T) {
	ctx := context.Background()
	k, src := fixture(t)
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("Reported")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		full bool
		err  error
	}
	var got []outcome
	record := func(full bool, err error) { got = append(got, outcome{full, err}) }

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("X-Satelle-Instance", "inst")
			return
		}
		http.Error(w, `mirror: snapshot has duplicate item kind=ledger_event id="deadbeef"`, http.StatusInternalServerError)
	}))
	t.Cleanup(refusing.Close)
	c := uipush.Client{Endpoint: refusing.URL, InstanceID: "inst", Report: record}
	if err := c.Seed(ctx, src); err == nil {
		t.Fatal("a refused seed reported success")
	}
	if len(got) != 1 || !got[0].full || got[0].err == nil || !strings.Contains(got[0].err.Error(), `kind=ledger_event id="deadbeef"`) {
		t.Fatalf("seed outcome %+v", got)
	}

	got = nil
	srv, _ := satelled(t, "inst")
	c = uipush.Client{Endpoint: srv.URL, InstanceID: "inst", Report: record}
	if err := c.Push(ctx, src, []string{it.ID}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].full || got[0].err != nil {
		t.Fatalf("a delta to an unseeded partition: outcomes %+v, want one full success", got)
	}

	got = nil
	c = uipush.Client{Endpoint: "http://127.0.0.1:1", Report: record}
	if err := c.Seed(ctx, src); err == nil {
		t.Fatal("a seed to nothing listening reported success")
	}
	if err := c.Push(ctx, src, []string{it.ID}); err == nil {
		t.Fatal("a push to nothing listening reported success")
	}
	if len(got) != 0 {
		t.Fatalf("an unreachable satelled reported %+v", got)
	}
}

// Pushed seat rows carry the machine and host the chain records for each seat
// (sto_e127b2e6 AC6); a seat bound without one carries none.
func TestPushedSeatsCarryTheMachine(t *testing.T) {
	k, src := fixture(t)
	a, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a")}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	k.Machine, k.Host = "m1", "h1"
	if _, err := k.Seat(a.ID, "s-here", "local:/x", "", "ann"); err != nil {
		t.Fatal(err)
	}
	k.Machine, k.Host = "", ""
	if _, err := k.Seat(a.ID, "s-old", "local:/x", "", "ann"); err != nil {
		t.Fatal(err)
	}
	snap, err := uipush.Full(src)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range snap.Seats {
		var s mirror.Seat
		if err := json.Unmarshal(r, &s); err != nil {
			t.Fatal(err)
		}
		got[s.Session] = s.Machine + "/" + s.Host
	}
	if got["s-here"] != "m1/h1" || got["s-old"] != "/" || len(got) != 2 {
		t.Fatalf("seat rows %v, want s-here m1/h1 and s-old with none", got)
	}
}
