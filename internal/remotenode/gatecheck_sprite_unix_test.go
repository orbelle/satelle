//go:build !windows

package remotenode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/workflow"
)

var integrationFile = filepath.Join("..", "..", ".satelle", "remotes", "integration", DefinitionFile)

func swapProviderLine(t *testing.T, def string) string {
	t.Helper()
	const from, to = `provider = "docker-local"`, `provider = "fly-sprite"`
	if strings.Count(def, from+"\n") != 1 {
		t.Fatalf("the definition has no single %s line", from)
	}
	out := strings.Replace(def, from+"\n", to+"\n", 1)
	a, b := strings.Split(def, "\n"), strings.Split(out, "\n")
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
			if a[i] != from || b[i] != to {
				t.Fatalf("line %d: %q became %q", i+1, a[i], b[i])
			}
		}
	}
	if len(a) != len(b) || diff != 1 {
		t.Fatalf("%d lines differ (of %d and %d), want 1", diff, len(a), len(b))
	}
	return out
}

type spriteFixture struct {
	dataDir, tree string
	orig, swapped string
	head          gate.Head
}

func newSpriteFixture(t *testing.T) spriteFixture {
	t.Helper()
	b, err := os.ReadFile(integrationFile)
	if err != nil {
		t.Fatal(err)
	}
	f := spriteFixture{orig: string(b), swapped: swapProviderLine(t, string(b))}
	base := t.TempDir()
	f.tree = filepath.Join(base, "tree")
	f.dataDir = filepath.Join(f.tree, ".satelle")
	writeFiles(t, filepath.Join(f.dataDir, RemotesDir, "integration"), map[string]string{DefinitionFile: f.swapped})
	origin := filepath.Join(base, "origin.git")
	git := func(dir string, args ...string) string { t.Helper(); return gittest.Git(t, dir, args...) }
	git(base, "init", "-q", "--bare", origin)
	git(base, "init", "-q", "-b", "main", f.tree)
	if err := os.WriteFile(filepath.Join(f.tree, "README"), []byte("checked on a sprite\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(f.tree, "add", "README")
	git(f.tree, "commit", "-q", "-m", "one")
	git(f.tree, "remote", "add", "origin", origin)
	git(f.tree, "push", "-q", "origin", "main")
	git(f.tree, "fetch", "-q", "origin")
	f.head = gate.Head{Commit: git(f.tree, "rev-parse", "HEAD"), Ref: "refs/remotes/origin/main"}
	return f
}

type recordingStarter struct {
	t      *testing.T
	mu     sync.Mutex
	starts []SpriteStart
	h      *hostHandle
}

func (r *recordingStarter) StartSprite(_ context.Context, s SpriteStart) (SpriteNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, s)
	r.h = &hostHandle{}
	return SpriteNode{Node: "nod_fake", Handle: r.h, Root: filepath.Join(r.t.TempDir(), "gate")}, nil
}

type hostHandle struct{ stopped bool }

func (*hostHandle) ID() string { return "nod_fake" }
func (*hostHandle) Exec(ctx context.Context, argv ...string) ([]byte, error) {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
}
func (*hostHandle) CopyIn(_ context.Context, host, machine string) error {
	return copyTree(host, machine)
}
func (*hostHandle) CopyOut(_ context.Context, machine, host string) error {
	return copyTree(machine, host)
}
func (*hostHandle) Logs(context.Context) ([]byte, error) { return nil, nil }
func (h *hostHandle) Stop(context.Context) error         { h.stopped = true; return nil }

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

func recordingDocker(t *testing.T) (provider.DockerLocal, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> '"+log+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return provider.DockerLocal{Docker: bin}, log
}

func TestGateOnTheFlySpriteIntegrationRemoteReachesTheSpriteStart(t *testing.T) {
	for _, c := range []struct {
		script, want string
		notes        []string
	}{
		{"exit 0", gate.Accept, []string{"exit 0"}},
		{"echo bad; exit 1", gate.Reject, []string{"bad", "exit 1"}},
	} {
		f := newSpriteFixture(t)
		docker, calls := recordingDocker(t)
		rec := &recordingStarter{t: t}
		gates := map[string]workflow.Gate{"integration-check": {Skill: "chk", Placement: "remote:integration"}}
		wf := &workflow.Workflow{Name: "w", States: map[string]workflow.State{"a": {Start: true}, "z": {Terminal: true}}, Gates: gates}
		run := gate.Run{
			Item: item.Item{ID: "sto_1", Title: "t"}, From: "a", To: "z", Workflow: wf,
			Transition: &workflow.Transition{From: []string{"a"}, To: "z", Groups: []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"integration-check"}}}},
			Skills:     map[string]workflow.Skill{"chk": {Name: "chk", Meta: map[string]string{"type": workflow.TypeCheck}, Script: c.script}},
			Dir:        f.tree,
			Remote:     GateChecker{Tree: f.tree, DataDir: f.dataDir, Docker: docker, Sprites: rec},
			Head:       f.head,
		}
		rs, pass := run.Execute(testCtx(t))
		if len(rs) != 1 || rs[0].Result != c.want || pass != (c.want == gate.Accept) || rs[0].Location != "remote:nod_fake" {
			t.Fatalf("%s: %v %+v", c.script, pass, rs)
		}
		for _, n := range c.notes {
			if !strings.Contains(rs[0].Notes, n) {
				t.Errorf("%s: notes %q lack %q", c.script, rs[0].Notes, n)
			}
		}
		if len(rec.starts) != 1 {
			t.Fatalf("%s: the sprite start was called %d times", c.script, len(rec.starts))
		}
		got := rec.starts[0]
		if got.Remote != "integration" || got.Item != "sto_1" {
			t.Fatalf("%s: started %q for %q", c.script, got.Remote, got.Item)
		}
		want, _, err := LoadLocal(filepath.Join("..", "..", ".satelle"), "integration")
		if err != nil {
			t.Fatal(err)
		}
		if want.Provider != remotedef.ProviderDockerLocal {
			t.Fatalf("this repo's integration remote is %s", want.Provider)
		}
		want.Provider = remotedef.ProviderFlySprite
		if !reflect.DeepEqual(got.Definition, want) {
			t.Fatalf("%s: the sprite start got\n%+v\nwant\n%+v", c.script, got.Definition, want)
		}
		if got.Definition.Machine.Image != "golang:1.27-alpine" ||
			!reflect.DeepEqual(got.Definition.Setup.OnCreate, []string{"apk add --no-cache git make bash jq curl"}) {
			t.Fatalf("%s: image %q setup %q", c.script, got.Definition.Machine.Image, got.Definition.Setup.OnCreate)
		}
		sum := sha256.Sum256([]byte(f.swapped))
		if got.DefinitionSHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: definition sha256 %s", c.script, got.DefinitionSHA256)
		}
		if !rec.h.stopped {
			t.Errorf("%s: the sprite node was not ended", c.script)
		}
		if b, err := os.ReadFile(calls); err == nil || len(b) > 0 {
			t.Fatalf("%s: docker was called: %q", c.script, b)
		}
	}
}

