package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestValidateListsAnUnreferencedSkill(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	out := must(t, "validate")
	if !strings.Contains(out, "configuration valid") || strings.Contains(out, "skills nothing names") {
		t.Fatalf("validate after init:\n%s", out)
	}
	if out := must(t, "validate", "--json"); !strings.Contains(out, `"skill_warnings": []`) {
		t.Fatalf("validate --json after init lacks an empty skill_warnings:\n%s", out)
	}

	writeFile(t, filepath.Join(dir, ".satelle", "skills", "orphan.md"), "---\nname: orphan\ntype: skill\ndescription: named by nothing\n---\n\nbody\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "README.md"), "# skills\n\nNot a skill.\n")
	out, err := run(t, "", "validate")
	if err != nil {
		t.Fatalf("validate fails on an unnamed skill: %v\n%s", err, out)
	}
	if !strings.Contains(out, "configuration valid") {
		t.Errorf("validate does not say the configuration is valid:\n%s", out)
	}
	found := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "skills/orphan.md") && strings.Contains(line, "skill orphan") {
			found++
		}
		if strings.Contains(line, "README") {
			t.Errorf("validate names the README: %q", line)
		}
	}
	if found != 1 {
		t.Errorf("validate prints %d lines naming skills/orphan.md and skill orphan, want 1:\n%s", found, out)
	}

	var rep struct {
		Valid         bool                  `json:"valid"`
		SkillWarnings []kernel.SkillWarning `json:"skill_warnings"`
	}
	if err := json.Unmarshal([]byte(must(t, "validate", "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Valid || len(rep.SkillWarnings) != 1 || rep.SkillWarnings[0].Skill != "orphan" {
		t.Errorf("validate --json: %+v", rep)
	}
}

func TestValidateDirectorySkills(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeFile(t, filepath.Join(dir, ".satelle", "agents.toml"), "[agents.dev]\ncommand = \"session\"\n")
	writeFile(t, filepath.Join(dir, ".satelle", "workflows", "dirwf.toml"), `name = "dirwf"

[states.s0]
start = true
performer = { agent = "dev", skill = "dir-perform" }

[states.done]
terminal = true

[gates.g]
skill = "dir-gate"

[[transition]]
from = ["s0"]
to = "done"
groups = [{ mode = "sequential", gates = ["g"] }]
`)
	writeFile(t, filepath.Join(dir, ".satelle", "doctor.toml"), "[[check]]\nskill = \"dir-doctor\"\nfix = \"fix it\"\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dir-perform", "SKILL.md"), "---\nname: dir-perform\ntype: skill\ndescription: performer dir skill\n---\n\nFollow the plan.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dir-gate", "SKILL.md"), "---\nname: dir-gate\ntype: check\ndescription: gate dir skill\n---\n\n```sh\nsh scripts/gate.sh\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dir-gate", "scripts", "gate.sh"), "exit 0\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dir-doctor", "SKILL.md"), "---\nname: dir-doctor\ntype: check\ndescription: doctor dir skill\n---\n\n```sh\nsh scripts/doc.sh\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dir-doctor", "scripts", "doc.sh"), "exit 0\n")

	out := must(t, "validate")
	if !strings.Contains(out, "configuration valid") || strings.Contains(out, "skills nothing names") {
		t.Fatalf("validate with directory skills:\n%s", out)
	}

	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dir-perform", "SKILL.md"), "---\nname: dir-perform\ntype: skill\ndescription: performer dir skill\n---\n\nRun `satelle nosuch`.\n")
	if out, err := run(t, "", "validate"); err == nil || !strings.Contains(err.Error(), ".satelle/skills/dir-perform/SKILL.md") {
		t.Fatalf("validate missed substrate finding in SKILL.md: err=%v\n%s", err, out)
	}
}

func TestValidateDuplicateSkill(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dup.md"), "---\nname: dup\ntype: skill\ndescription: file\n---\n\nfile\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "dup", "SKILL.md"), "---\nname: dup\ntype: skill\ndescription: dir\n---\n\ndir\n")

	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatalf("validate accepted duplicate skill:\n%s", out)
	}
	for _, want := range []string{"dup", "skills/dup.md", "skills/dup/SKILL.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate error lacks %q:\n%v", want, err)
		}
	}

	raw, err := run(t, "", "validate", "--json")
	if err == nil {
		t.Fatalf("validate --json accepted duplicate skill:\n%s", raw)
	}
	var rep struct {
		Valid    bool     `json:"valid"`
		Problems []string `json:"problems"`
	}
	if jerr := json.Unmarshal([]byte(raw), &rep); jerr != nil {
		t.Fatalf("validate --json unmarshal: %v\n%s", jerr, raw)
	}
	joined := strings.Join(rep.Problems, "\n")
	if rep.Valid {
		t.Errorf("validate --json reported valid=true")
	}
	for _, want := range []string{"dup", "skills/dup.md", "skills/dup/SKILL.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("validate --json problems lack %q:\n%s", want, joined)
		}
	}
}

func TestValidateSkillDirectoryMissingPaths(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "missing-own", "SKILL.md"), "---\nname: missing-own\ntype: check\ndescription: missing paths\n---\n\nRead `assets/missing.txt`.\n\n```sh\nsh scripts/nope.sh\n```\n")

	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatalf("validate accepted missing own paths:\n%s", out)
	}
	for _, want := range []string{"missing-own", "scripts/nope.sh", "assets/missing.txt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate error lacks %q:\n%v", want, err)
		}
	}
}

func TestValidateSkillDirectoryForeignPaths(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "bad-foreign", "SKILL.md"), "---\nname: bad-foreign\ntype: check\ndescription: foreign paths\n---\n\nSee `.satelle/scripts/shared.sh`.\n\n```sh\nsh .satelle/skills/other/scripts/helper.sh\n```\n")

	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatalf("validate accepted foreign paths:\n%s", out)
	}
	for _, want := range []string{"bad-foreign", ".satelle/scripts/shared.sh", ".satelle/skills/other/scripts/helper.sh", "not its own"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate error lacks %q:\n%v", want, err)
		}
	}
}

func TestInitPreservesEditedSkillDirectoryCLI(t *testing.T) {
	const edited = "---\nname: satelle-workflow-standard\ntype: skill\ndescription: repo edited\n---\n\nStandard: repo-standard.md\n"
	t.Run("unconfigured", func(t *testing.T) {
		dir := isolate(t)
		p := filepath.Join(dir, ".satelle", "skills", "satelle-workflow-standard", "SKILL.md")
		writeFile(t, p, edited)
		must(t, "init")
		if b, err := os.ReadFile(p); err != nil || string(b) != edited {
			t.Fatalf("SKILL.md changed on first init: %q (%v)", b, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".satelle", "skills", "satelle-workflow-standard.md")); !os.IsNotExist(err) {
			t.Fatalf("init wrote duplicate single-file skill: %v", err)
		}
	})
	t.Run("configured", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		if err := os.Remove(filepath.Join(dir, ".satelle", "skills", "satelle-workflow-standard.md")); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, ".satelle", "skills", "satelle-workflow-standard", "SKILL.md")
		writeFile(t, p, edited)
		must(t, "init")
		if b, err := os.ReadFile(p); err != nil || string(b) != edited {
			t.Fatalf("SKILL.md changed on configured init: %q (%v)", b, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".satelle", "skills", "satelle-workflow-standard.md")); !os.IsNotExist(err) {
			t.Fatalf("configured init wrote duplicate single-file skill: %v", err)
		}
	})
}
