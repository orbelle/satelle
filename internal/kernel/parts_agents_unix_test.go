//go:build !windows

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const sharedAgents = `[agents.coder]
command = "claude"
model = "shared-coder"
tools = ["Read", "Edit", "Bash"]
[agents.reviewer]
command = "claude"
model = "shared-rev"
tools = ["Read"]
[agents.helper]
command = "claude"
model = "shared-helper"
tools = ["Read"]
`

const partAgentsFile = `[agents.coder]
command = "claude"
model = "x-coder"
tools = ["Read", "Edit", "Bash"]
[agents.reviewer]
command = "claude"
model = "x-rev"
tools = ["Read"]
`

const partAgentsInfra = `[states.intake]
start = true
performer = { agent = "coder", skill = "do" }
[states.build]
performer = { agent = "helper", skill = "do" }
[states.shipped]
terminal = true
[gates.review]
skill = "review"
agent = "reviewer"
[[transition]]
from = ["intake"]
to = "build"
groups = [{ mode = "sequential", gates = ["review"] }]
[[transition]]
from = ["build"]
to = "shipped"
`

const sharedFlow = `[states.open]
start = true
performer = { agent = "coder", skill = "do" }
[states.closed]
terminal = true
[gates.review]
skill = "review"
agent = "reviewer"
[[transition]]
from = ["open"]
to = "closed"
groups = [{ mode = "sequential", gates = ["review"] }]
`

func partAgentsRoot(t *testing.T, partAgents string, withPart bool) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"flow\"\n")
	write(t, filepath.Join(root, AgentsFile), sharedAgents)
	write(t, filepath.Join(root, SkillsDir, "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, SkillsDir, "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, WorkflowsDir, "flow.toml"), sharedFlow)
	write(t, filepath.Join(root, WorkflowsDir, "xflow.toml"), "name = \"x/flow\"\n"+sharedFlow)
	write(t, filepath.Join(root, reporeg.TrackedFile), "[repos.x]\norigin = \"https://github.com/o/x\"\n")
	hostGitFloor(t)
	tgt := t.TempDir()
	gittest.Git(t, tgt, "init", "-q", "-b", "main")
	gittest.Git(t, tgt, "remote", "add", "origin", "https://github.com/o/x")
	gittest.Git(t, tgt, "update-ref", "refs/remotes/origin/main", commitFile(t, tgt, "a.go", "package a\n"))
	write(t, filepath.Join(root, reporeg.LocalFile), "[repos.x]\npath = "+strconv.Quote(tgt)+"\n")
	if withPart {
		write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n")
		write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"), partAgentsInfra)
	}
	if partAgents != "" {
		write(t, filepath.Join(root, PartsDir, "x", AgentsFile), partAgents)
	}
	return root
}

func openPartAgents(t *testing.T, root string) (*Kernel, *fakeDispatcher) {
	t.Helper()
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"coder": "did", "helper": "did", "reviewer": accept}}
	k.Dispatcher = d
	k.Git = fakeGit{changed: []string{"src/a.go"}}
	return k, d
}

func lastModel(t *testing.T, d *fakeDispatcher) (string, string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reqs) == 0 {
		t.Fatal("nothing was dispatched")
	}
	return d.calls[len(d.calls)-1], d.reqs[len(d.reqs)-1].Binding.Model
}

func samePointer(a, b agent.Catalog) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

func performAndCross(t *testing.T, k *Kernel, d *fakeDispatcher, id, to, wantPerf, wantJudge string) {
	t.Helper()
	ctx := context.Background()
	if _, err := k.Seat(id, "s1", "local:"+filepath.Dir(k.Root), "", "me"); err != nil {
		t.Fatalf("seat: %v", err)
	}
	if _, err := k.Perform(ctx, id, "me"); err != nil {
		t.Fatalf("perform: %v", err)
	}
	if call, model := lastModel(t, d); call != "perform:coder" || model != wantPerf {
		t.Errorf("performer dispatched %s %s, want perform:coder %s", call, model, wantPerf)
	}
	res, err := k.Transition(ctx, id, to, "me")
	if err != nil || !res.Moved {
		t.Fatalf("transition to %s: %+v %v", to, res, err)
	}
	if call, model := lastModel(t, d); call != "judge:reviewer" || model != wantJudge {
		t.Errorf("judge dispatched %s %s, want judge:reviewer %s", call, model, wantJudge)
	}
	if len(res.Gates) != 1 || res.Gates[0].Binding != "reviewer" {
		t.Errorf("gate results %+v", res.Gates)
	}
}