func TestGateOnFlySpriteNeedsABoundProject(t *testing.T) {
	f := newSpriteFixture(t)
	docker, calls := recordingDocker(t)
	_, err := GateChecker{Tree: f.tree, DataDir: f.dataDir, Docker: docker}.RunRemote(testCtx(t),
		gate.RemoteCheck{Remote: "integration", Gate: "integration-check", Script: "exit 0", Env: gate.CheckEnv{Item: "sto_1"}, Head: f.head})
	if err == nil || !strings.Contains(err.Error(), "gate integration-check is placed on fly-sprite remote integration, which needs a bound project (satelle project bind)") {
		t.Fatalf("RunRemote = %v", err)
	}
	if b, err := os.ReadFile(calls); err == nil || len(b) > 0 {
		t.Fatalf("docker was called: %q", b)
	}
}

type spritePlacer struct {
	Placer
	t         *testing.T
	srv       *fakeserver.Server
	bundle    []byte
	agentOnly bool
	done      chan error
}

func (p *spritePlacer) StartNode(ctx context.Context, remote, itemID, binding string) (string, error) {
	node, err := p.Placer.StartNode(ctx, remote, itemID, binding)
	if err != nil {
		return "", err
	}
	p.srv.SetNodeDefinition(node, p.bundle, "v1")
	tok := p.srv.NodeToken(node)
	if p.agentOnly {
		go func() { p.done <- serveAgentOnly(p.srv.URL, node, tok) }()
		return node, nil
	}
	go func() {
		_, err := Join(context.Background(), JoinOptions{Server: p.srv.URL, NodeID: node, Token: tok, Dir: p.t.TempDir(), NoSetup: true, Dispatcher: &answers{}})
		p.done <- err
	}()
	return node, nil
}

func serveAgentOnly(server, node, tok string) error {
	ctx := context.Background()
	nc, err := NewNodeClient(server, node, tok)
	if err != nil {
		return err
	}
	defer nc.Close()
	id, err := remotechan.NewIdentity()
	if err != nil {
		return err
	}
	if err := nc.PublishChannelKey(ctx, id.Public()); err != nil {
		return err
	}
	sess, err := remotechan.Join(ctx, nc.Hosted().Relay, node, id)
	if err != nil {
		return err
	}
	defer sess.Close()
	n := NewNode()
	n.Handle(AgentJob, func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil })
	return n.Serve(ctx, sess, Report{Version: "old"})
}

