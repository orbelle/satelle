package kernel

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

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

func TestValidateWithSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, DoctorFile), "[[check]]\nskill = \"extra\"\nfix = \"do it\"\n")
	errs := Validate(root)
	if len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), `"extra"`) {
		t.Fatalf("Validate = %v, want a problem naming extra", errs)
	}
	prospect := filepath.Join(t.TempDir(), SkillsDir)
	write(t, filepath.Join(prospect, "extra.md"), "---\nname: extra\ntype: check\n---\n\n```sh\nexit 0\n```\n")
	if errs := ValidateWithSkills(root, prospect); len(errs) != 0 {
		t.Fatalf("ValidateWithSkills = %v, want none", errs)
	}
	if errs := Validate(root); len(errs) == 0 {
		t.Fatal("Validate passed after ValidateWithSkills; the real skills dir must still lack extra")
	}
}