func TestPartAgentsBindItsStatesAndGates(t *testing.T) {
	root := partAgentsRoot(t, partAgentsFile, true)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.Parts["x"].Problems; len(ps) > 0 {
		t.Fatalf("part problems: %v", errors.Join(ps...))
	}
	k, d := openPartAgents(t, root)
	if _, ok := k.Workflows["x/infra"]; !ok {
		t.Fatal("x/infra is not loaded")
	}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me")
	if err != nil || it.Workflow != "x/infra" {
		t.Fatalf("create %+v %v", it, err)
	}
	performAndCross(t, k, d, it.ID, "build", "x-coder", "x-rev")
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if call, model := lastModel(t, d); call != "perform:helper" || model != "shared-helper" {
		t.Errorf("build dispatched %s %s, want perform:helper shared-helper", call, model)
	}

	self, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s")}, "me")
	if err != nil || self.Workflow != "flow" {
		t.Fatalf("create self %+v %v", self, err)
	}
	performAndCross(t, k, d, self.ID, "closed", "shared-coder", "shared-rev")

	onShared, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("x"), Repo: ptr("x"), Workflow: ptr("flow")}, "me")
	if err != nil || onShared.Workflow != "flow" {
		t.Fatalf("create x on flow %+v %v", onShared, err)
	}
	performAndCross(t, k, d, onShared.ID, "closed", "shared-coder", "shared-rev")

	named := k.Workflows["x/flow"]
	if named == nil || named.Part != "" {
		t.Fatalf("shared x/flow: %+v", named)
	}
	if !samePointer(k.AgentsFor(named), k.Agents) {
		t.Error("AgentsFor a shared workflow named x/flow is not the shared catalog")
	}
	lookalike, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("l"), Repo: ptr("x"), Workflow: ptr("x/flow")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	performAndCross(t, k, d, lookalike.ID, "closed", "shared-coder", "shared-rev")

	if errs := Validate(root); len(errs) > 0 {
		t.Errorf("validate: %v", errors.Join(errs...))
	}
	if errs := k.Validate(); len(errs) > 0 {
		t.Errorf("kernel validate: %v", errors.Join(errs...))
	}
}

func TestPartAgentsFallbackToShared(t *testing.T) {
	root := partAgentsRoot(t, partAgentsFile+"fallback = \"helper\"\n", true)
	k, d := openPartAgents(t, root)
	d.answers = map[string]string{"coder": "did", "helper": accept}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(context.Background(), it.ID, "build", "me")
	if err != nil || !res.Moved {
		t.Fatalf("transition %+v %v", res, err)
	}
	if call, model := lastModel(t, d); call != "judge:helper" || model != "shared-helper" {
		t.Errorf("fallback dispatched %s %s", call, model)
	}
	if d.reqs[len(d.reqs)-2].Binding.Model != "x-rev" {
		t.Errorf("the part reviewer was not asked first: %+v", d.calls)
	}
}

func TestPartAgentsEmptyFile(t *testing.T) {
	root := partAgentsRoot(t, "", true)
	write(t, filepath.Join(root, PartsDir, "x", AgentsFile), "")
	write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"), sharedFlow)
	write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n")
	k, _ := openPartAgents(t, root)
	w := k.Workflows["x/infra"]
	if w == nil {
		t.Fatal("x/infra is not loaded")
	}
	if !samePointer(k.AgentsFor(w), k.Agents) {
		t.Error("an empty part agents.toml overlays the shared catalog")
	}
}

