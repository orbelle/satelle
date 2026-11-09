//go:build !windows

package remotenode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// integrationFile is this repo's docker test-gate remote definition.
var integrationFile = filepath.Join("..", "..", ".satelle", "remotes", "integration", DefinitionFile)

// swapProviderLine replaces only the definition's docker-local provider line
// with fly-sprite and asserts that exactly that one line differs.
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

// spriteFixture is a .satelle directory whose remote integration is this
// repo's file with only its provider line swapped to fly-sprite, and a work
// tree with one commit pushed to a bare origin.
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

// recordingStarter is a SpriteStarter that records each start and runs the
// node on the host, under its own root.
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

// hostHandle is a provider.Handle that runs argv on the host and copies
// files on it.
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

// recordingDocker is a docker CLI that records any call into its log.
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

// The fire: a transition whose gate integration-check is placed
// remote:integration, the integration remote being this repo's file with only
// its provider line swapped to fly-sprite, reaches the fly-sprite start path
// with the same definition and image, never docker, and the check's exit is
// the verdict.
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

// With no SpriteStarter (no bound project) a fly-sprite gate is refused,
// naming the bound project, and no docker call is made.
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

// spritePlacer is the NodePlacer with satelle-server's half played by the
// test: a node StartNode made is served bundle and joined in-process (setup
// skipped: the host is not the image), or, with agentOnly, served by a node
// that registers only AgentJob (a satelle without the gate jobs).
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

// serveAgentOnly serves node with only AgentJob registered.
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

// The production start path against the fake server: ServerSprites starts
// the node through RemoteService.StartNode, the node (satelle node join,
// served the swapped integration definition) answers gate.hello with the
// definition's sha256, and the check runs through the gate jobs. A node
// served another definition, or one without the gate jobs, is refused before
// any check runs, and no verdict comes back.
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
