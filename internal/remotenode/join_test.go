//go:build !windows

package remotenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const nodeSecret = "s3cret-value-NEVER-REPORTED"

const nodeDefinition = `version = 1
provider = "docker-local"
satelle_version = "0.0.900"

[secrets.override]
NOTE = "satelle:NOTE"

[setup]
on_create = ["test \"$NOTE\" = '` + nodeSecret + `' && echo setup-ok", "echo leaked=$NOTE", "printf '%s\\n' \"$SATELLE_SUBSTRATE_DIR\" > ../substrate-seen && test -f \"$SATELLE_SUBSTRATE_DIR/satelle.toml\""]

[drive]
kind = "item"
`

type answers struct {
	mu      sync.Mutex
	calls   []string
	reqs    []agent.Request
	text    map[string]string
	seen    map[string]string
	folders []string
}

func recordFolder(prompt string) string {
	i := strings.Index(prompt, kernel.RecordLead)
	if i < 0 {
		return ""
	}
	rest := prompt[i+len(kernel.RecordLead):]
	return rest[:strings.Index(rest, ":\n")]
}

func (a *answers) Dispatch(_ context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, string(role)+":"+req.Binding.Name)
	a.reqs = append(a.reqs, req)
	if dir := recordFolder(req.Prompt); dir != "" {
		a.folders = append(a.folders, dir)
		if b, err := os.ReadFile(filepath.Join(dir, "attachments", "plan")); err == nil {
			if a.seen == nil {
				a.seen = map[string]string{}
			}
			a.seen[string(role)+":"+req.Binding.Name] = string(b)
		}
	}
	if t, ok := a.text[req.Binding.Name]; ok {
		return agent.Result{Text: t, Model: "m", TokensIn: 5, TokensOut: 7}, nil
	}
	return agent.Result{}, errors.New("no answer")
}

type localRepo struct {
	srv     *fakeserver.Server
	client  *hosted.Client
	project string
	k       *kernel.Kernel
}