func TestServerSpritesRunsTheCheckOnAJoinedNode(t *testing.T) {
	f := newSpriteFixture(t)
	swappedBundle := bundleOf(t, map[string]string{DefinitionFile: f.swapped})
	dockerBundle := bundleOf(t, map[string]string{DefinitionFile: f.orig})
	for _, c := range []struct {
		name, script string
		bundle       []byte
		agentOnly    bool
		exit         int
		output       string
		err          string
	}{
		{name: "accept", script: "exit 0", bundle: swappedBundle},
		{name: "reject", script: "echo bad; exit 1", bundle: swappedBundle, exit: 1, output: "bad"},
		{name: "other definition", script: "exit 0", bundle: dockerBundle, err: "runs remote integration at definition sha256 "},
		{name: "no gate jobs", script: "exit 0", bundle: swappedBundle, agentOnly: true, err: `unsupported job type "gate.hello"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLocalRepo(t)
			it, err := l.k.Create(item.Fields{Kind: ptr("story"), Title: ptr("check me")}, "ada")
			if err != nil {
				t.Fatal(err)
			}
			p := &spritePlacer{Placer: Placer{Client: l.client, Project: l.project, Repl: l.repl(), ReadyTimeout: 30 * time.Second},
				t: t, srv: l.srv, bundle: c.bundle, agentOnly: c.agentOnly, done: make(chan error, 1)}
			closed := false
			g := GateChecker{Tree: f.tree, DataDir: f.dataDir, Sprites: ServerSprites{Placer: func(context.Context) (NodePlacer, func(), error) {
				return p, func() { closed = true }, nil
			}}}
			out, err := g.RunRemote(testCtx(t), gate.RemoteCheck{Remote: "integration", Gate: "integration-check", Script: c.script,
				Env: gate.CheckEnv{Item: it.ID}, Head: f.head, Timeout: 2 * time.Minute})
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("RunRemote = %+v, %v; want %q", out, err, c.err)
				}
				if c.name == "other definition" {
					sw, or := sha256.Sum256([]byte(f.swapped)), sha256.Sum256([]byte(f.orig))
					for _, d := range [][32]byte{sw, or} {
						if !strings.Contains(err.Error(), hex.EncodeToString(d[:])) {
							t.Errorf("the refusal %q does not name %x", err, d)
						}
					}
				}
			} else {
				if err != nil {
					t.Fatalf("RunRemote = %+v, %v", out, err)
				}
				if out.Exit != c.exit || !strings.HasPrefix(out.Node, "nod") || !strings.Contains(out.Output, c.output) ||
					!strings.Contains(out.Output, "node "+out.Node+" ended") {
					t.Fatalf("outcome = %+v", out)
				}
			}
			select {
			case <-p.done:
			case <-time.After(30 * time.Second):
				t.Fatal("the node kept serving after the check")
			}
			if !closed {
				t.Error("the connection was not released")
			}
		})
	}
}

type loggingHandle struct {
	hostHandle
	mu     sync.Mutex
	log    []string
	failOn string
}

func (l *loggingHandle) record(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log = append(l.log, s)
}

func (l *loggingHandle) Exec(ctx context.Context, argv ...string) ([]byte, error) {
	l.record(strings.Join(argv, " "))
	if l.failOn != "" && argv[0] == l.failOn {
		return []byte("refused by the test node"), errors.New("exit status 1")
	}
	return l.hostHandle.Exec(ctx, argv...)
}

func (l *loggingHandle) CopyIn(ctx context.Context, host, machine string) error {
	l.record("copy-in " + machine)
	return l.hostHandle.CopyIn(ctx, host, machine)
}

type loggingStarter struct {
	h    *loggingHandle
	root string
}

func (s *loggingStarter) StartSprite(context.Context, SpriteStart) (SpriteNode, error) {
	return SpriteNode{Node: "nod_fake", Handle: s.h, Root: s.root}, nil
}

func runLoggedGate(t *testing.T, f spriteFixture, failOn, skillDir, script string) (*loggingStarter, gate.RemoteOutcome, error) {
	t.Helper()
	s := &loggingStarter{h: &loggingHandle{failOn: failOn}, root: filepath.Join(t.TempDir(), "gate")}
	out, err := GateChecker{Tree: f.tree, DataDir: f.dataDir, Sprites: s}.RunRemote(testCtx(t), gate.RemoteCheck{
		Remote: "integration", Gate: "chk", Script: script, Head: f.head,
		Env: gate.CheckEnv{Item: "sto_1", SkillDir: skillDir},
	})
	return s, out, err
}

func TestRemoteGateSkillStagingOrder(t *testing.T) {
	f := newSpriteFixture(t)
	skill := t.TempDir()
	writeSkill(t, skill, map[string]string{"scripts/run.sh": "exit 0\n", "assets/a.txt": "a\n"})
	s, out, err := runLoggedGate(t, f, "", skill, "sh scripts/run.sh")
	if err != nil || out.Exit != 0 {
		t.Fatalf("RunRemote = %+v, %v", out, err)
	}
	nodeSkill := filepath.Join(s.root, "in", gateSkill)
	want := []string{
		"rm -rf " + nodeSkill,
		"copy-in " + filepath.Join(s.root, "in"),
		"find " + nodeSkill + " -type f -exec chmod a-w {} +",
		"git init -q " + filepath.Join(s.root, "work"),
	}
	at := 0
	for _, l := range s.h.log {
		if at < len(want) && l == want[at] {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("the node saw\n%s\nwant in order\n%s", strings.Join(s.h.log, "\n"), strings.Join(want, "\n"))
	}
	err = filepath.WalkDir(nodeSkill, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if fi.Mode()&0o222 != 0 {
				t.Errorf("staged %s is writable: %v", p, fi.Mode())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	s, out, err = runLoggedGate(t, f, "", "", "exit 0")
	if err != nil || out.Exit != 0 {
		t.Fatalf("RunRemote with no skill = %+v, %v", out, err)
	}
	for _, l := range s.h.log {
		if strings.HasPrefix(l, "find ") {
			t.Errorf("a check with no skill sealed one: %q", l)
		}
	}
}

func TestRemoteGateRunsTheSubstrateSkillNotTheHead(t *testing.T) {
	skill := t.TempDir()
	writeSkill(t, skill, map[string]string{
		"scripts/run.sh": "echo \"FROM-SUBSTRATE $SATELLE_SKILL_DIR\"; cat \"$SATELLE_SKILL_DIR/assets/a.txt\"\n",
		"assets/a.txt":   "SUBSTRATE-ASSET\n",
	})
	tampered := func(t *testing.T) spriteFixture {
		f := newSpriteFixture(t)
		writeSkill(t, filepath.Join(f.tree, ".satelle", "skills", "chk"), map[string]string{"scripts/run.sh": "echo TAMPERED\n"})
		gittest.Git(t, f.tree, "add", ".satelle/skills")
		gittest.Git(t, f.tree, "commit", "-q", "-m", "tamper")
		gittest.Git(t, f.tree, "push", "-q", "origin", "main")
		gittest.Git(t, f.tree, "fetch", "-q", "origin")
		f.head.Commit = gittest.Git(t, f.tree, "rev-parse", "HEAD")
		return f
	}
	for _, c := range []struct {
		name     string
		fixture  func(*testing.T) spriteFixture
		tampered bool
	}{
		{"modified", tampered, true},
		{"deleted", newSpriteFixture, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := c.fixture(t)
			s, out, err := runLoggedGate(t, f, "", skill, "sh scripts/run.sh")
			if err != nil || out.Exit != 0 {
				t.Fatalf("RunRemote = %+v, %v", out, err)
			}
			want := "FROM-SUBSTRATE " + filepath.Join(s.root, "in", gateSkill)
			if !strings.Contains(out.Output, want) || !strings.Contains(out.Output, "SUBSTRATE-ASSET") {
				t.Fatalf("output %q lacks %q and the asset", out.Output, want)
			}
			if strings.Contains(out.Output, "/work/") || strings.Contains(out.Output, "TAMPERED") {
				t.Fatalf("the check ran from the head: %q", out.Output)
			}
			if c.tampered {
				b, err := os.ReadFile(filepath.Join(s.root, "work", ".satelle", "skills", "chk", "scripts", "run.sh"))
				if err != nil || string(b) != "echo TAMPERED\n" {
					t.Fatalf("the head's tampered script is not checked out: %q, %v", b, err)
				}
			}
		})
	}

	f := newSpriteFixture(t)
	for _, failOn := range []string{"rm", "find"} {
		s, out, err := runLoggedGate(t, f, failOn, skill, "sh scripts/run.sh")
		if err == nil || !strings.Contains(out.Output, "refused by the test node") {
			t.Fatalf("%s failing: RunRemote = %+v, %v; want the gate failed with the node's output", failOn, out, err)
		}
		wrapper := "sh " + filepath.Join(s.root, "in", gateWrapper)
		for _, l := range s.h.log {
			if strings.HasPrefix(l, wrapper) || strings.HasPrefix(l, "git ") {
				t.Fatalf("%s failing: the node went on to %q", failOn, l)
			}
		}
	}
}
