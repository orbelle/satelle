package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type packTestGit struct{}

func (packTestGit) Head() (string, error) {
	return "abc123", nil
}

func (packTestGit) ChangedPaths(string) ([]string, error) {
	return []string{"src/a.go"}, nil
}

func (packTestGit) Untracked() ([]string, error) {
	return nil, nil
}

func (packTestGit) Diff(_ string, _ []string) (string, error) {
	return "diff", nil
}

func (packTestGit) StatPaths(string) ([]gitfacts.FileStat, error) {
	return nil, nil
}

type packTestDispatcher struct {
	mu   sync.Mutex
	reqs []agent.Request
}

func (d *packTestDispatcher) Dispatch(_ context.Context, _ agent.Role, req agent.Request) (agent.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = append(d.reqs, req)
	return agent.Result{Text: `{"verdict":"accept","notes":"ok"}`, Model: req.Binding.Model}, nil
}

func TestLoadConfigValidPack(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SkillsDir, "my-pack", workflow.SkillFile), "---\nname: my-pack\ntype: skill\ndescription: wiring guide\n---\n\nWiring guide.\n")
	write(t, filepath.Join(root, SkillsDir, "my-pack", workflow.PackFile), `
name = "my-pack"
version = "1.0.0"
skills = ["my-check", "my-judge"]

[needs]
scripts = ["sh", "jq"]
agents = ["reviewer"]
network = ["api.example.com"]
secrets = ["API_TOKEN"]
`)
	write(t, filepath.Join(root, SkillsDir, "minimal-pack", workflow.SkillFile), "---\nname: minimal-pack\ntype: skill\ndescription: minimal\n---\n\nMinimal.\n")
	write(t, filepath.Join(root, SkillsDir, "minimal-pack", workflow.PackFile), `
name = "minimal-pack"
version = "0.2.0"
skills = ["my-check"]
`)
	write(t, filepath.Join(root, SkillsDir, "my-check", workflow.SkillFile), "---\nname: my-check\ntype: check\ndescription: check\n---\n\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, SkillsDir, "my-judge", workflow.SkillFile), "---\nname: my-judge\ntype: skill\ndescription: judge\n---\n\nJudge rubric.\n")

	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.UnknownKeys) != 0 {
		t.Fatalf("UnknownKeys = %+v, want empty", cfg.UnknownKeys)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("Validate: %v", errs)
	}
	pk, ok := cfg.Packs["my-pack"]
	if !ok {
		t.Fatalf("Packs missing my-pack: %+v", cfg.Packs)
	}
	if pk.Name != "my-pack" || pk.Version != "1.0.0" {
		t.Errorf("pack name/version = %q / %q", pk.Name, pk.Version)
	}
	if !slices.Equal(pk.Skills, []string{"my-check", "my-judge"}) {
		t.Errorf("pack skills = %v", pk.Skills)
	}
	if !slices.Equal(pk.Needs.Scripts, []string{"sh", "jq"}) ||
		!slices.Equal(pk.Needs.Agents, []string{"reviewer"}) ||
		!slices.Equal(pk.Needs.Network, []string{"api.example.com"}) ||
		!slices.Equal(pk.Needs.Secrets, []string{"API_TOKEN"}) {
		t.Errorf("pack needs = %+v", pk.Needs)
	}
	min, ok := cfg.Packs["minimal-pack"]
	if !ok || min.Name != "minimal-pack" || len(min.Needs.Scripts) != 0 {
		t.Errorf("minimal-pack = %+v (ok=%v)", min, ok)
	}
}

