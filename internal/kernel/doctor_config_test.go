package kernel

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// doctor.toml loads into Config.Doctor, and each malformed entry is a validate
// problem (sto_3cf973ae).
func TestDoctorConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SkillsDir, "probe.md"), "---\nname: probe\ntype: check\n---\n\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, SkillsDir, "prose.md"), "---\nname: prose\n---\n\nplain\n")
	write(t, filepath.Join(root, DoctorFile), `[[check]]
skill = "probe"
fix = "do it"

[[open]]
name = "drift"
kind = "story"
tag = "drift"
fix = "satelle story get {id}"
`)
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Doctor.Checks) != 1 || c.Doctor.Checks[0].Skill != "probe" || len(c.Doctor.Open) != 1 || c.Doctor.Open[0].Selection().Tag != "drift" {
		t.Fatalf("Doctor = %+v", c.Doctor)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("a good doctor.toml must validate: %v", errs)
	}
	cases := map[string]string{
		"unknown key":   "[[check]]\nskill = \"probe\"\nfix = \"x\"\nbogus = 1\n",
		"missing skill": "[[check]]\nskill = \"nope\"\nfix = \"x\"\n",
		"not a check":   "[[check]]\nskill = \"prose\"\nfix = \"x\"\n",
		"no check fix":  "[[check]]\nskill = \"probe\"\n",
		"no selector":   "[[open]]\nname = \"a\"\nfix = \"x\"\n",
		"no open fix":   "[[open]]\nname = \"a\"\ntag = \"t\"\n",
		"duplicate":     "[[open]]\nname = \"a\"\ntag = \"t\"\nfix = \"x\"\n[[open]]\nname = \"a\"\ntag = \"u\"\nfix = \"x\"\n",
	}
	for name, body := range cases {
		write(t, filepath.Join(root, DoctorFile), body)
		if errs := Validate(root); len(errs) == 0 {
			t.Errorf("%s: validate found no problem in %q", name, body)
		}
	}
}

// init in a repo that already has a satelle.toml writes no starter workflow,
// but still adds the other missing template files (sto_3cf973ae, absorbing
// sto_7e839979 AC1).
func TestInitWritesNoStarterWorkflowIntoAConfiguredRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"delivery\"\n")
	write(t, filepath.Join(root, WorkflowsDir, "delivery.toml"), "name = \"delivery\"\n\n[states.backlog]\nstart = true\n\n[states.done]\nterminal = true\n\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\n")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, WorkflowsDir, "default.toml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote the starter workflow into a configured repo: %v", err)
	}
	for _, f := range []string{AgentsFile, ".gitignore", filepath.Join(SkillsDir, "satelle-upgrade.md")} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("init did not add the missing %s: %v", f, err)
		}
	}
}

// What Init writes is exactly InitPlan's list, in a fresh and a configured
// repo: one rule serves init and doctor.
func TestInitPlanIsWhatInitWrites(t *testing.T) {
	files := func(root string) []string {
		var out []string
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		sort.Strings(out)
		return out
	}
	for _, configured := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), ".satelle")
		if configured {
			write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"delivery\"\n")
			write(t, filepath.Join(root, WorkflowsDir, "delivery.toml"), "name = \"delivery\"\n\n[states.backlog]\nstart = true\n\n[states.done]\nterminal = true\n\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\n")
		}
		before := files(root)
		plan, err := InitPlan(root)
		if err != nil {
			t.Fatal(err)
		}
		_ = Init(root)
		var added []string
		for _, f := range files(root) {
			if !slices.Contains(before, f) {
				added = append(added, f)
			}
		}
		if strings.Join(added, ",") != strings.Join(plan, ",") {
			t.Errorf("configured=%v: init added %v, InitPlan said %v", configured, added, plan)
		}
		if again, _ := InitPlan(root); len(again) != 0 {
			t.Errorf("configured=%v: InitPlan after init = %v, want nothing", configured, again)
		}
	}
}