func TestPartAgentsUnresolved(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.toml")
	write(t, outside, "[agents.secretbinding]\ncommand = \"claude\"\nmodel = \"leak\"\n")
	for _, c := range []struct {
		name, agents, want string
		link               bool
	}{
		{"names an unbound agent", partAgentsFile, `"nobody"`, false},
		{"invalid toml", "[agents.coder\n", "agents.toml", false},
		{"unknown key", partAgentsFile + "bogus = 1\n", "unknown key", false},
		{"bad binding", "[agents.coder]\ncommand = \"claude\"\ntools = [\"Nope\"]\n" + partAgentsFile[strings.Index(partAgentsFile, "[agents.reviewer]"):], "agents.coder", false},
		{"unbound fallback", partAgentsFile + "fallback = \"nobody\"\n", "nobody", false},
		{"symlink", "", "agents.toml", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := partAgentsRoot(t, c.agents, true)
			if c.name == "names an unbound agent" {
				write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"), strings.Replace(partAgentsInfra, `agent = "helper"`, `agent = "nobody"`, 1))
			}
			if c.link {
				if err := os.Symlink(outside, filepath.Join(root, PartsDir, "x", AgentsFile)); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if ps := cfg.Problems(); len(ps) > 0 {
				t.Fatalf("shared problems: %v", errors.Join(ps...))
			}
			ps := cfg.Parts["x"].Problems
			msg := errors.Join(ps...)
			if len(ps) == 0 || !strings.Contains(msg.Error(), c.want) {
				t.Fatalf("part problems %v, want %q", msg, c.want)
			}
			if strings.Contains(msg.Error(), "leak") || strings.Contains(msg.Error(), "secretbinding") {
				t.Errorf("outside content leaked: %v", msg)
			}
			if _, ok := cfg.PartAgents["x"]; ok {
				t.Error("an unresolved part keeps its agents")
			}
			k, _ := openPartAgents(t, root)
			var u *PartUnresolved
			if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me"); !errors.As(err, &u) {
				t.Errorf("create on an unresolved part: %v", err)
			}
		})
	}
}

func TestPartAgentsBindingNames(t *testing.T) {
	for _, name := range []string{"../escape", "a/b", ".hidden", "Upper"} {
		t.Run(name, func(t *testing.T) {
			root := partAgentsRoot(t, partAgentsFile+"[agents."+strconv.Quote(name)+"]\ncommand = \"claude\"\nmodel = \"m\"\ntools = [\"Read\"]\n", true)
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatal(err)
			}
			msg := errors.Join(cfg.Parts["x"].Problems...)
			if msg == nil || !strings.Contains(msg.Error(), "agents."+strconv.Quote(name)+": an agent name is") {
				t.Fatalf("part problems %v", msg)
			}
			if _, ok := cfg.PartAgents["x"]; ok {
				t.Error("a part with a bad binding name keeps its agents")
			}
			k, _ := openPartAgents(t, root)
			var u *PartUnresolved
			if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me"); !errors.As(err, &u) {
				t.Errorf("create: %v", err)
			}
			for _, dir := range []string{k.Logs, filepath.Dir(k.Logs)} {
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					if strings.Contains(e.Name(), "escape") || strings.Contains(e.Name(), "hidden") {
						t.Errorf("a file was written for the bad name: %s", filepath.Join(dir, e.Name()))
					}
				}
			}
		})
	}
	root := partAgentsRoot(t, partAgentsFile+"[agents.\"reviewer-grok-4.7\"]\ncommand = \"grok\"\nmodel = \"grok-4.7\"\ntools = [\"Read\"]\n", true)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.Parts["x"].Problems; len(ps) > 0 || !cfg.PartAgents["x"].Has("reviewer-grok-4.7") {
		t.Errorf("reviewer-grok-4.7: %v", errors.Join(ps...))
	}
}

func TestNoPartAgentsResolvesShared(t *testing.T) {
	t.Run("no repos directory", func(t *testing.T) {
		root := partAgentsRoot(t, "", false)
		k, d := openPartAgents(t, root)
		if len(k.PartAgents) != 0 {
			t.Fatalf("part agents %v", k.PartAgents)
		}
		for _, w := range []*workflow.Workflow{nil, k.Workflows["flow"]} {
			if !samePointer(k.AgentsFor(w), k.Agents) {
				t.Errorf("AgentsFor(%v) is not the shared catalog", w)
			}
		}
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a")}, "me")
		if err != nil {
			t.Fatal(err)
		}
		performAndCross(t, k, d, it.ID, "closed", "shared-coder", "shared-rev")
	})
	t.Run("part with no agents.toml", func(t *testing.T) {
		root := partAgentsRoot(t, "", true)
		k, d := openPartAgents(t, root)
		w := k.Workflows["x/infra"]
		if w == nil || len(k.PartAgents) != 0 {
			t.Fatalf("x/infra %v, part agents %v", w, k.PartAgents)
		}
		if !samePointer(k.AgentsFor(w), k.Agents) {
			t.Error("AgentsFor(x/infra) is not the shared catalog")
		}
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me")
		if err != nil {
			t.Fatal(err)
		}
		performAndCross(t, k, d, it.ID, "build", "shared-coder", "shared-rev")
	})
}