func TestPackUnknownKeysNormalizedAndValid(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SkillsDir, "my-pack", workflow.SkillFile), "---\nname: my-pack\ntype: skill\ndescription: wiring\n---\n\nGuide.\n")
	write(t, filepath.Join(root, SkillsDir, "my-pack", workflow.PackFile), `
name = "my-pack"
version = "1.0.0"
skills = ["member-a"]
author = "team-a"

[needs]
scripts = ["sh"]
workflows = ["delivery"]
`)
	write(t, filepath.Join(root, SkillsDir, "member-a", workflow.SkillFile), "---\nname: member-a\ntype: skill\ndescription: a\n---\n\nA.\n")

	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("Validate failed with unknown keys in pack.toml: %v", errs)
	}
	want := []workflow.UnknownKey{
		{File: ".satelle/skills/my-pack/pack.toml", Key: "author", Value: `"team-a"`},
		{File: ".satelle/skills/my-pack/pack.toml", Table: "needs", Key: "workflows", Value: `["delivery"]`},
	}
	if !slices.Equal(cfg.UnknownKeys, want) {
		t.Fatalf("UnknownKeys:\ngot:  %+v\nwant: %+v", cfg.UnknownKeys, want)
	}
}

func TestPackDuplicateNameRefused(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SkillsDir, "member", workflow.SkillFile), "---\nname: member\ntype: skill\ndescription: m\n---\n\nM.\n")
	write(t, filepath.Join(root, SkillsDir, "pack-a", workflow.SkillFile), "---\nname: pack-a\ntype: skill\ndescription: a\n---\n\nA.\n")
	write(t, filepath.Join(root, SkillsDir, "pack-a", workflow.PackFile), "name = \"shared\"\nversion = \"1.0.0\"\nskills = [\"member\"]\n")
	write(t, filepath.Join(root, SkillsDir, "pack-b", workflow.SkillFile), "---\nname: pack-b\ntype: skill\ndescription: b\n---\n\nB.\n")
	write(t, filepath.Join(root, SkillsDir, "pack-b", workflow.PackFile), "name = \"shared\"\nversion = \"2.0.0\"\nskills = [\"member\"]\n")

	errs := Validate(root)
	if len(errs) == 0 {
		t.Fatal("Validate accepted duplicate pack name")
	}
	joined := errors.Join(errs...).Error()
	for _, want := range []string{"shared", filepath.Join("pack-a", workflow.PackFile), filepath.Join("pack-b", workflow.PackFile)} {
		if !strings.Contains(joined, want) {
			t.Errorf("error lacks %q:\n%s", want, joined)
		}
	}

	// Verify Kernel.Validate forwards k.Packs too.
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	k := &Kernel{
		Root: root, Settings: cfg.Settings, Workflows: cfg.Workflows, Rules: cfg.Settings.Workflows,
		Skills: cfg.Skills, Packs: cfg.Packs, Agents: cfg.Agents, Queues: cfg.Queues, Harness: cfg.Harness,
	}
	if kerrs := k.Validate(); len(kerrs) == 0 || !strings.Contains(errors.Join(kerrs...).Error(), "shared") {
		t.Errorf("Kernel.Validate did not report duplicate pack name: %v", kerrs)
	}
}

