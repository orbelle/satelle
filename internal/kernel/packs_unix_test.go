//go:build !windows

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestPackMemberSkillEditedWithoutPacksTomlLoadsAndRuns(t *testing.T) {
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
	d1 := &fakeDispatcher{answers: map[string]string{"r1": accept}}
	k1.Dispatcher = d1
	k1.Git = fakeGit{changed: []string{"src/a.go"}}

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
	d2 := &fakeDispatcher{answers: map[string]string{"r1": accept}}
	k2.Dispatcher = d2
	k2.Git = fakeGit{changed: []string{"src/a.go"}}

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