func newLocalRepo(t *testing.T) *localRepo {
	t.Helper()
	srv := fakeserver.Start(t, "user-tok")
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "user-tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p, err := c.Projects.CreateProject(context.Background(), &satellepb.CreateProjectRequest{Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), ".satelle")
	writeFiles(t, root, lineConfig)
	k, err := kernel.Open(kernel.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return &localRepo{srv: srv, client: c, project: p.GetId(), k: k}
}

func (l *localRepo) repl() *ledgerrepl.Replicator {
	d := &ledgerrepl.Dev{Client: l.client, Project: l.project, Server: l.srv.URL, Ledger: l.k.Ledger, Cache: l.k.Cache}
	return &ledgerrepl.Replicator{Ledger: l.k.Ledger, Store: d, Refs: kernel.BlobRefs, Cache: l.k.Cache, Choose: l.k}
}

func nodeItems(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(filepath.Dir(p)) == "ledger" && strings.HasSuffix(p, ".jsonl") {
			out = append(out, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func nodeBundle(t *testing.T) []byte {
	files := map[string]string{"remote.toml": nodeDefinition}
	for p, b := range lineConfig {
		files[p] = b
	}
	return bundleOf(t, files)
}

func methods(srv *fakeserver.Server, auth string) []string {
	var out []string
	for _, c := range srv.Calls() {
		if c.Auth == auth {
			out = append(out, c.Method[strings.LastIndex(c.Method, "/")+1:])
		}
	}
	return out
}

func TestJoinDrivesAnItemAndReportsBack(t *testing.T) {
	l := newLocalRepo(t)
	it, err := l.k.Create(item.Fields{Kind: ptr("story"), Title: ptr("remote work")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	plan := strings.Repeat("plan line\n", 2048) + "the last line of the remote plan\n"
	if _, err := l.k.Attach(it.ID, "plan", []byte(plan), "text/markdown", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.repl().Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.srv.AddNode(&fakeserver.Node{ID: "nod_9", Token: "node-tok", Bundle: nodeBundle(t), Version: "v3",
		Secrets: map[string]string{"NOTE": nodeSecret}, Run: &satellepb.RunDescriptor{Mode: ModeItem, Project: l.project, ItemId: it.ID}})

	d := &answers{text: map[string]string{"doer": "did it", "r1": `{"verdict":"accept","notes":"fine"}`}}
	var log bytes.Buffer
	dir := t.TempDir()
	out, err := Join(context.Background(), JoinOptions{Server: l.srv.URL, NodeID: "nod_9", Token: "node-tok", Dir: dir,
		Log: &log, Version: "0.0.900", Dispatcher: d})
	if err != nil {
		t.Fatalf("Join = %v\n%s", err, log.String())
	}
	if out.Verdict != kernel.VerdictAccept || !strings.Contains(out.Notes, "reached c") {
		t.Fatalf("outcome = %+v", out)
	}
	if strings.Join(d.calls, " ") != "perform:doer judge:r1" {
		t.Fatalf("dispatched %v", d.calls)
	}
	for _, who := range []string{"perform:doer", "judge:r1"} {
		if d.seen[who] != plan {
			t.Errorf("%s read %d bytes of the plan from the node's record, want %d", who, len(d.seen[who]), len(plan))
		}
	}
	for _, f := range d.folders {
		if !strings.HasPrefix(f, dir) || !strings.Contains(f, filepath.Join(".satelle", "runs")) {
			t.Errorf("record folder %s is not under the node's tree %s", f, dir)
		}
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("record folder %s was left behind", f)
		}
	}
	substrate := filepath.Join(dir, "work", ".satelle")
	env := strings.Join(d.reqs[0].Env, " ")
	if len(d.reqs[0].Env) != 4 || !strings.Contains(env, "NOTE="+nodeSecret) ||
		!strings.Contains(env, "SATELLE_SESSION=perform_") || !strings.Contains(env, "SATELLE_PROJECT_DIR="+dir) ||
		!slices.Contains(d.reqs[0].Env, "SATELLE_SUBSTRATE_DIR="+substrate) {
		t.Fatalf("the performer's env = %q, want the run's secrets, its seat and its substrate", env)
	}
	if seen, err := os.ReadFile(filepath.Join(dir, "substrate-seen")); err != nil || string(seen) != substrate+"\n" {
		t.Fatalf("setup saw SATELLE_SUBSTRATE_DIR %q (%v), want %q", seen, err, substrate)
	}
	for _, s := range stagedGateRuns(t, dir, "nod_9") {
		if s.Head != "" {
			t.Fatalf("a node with no fetched substrate repo recorded head %q", s.Head)
		}
	}

	got := strings.Join(methods(l.srv, "node-tok"), " ")
	for _, want := range []string{"GetDefinition GetRun", "ReadSecret", "ReportStatus", "Read", "Append", "PutItems", "EndNode"} {
		if !strings.Contains(got, want) {
			t.Errorf("node calls %q lack %q", got, want)
		}
	}
	if !strings.HasSuffix(got, "ReportStatus EndNode") {
		t.Errorf("the node did not conclude then end: %s", got)
	}
	st := l.srv.NodeStatuses("nod_9")
	last := st[len(st)-1]
	if last.GetState() != StateConcluded || last.GetVerdict() != kernel.VerdictAccept || last.GetItemId() != it.ID ||
		!strings.Contains(last.GetSetupLog(), "setup-ok") || !strings.Contains(last.GetSetupLog(), "leaked=[secret]") {
		t.Fatalf("conclusion = %+v", last)
	}
	for _, s := range st {
		if strings.Contains(s.String(), nodeSecret) {
			t.Fatalf("a report carries the secret: %v", s)
		}
	}
	if !l.srv.NodeEnded("nod_9") {
		t.Fatal("the node did not end")
	}
	if _, err := l.repl().Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	back, err := l.k.Get(it.ID)
	if err != nil || back.Status != "c" {
		t.Fatalf("pulled item = %+v, %v", back, err)
	}
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(nodeSecret)) {
				t.Errorf("%s holds the secret", p)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJoinRunsATask(t *testing.T) {
	l := newLocalRepo(t)
	task, err := l.k.Create(item.Fields{Kind: ptr("task"), Title: ptr("sweep"), Inputs: &item.Selection{Kind: "story"}}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.repl().Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.srv.AddNode(&fakeserver.Node{ID: "nod_t", Token: "node-tok", Bundle: nodeBundle(t), Version: "v1",
		Secrets: map[string]string{"NOTE": nodeSecret}, Run: &satellepb.RunDescriptor{Mode: ModeTask, Project: l.project, ItemId: task.ID}})
	d := &answers{text: map[string]string{
		"doer": `did it {"artifact":{"name":"plan","type":"evidence","body":"execution evidence\n"}}`,
		"r1":   `{"verdict":"accept","notes":"fine"}`}}
	out, err := Join(context.Background(), JoinOptions{Server: l.srv.URL, NodeID: "nod_t", Token: "node-tok", Dir: t.TempDir(), Dispatcher: d})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("Join = %+v, %v", out, err)
	}
	if d.seen["judge:r1"] != "execution evidence\n" {
		t.Errorf("the judge read %q from the node's record, want the execution's evidence", d.seen["judge:r1"])
	}
	exec := strings.SplitN(out.Notes, ":", 2)[0]
	if events := l.srv.Events(l.project, exec); len(events) == 0 {
		t.Fatalf("the execution %q was not pushed (notes %q)", exec, out.Notes)
	}
}

func TestJoinConcludesRejectingWhenASecretIsRefused(t *testing.T) {
	l := newLocalRepo(t)
	l.srv.AddNode(&fakeserver.Node{ID: "nod_r", Token: "node-tok", Bundle: nodeBundle(t), Version: "v1",
		Run: &satellepb.RunDescriptor{Mode: ModeItem, Project: l.project, ItemId: "sto_00000000"}})
	out, err := Join(context.Background(), JoinOptions{Server: l.srv.URL, NodeID: "nod_r", Token: "node-tok", Dir: t.TempDir(), Dispatcher: &answers{}})
	var refusal *SecretRefusal
	if !errors.As(err, &refusal) || out.Verdict != kernel.VerdictReject {
		t.Fatalf("Join = %+v, %v", out, err)
	}
	st := l.srv.NodeStatuses("nod_r")
	if last := st[len(st)-1]; last.GetState() != StateConcluded || !strings.Contains(last.GetNotes(), "secret NOTE refused") || !l.srv.NodeEnded("nod_r") {
		t.Fatalf("conclusion = %+v, ended %v", last, l.srv.NodeEnded("nod_r"))
	}
}

func TestJoinConcludesAfterAHeartbeatHeldAcrossTheRunsEnd(t *testing.T) {
	l := newLocalRepo(t)
	release := make(chan struct{})
	l.srv.AddNode(&fakeserver.Node{ID: "nod_h", Token: "node-tok", Bundle: nodeBundle(t), Version: "v1", HoldHeartbeat: release,
		Run: &satellepb.RunDescriptor{Mode: ModeItem, Project: l.project, ItemId: "sto_00000000"}})
	answered := time.AfterFunc(time.Second, func() { close(release) })
	defer answered.Stop()
	out, err := Join(context.Background(), JoinOptions{Server: l.srv.URL, NodeID: "nod_h", Token: "node-tok", Dir: t.TempDir(),
		Dispatcher: &answers{}, Heartbeat: time.Hour})
	var refusal *SecretRefusal
	if !errors.As(err, &refusal) || out.Verdict != kernel.VerdictReject {
		t.Fatalf("Join = %+v, %v", out, err)
	}
	st := l.srv.NodeStatuses("nod_h")
	var states []string
	for _, s := range st {
		states = append(states, s.GetState())
	}
	if len(st) != 2 || states[0] != StateRunning || states[1] != StateConcluded {
		t.Fatalf("statuses = %v, want the held heartbeat's running then concluded", states)
	}
	if last := st[len(st)-1]; !strings.Contains(last.GetNotes(), "secret NOTE refused") || !l.srv.NodeEnded("nod_h") {
		t.Fatalf("conclusion = %+v, ended %v", last, l.srv.NodeEnded("nod_h"))
	}
}

type startingPlacer struct {
	Placer
	t      *testing.T
	srv    *fakeserver.Server
	bundle []byte
	d      agent.Dispatcher
	done   chan error
	dir    string
}

func (p *startingPlacer) StartNode(ctx context.Context, remote, itemID, binding string) (string, error) {
	node, err := p.Placer.StartNode(ctx, remote, itemID, binding)
	if err != nil {
		return "", err
	}
	p.srv.SetNodeDefinition(node, p.bundle, "v1")
	tok := p.srv.NodeToken(node)
	p.dir = p.t.TempDir()
	go func() {
		_, err := Join(context.Background(), JoinOptions{Server: p.srv.URL, NodeID: node, Token: tok, Dir: p.dir, Dispatcher: p.d})
		p.done <- err
	}()
	return node, nil
}

func TestPlacementRemotePerformsOnTheNode(t *testing.T) {
	l := newLocalRepo(t)
	it, err := l.k.Create(item.Fields{Kind: ptr("story"), Title: ptr("place me")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	plan := strings.Repeat("placed plan\n", 1500) + "the last line of the placed plan\n"
	if _, err := l.k.Attach(it.ID, "plan", []byte(plan), "text/markdown", "ada"); err != nil {
		t.Fatal(err)
	}
	other, err := l.k.Create(item.Fields{Kind: ptr("story"), Title: ptr("stays here")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	d := &answers{text: map[string]string{"doer": "done remotely"}}
	files := map[string]string{"remote.toml": performerDefinition}
	for p, b := range lineConfig {
		files[p] = b
	}
	p := &startingPlacer{Placer: Placer{Client: l.client, Project: l.project, Repl: l.repl()}, t: t, srv: l.srv, bundle: bundleOf(t, files), d: d, done: make(chan error, 1)}
	l.k.Remote = p
	res, err := l.k.Perform(context.Background(), it.ID, "ada")
	if err != nil {
		t.Fatalf("Perform = %v", err)
	}
	if res.Text != "done remotely" || res.TokensOut != 7 {
		t.Fatalf("result = %+v", res)
	}
	if err := <-p.done; err != nil {
		t.Fatalf("node Join = %v", err)
	}
	if len(d.reqs) != 1 || d.reqs[0].System != "Do the work.\n" || !strings.Contains(d.reqs[0].Prompt, "place me") {
		t.Fatalf("the node performed %+v", d.reqs)
	}
	if sub, _ := envValue(d.reqs[0].Env, "SATELLE_SUBSTRATE_DIR"); sub != filepath.Join(p.dir, "work", ".satelle") {
		t.Fatalf("the placed performer's SATELLE_SUBSTRATE_DIR = %q, want %q", sub, filepath.Join(p.dir, "work", ".satelle"))
	}
	seats, _ := l.k.Cache.Seats("", it.ID)
	if len(seats) != 0 {
		t.Fatalf("the node's seat was not released: %+v", seats)
	}
	got := strings.Join(methods(l.srv, "user-tok"), " ")
	for _, want := range []string{"StartNode", "Connect", "GetNode"} {
		if !strings.Contains(got, want) {
			t.Errorf("starter calls %q lack %q", got, want)
		}
	}
	start := strings.Index(got, "StartNode")
	for _, want := range []string{"Append", "PutAttachment"} {
		if i := strings.Index(got, want); i < 0 || i > start {
			t.Errorf("starter calls %q: %s does not come before StartNode", got, want)
		}
	}
	if d.seen["perform:doer"] != plan {
		t.Errorf("the node's performer read %d bytes of the plan from its record, want %d", len(d.seen["perform:doer"]), len(plan))
	}
	if len(l.srv.Events(l.project, it.ID)) == 0 || len(l.srv.Events(l.project, other.ID)) != 0 {
		t.Errorf("the server holds %d event(s) of the placed item and %d of the other, want some and none",
			len(l.srv.Events(l.project, it.ID)), len(l.srv.Events(l.project, other.ID)))
	}
	if got := nodeItems(t, p.dir); strings.Join(got, ",") != it.ID {
		t.Errorf("the performer node's ledger holds %v, want only %s", got, it.ID)
	}
	for _, f := range d.folders {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("record folder %s was left behind", f)
		}
	}
}

func TestPlacementWithoutAReplicatorRefuses(t *testing.T) {
	_, err := Placer{Client: &hosted.Client{}, Project: "prj"}.StartNode(context.Background(), "ci", "sto_1", "doer")
	if err == nil || !strings.Contains(err.Error(), "pushes the item first") {
		t.Fatalf("StartNode without Repl = %v", err)
	}
}

const performerDefinition = `version = 1
provider = "docker-local"
satelle_version = "0.0.900"

[setup]
on_create = ["true"]

[drive]
kind = "item"
`

func TestPlacementRefusesADivergedItem(t *testing.T) {
	l := newLocalRepo(t)
	it, err := l.k.Create(item.Fields{Kind: ptr("story"), Title: ptr("forked")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	events, err := l.k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Import(events[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Append(it.ID, "note.added", "bob", map[string]string{"n": "theirs"}); err != nil {
		t.Fatal(err)
	}
	theirs := &ledgerrepl.Replicator{Ledger: other, Store: &ledgerrepl.Dev{Client: l.client, Project: l.project}}
	if res, err := theirs.Push(context.Background()); err != nil || res.Failed() {
		t.Fatalf("the other runtime's push: %+v %v", res, err)
	}
	if _, err := l.k.Ledger.Append(it.ID, "note.added", "ada", map[string]string{"n": "mine"}); err != nil {
		t.Fatal(err)
	}
	_, err = Placer{Client: l.client, Project: l.project, Repl: l.repl()}.StartNode(context.Background(), "ci", it.ID, "doer")
	if err == nil || !strings.Contains(err.Error(), "pushing "+it.ID+" before its node starts: "+it.ID+" diverged from the server") {
		t.Fatalf("StartNode of a diverged item = %v", err)
	}
	for _, m := range methods(l.srv, "user-tok") {
		if m == "StartNode" {
			t.Fatal("StartNode was called for a diverged item")
		}
	}
}

func envValue(env []string, key string) (string, bool) {
	v, ok := "", false
	for _, kv := range env {
		if rest, found := strings.CutPrefix(kv, key+"="); found {
			v, ok = rest, true
		}
	}
	return v, ok
}

func stagedGateRuns(t *testing.T, nodeDir, node string) []gate.Staged {
	t.Helper()
	dir, err := config.CanonicalPath(nodeDir)
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(dir, "runtime")
	stamp, err := config.ReadRuntimeStamp(runtime)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.SubstrateDigest(stamp.Substrate)
	if err != nil || digest != stamp.Digest {
		t.Fatalf("substrate digest %s (%v), runtime stamp %s", digest, err, stamp.Digest)
	}
	st, err := ledger.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.Items()
	if err != nil {
		t.Fatal(err)
	}
	var out []gate.Staged
	for _, id := range ids {
		events, err := st.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Kind != gate.KindRun || e.Actor != "node:"+node {
				continue
			}
			var g gate.Result
			if err := json.Unmarshal(e.Payload, &g); err != nil {
				t.Fatal(err)
			}
			if g.Staged == nil || g.Staged.Digest != stamp.Digest {
				t.Fatalf("gate %s recorded staged %+v, want digest %s", g.Gate, g.Staged, stamp.Digest)
			}
			out = append(out, *g.Staged)
		}
	}
	if len(out) == 0 {
		t.Fatalf("node %s recorded no gate.run", node)
	}
	return out
}

func TestNodeResolvesAPartBindingByItem(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	files := map[string]string{
		"repos.toml":                  "[repos.x]\norigin = \"https://github.com/o/x\"\n",
		"repos/x/satelle.toml":        "[workflows]\ndefault = \"line\"\n",
		"repos/x/agents.toml":         "[agents.doer]\ncommand = \"claude\"\nmodel = \"x-doer\"\ntools = [\"Read\", \"Edit\"]\nplacement = \"remote:ci\"\n",
		"repos/x/workflows/line.toml": lineConfig["workflows/line.toml"],
	}
	for p, b := range lineConfig {
		files[p] = b
	}
	writeFiles(t, root, files)
	k, err := openNodeKernel(root, t.TempDir(), filepath.Join(t.TempDir(), "runtime"), nil, &answers{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	if p := k.PartAgents["x"]["doer"].Placement; p != agent.PlaceLocal {
		t.Fatalf("the part binding's placement on the node = %q", p)
	}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Repo: ptr("x")}, "ada")
	if err != nil || it.Workflow != "x/line" {
		t.Fatalf("create %+v %v", it, err)
	}
	if b, err := nodeBinding(k, it.ID, "doer"); err != nil || b.Model != "x-doer" {
		t.Errorf("an x item's doer = %+v %v", b, err)
	}
	if b, err := nodeBinding(k, "", "doer"); err != nil || b.Model != "" || b.Placement != agent.PlaceLocal {
		t.Errorf("doer with no item = %+v %v", b, err)
	}
	self, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := nodeBinding(k, self.ID, "doer"); err != nil || b.Model != "" {
		t.Errorf("a self item's doer = %+v %v", b, err)
	}
}
