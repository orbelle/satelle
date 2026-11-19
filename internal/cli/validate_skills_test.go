package cli

import (
	"encoding/json"
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