func TestPackMemberSkillEditedWithoutPacksTomlLoadsAndRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh check script")
	}
	repo := t.TempDir()
	root := filepath.Join(repo, ".satelle")
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"flow\"\n")
	write(t, filepath.Join(root, AgentsFile), `
[agents.r1]
command = "claude"
model = "m1"
tools = ["Read"]
`)
	write(t, filepath.Join(root, WorkflowsDir, "flow.toml"), `
name = "flow"
[states.open]
start = true
[states.done]
terminal = true

[gates.check-gate]
skill = "my-check"

[gates.judge-gate]
skill = "my-judge"
agent = "r1"

[[transition]]
from = ["open"]
to = "done"
groups = [{ mode = "sequential", gates = ["check-gate", "judge-gate"] }]
`)
	write(t, filepath.Join(root, SkillsDir, "my-pack", workflow.SkillFile), "---\nname: my-pack\ntype: skill\ndescription: wiring guide\n---\n\nWiring guide.\n")
	write(t, filepath.Join(root, SkillsDir, "my-pack", workflow.PackFile), `
name = "my-pack"
version = "1.0.0"
skills = ["my-check", "my-judge"]

[needs]
scripts = ["sh"]
agents = ["r1"]
`)
	write(t, filepath.Join(root, SkillsDir, "my-check", workflow.SkillFile), "---\nname: my-check\ntype: check\ndescription: check v1\n---\n\n```sh\nsh scripts/check.sh\n```\n")
	write(t, filepath.Join(root, SkillsDir, "my-check", "scripts", "check.sh"), "echo pack-check-v1\n")
	write(t, filepath.Join(root, SkillsDir, "my-judge", workflow.SkillFile), "---\nname: my-judge\ntype: skill\ndescription: judge v1\n---\n\nRUBRIC-V1\n")

	if _, err := os.Stat(filepath.Join(root, "packs.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected no .satelle/packs.toml, got err=%v", err)
	}

	k1, err := Open(Options{Root: root})
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	if _, ok := k1.Packs["my-pack"]; !ok {
		k1.Close()
		t.Fatalf("k1.Packs missing my-pack: %+v", k1.Packs)
	}
	d1 := &packTestDispatcher{}
	k1.Dispatcher = d1
	k1.Git = packTestGit{}

	s1, err := k1.Create(item.Fields{Kind: ptr("story"), Title: ptr("s1"), Criteria: ptr([]string{"c"})}, "me")
	if err != nil {
		k1.Close()
		t.Fatal(err)
	}
	res1, err := k1.Transition(context.Background(), s1.ID, "done", "me")
	k1.Close()
	if err != nil || !res1.Moved {
		t.Fatalf("v1 transition failed: res=%+v err=%v", res1, err)
	}
	if len(res1.Gates) != 2 || res1.Gates[0].Result != gate.Accept || !strings.Contains(res1.Gates[0].Notes, "pack-check-v1") {
		t.Fatalf("v1 check gate = %+v", res1.Gates)
	}
	if len(d1.reqs) != 1 || !strings.Contains(d1.reqs[0].System, "RUBRIC-V1") {
		t.Fatalf("v1 judge system prompt = %+v", d1.reqs)
	}

	// Modify member skill bytes with no .satelle/packs.toml present.
	write(t, filepath.Join(root, SkillsDir, "my-check", workflow.SkillFile), "---\nname: my-check\ntype: check\ndescription: check v2 edited\n---\n\nEdited body.\n\n```sh\nsh scripts/check.sh\n```\n")
	write(t, filepath.Join(root, SkillsDir, "my-check", "scripts", "check.sh"), "echo pack-check-v2-edited\n")
	write(t, filepath.Join(root, SkillsDir, "my-judge", workflow.SkillFile), "---\nname: my-judge\ntype: skill\ndescription: judge v2 edited\n---\n\nRUBRIC-V2-EDITED\n")

	if _, err := os.Stat(filepath.Join(root, "packs.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected no .satelle/packs.toml after edit, got err=%v", err)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("Validate after editing member skill bytes: %v", errs)
	}
	k2, err := Open(Options{Root: root})
	if err != nil {
		t.Fatalf("Open v2: %v", err)
	}
	defer k2.Close()
	d2 := &packTestDispatcher{}
	k2.Dispatcher = d2
	k2.Git = packTestGit{}

	s2, err := k2.Create(item.Fields{Kind: ptr("story"), Title: ptr("s2"), Criteria: ptr([]string{"c"})}, "me")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := k2.Transition(context.Background(), s2.ID, "done", "me")
	if err != nil || !res2.Moved {
		t.Fatalf("v2 transition failed: res=%+v err=%v", res2, err)
	}
	if len(res2.Gates) != 2 || res2.Gates[0].Result != gate.Accept || !strings.Contains(res2.Gates[0].Notes, "pack-check-v2-edited") {
		t.Fatalf("v2 check gate = %+v", res2.Gates)
	}
	if len(d2.reqs) != 1 || !strings.Contains(d2.reqs[0].System, "RUBRIC-V2-EDITED") {
		t.Fatalf("v2 judge system prompt = %+v", d2.reqs)
	}
}
