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
)

// The kinds and tokens satelled reads are the kernel's.
func TestWireConstantsAreTheKernels(t *testing.T) {
	for got, want := range map[string]string{
		mirror.EventGateRun:         gate.KindRun,
		mirror.EventTransitioned:    item.KindTransitioned,
		mirror.EventAttachmentAdded: item.KindAttachmentAdd,
		mirror.GateReject:           gate.Reject,
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
	if _, err := k.Seat(it.ID, "sess", "local:/x", "ann"); err != nil {
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
	if home := get(t, srv.URL+"/r/proj/"); !strings.Contains(home, "1 engaged") {
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

// Delta lists identity among the kinds it replaces and carries the whole blob.
func TestDeltaListsIdentity(t *testing.T) {
	_, src := fixture(t)
	src.User = &mirror.User{Name: "Ada", Email: "ada@example.com"}
	snap, err := uipush.Delta(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snap.Kinds, "identity") {
		t.Fatalf("delta kinds = %v", snap.Kinds)
	}
	var meta mirror.IdentityMeta
	if err := json.Unmarshal(snap.Identity, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ProjectName != "proj" || meta.RepoRoot != src.RepoRoot || meta.User == nil || *meta.User != *src.User {
		t.Fatalf("delta identity = %+v", meta)
	}
}
