package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const skillWarningsWorkflow = `name = "named"

[states.s0]
start = true
[states.end]
terminal = true

[gates.g]
skill = "a"

[[transition]]
from = ["s0"]
to = "end"
groups = [{ mode = "sequential", gates = ["g"] }]
`

func skillWarningsRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func plainSkill(name, body string) string {
	return "---\nname: " + name + "\ntype: skill\ndescription: d\n---\n\n" + body + "\n"
}

func skillWarningNames(t *testing.T, root string) []string {
	t.Helper()
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := c.SkillWarnings()
	if got == nil {
		t.Fatal("SkillWarnings is nil, want a list")
	}
	var names []string
	for _, w := range got {
		names = append(names, w.Skill)
	}
	return names
}

func TestSkillWarningsNameAnUnreferencedSkill(t *testing.T) {
	root := skillWarningsRoot(t)
	write(t, filepath.Join(root, SkillsDir, "orphan.md"), plainSkill("orphan", "body"))
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("an unnamed skill is a problem: %v", errs)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := c.SkillWarnings()
	if len(got) != 1 || got[0].Skill != "orphan" || got[0].File != ".satelle/skills/orphan.md" {
		t.Fatalf("warnings %+v, want one for orphan", got)
	}
	line := got[0].String()
	if !strings.Contains(line, ".satelle/skills/orphan.md") || !strings.Contains(line, "skill orphan") {
		t.Errorf("warning line %q names neither the file nor the skill", line)
	}
}

func TestSkillWarningsFollowSkillPaths(t *testing.T) {
	root := skillWarningsRoot(t)
	write(t, filepath.Join(root, WorkflowsDir, "named.toml"), skillWarningsWorkflow)
	write(t, filepath.Join(root, SkillsDir, "a.md"), "---\nname: a\ntype: check\ndescription: d\n---\n\nSee .satelle/skills/b.md.\n\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, SkillsDir, "b.md"), plainSkill("b", "Then follow skills/c.md."))
	write(t, filepath.Join(root, SkillsDir, "c.md"), plainSkill("c", "body"))
	write(t, filepath.Join(root, SkillsDir, "d.md"), plainSkill("d", "body"))
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("problems: %v", errs)
	}
	if got := skillWarningNames(t, root); strings.Join(got, ",") != "d" {
		t.Fatalf("warnings %v, want only d", got)
	}
}

func TestSkillWarningsReadDoctorAndInject(t *testing.T) {
	root := skillWarningsRoot(t)
	write(t, filepath.Join(root, SkillsDir, "probe.md"), "---\nname: probe\ntype: check\ndescription: d\n---\n\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, SkillsDir, "injected.md"), plainSkill("injected", "body"))
	write(t, filepath.Join(root, DoctorFile), "[[check]]\nskill = \"probe\"\nfix = \"do it\"\n")
	settings := filepath.Join(root, "satelle.toml")
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	const from = "inject = ["
	if !strings.Contains(string(b), from) {
		t.Fatalf("the template satelle.toml has no %q", from)
	}
	write(t, settings, strings.Replace(string(b), from, from+"\".satelle/skills/injected.md\", ", 1))
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("problems: %v", errs)
	}
	if got := skillWarningNames(t, root); len(got) != 0 {
		t.Fatalf("warnings %v, want none", got)
	}
}

func TestFreshInitHasNoSkillWarnings(t *testing.T) {
	root := skillWarningsRoot(t)
	if got := skillWarningNames(t, root); len(got) != 0 {
		t.Fatalf("a fresh init warns on %v", got)
	}
}

func TestSkillWarningsDirectorySkills(t *testing.T) {
	root := skillWarningsRoot(t)
	write(t, filepath.Join(root, WorkflowsDir, "named.toml"), skillWarningsWorkflow)
	write(t, filepath.Join(root, SkillsDir, "a", "SKILL.md"), "---\nname: a\ntype: check\ndescription: d\n---\n\nSee `.satelle/skills/b/SKILL.md`.\n\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, SkillsDir, "b", "SKILL.md"), plainSkill("b", "Then follow `skills/c/SKILL.md`."))
	write(t, filepath.Join(root, SkillsDir, "c", "SKILL.md"), plainSkill("c", "body"))
	write(t, filepath.Join(root, SkillsDir, "probe", "SKILL.md"), "---\nname: probe\ntype: check\ndescription: d\n---\n\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, SkillsDir, "injected", "SKILL.md"), plainSkill("injected", "body"))
	write(t, filepath.Join(root, SkillsDir, "orphan-dir", "SKILL.md"), plainSkill("orphan-dir", "body"))
	write(t, filepath.Join(root, DoctorFile), "[[check]]\nskill = \"probe\"\nfix = \"do it\"\n")
	settings := filepath.Join(root, SettingsFile)
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	const from = "inject = ["
	write(t, settings, strings.Replace(string(b), from, from+"\".satelle/skills/injected/SKILL.md\", ", 1))
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("problems: %v", errs)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := c.SkillWarnings()
	if len(got) != 1 || got[0].Skill != "orphan-dir" || got[0].File != ".satelle/skills/orphan-dir/SKILL.md" {
		t.Fatalf("warnings %+v, want only orphan-dir at .satelle/skills/orphan-dir/SKILL.md", got)
	}
}

func TestSkillWarningsReachPackEntryAndMembers(t *testing.T) {
	root := skillWarningsRoot(t)
	write(t, filepath.Join(root, SkillsDir, "my-pack", "SKILL.md"), plainSkill("my-pack", "Wiring guide."))
	write(t, filepath.Join(root, SkillsDir, "my-pack", "pack.toml"), "name = \"my-pack\"\nversion = \"1.0.0\"\nskills = [\"member-a\", \"member-b\"]\n")
	write(t, filepath.Join(root, SkillsDir, "member-a", "SKILL.md"), plainSkill("member-a", "Member A."))
	write(t, filepath.Join(root, SkillsDir, "member-b", "SKILL.md"), plainSkill("member-b", "Member B."))
	write(t, filepath.Join(root, SkillsDir, "orphan", "SKILL.md"), plainSkill("orphan", "Orphan."))
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	if got := skillWarningNames(t, root); len(got) != 1 || got[0] != "orphan" {
		t.Fatalf("warned skills %v, want only orphan", got)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := c.SkillWarnings()
	if got[0].File != ".satelle/skills/orphan/SKILL.md" {
		t.Errorf("orphan warning file %q", got[0].File)
	}
	if !strings.Contains(got[0].String(), "pack.toml") {
		t.Errorf("warning line %q does not name pack.toml as a namer", got[0].String())
	}
}
