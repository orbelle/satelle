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

[[items]]
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
		"no selector":   "[[items]]\nname = \"a\"\nfix = \"x\"\n",
		"no open fix":   "[[items]]\nname = \"a\"\ntag = \"t\"\n",
		"duplicate":     "[[items]]\nname = \"a\"\ntag = \"t\"\nfix = \"x\"\n[[items]]\nname = \"a\"\ntag = \"u\"\nfix = \"x\"\n",
	}
	for name, body := range cases {
		write(t, filepath.Join(root, DoctorFile), body)
		if errs := Validate(root); len(errs) == 0 {
			t.Errorf("%s: validate found no problem in %q", name, body)
		}
	}
}

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
	plan, _ := InitPlan(root)
	for _, f := range []string{AgentsFile, ".gitignore", "README.md", "skills/satelle-upgrade.md", "principles/satelle-reference-not-restate.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); !os.IsNotExist(err) {
			t.Errorf("init wrote %s into a configured repo: %v", f, err)
		}
		if !slices.Contains(plan, f) {
			t.Errorf("InitPlan does not list the missing %s: %v", f, plan)
		}
		if _, err := Shipped(f); err != nil {
			t.Errorf("Shipped(%s): %v", f, err)
		}
	}
}

func TestInitLeavesAnExistingReadmeAlone(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || !strings.Contains(string(b), "satelle doctor") {
		t.Fatalf("init wrote no README naming satelle doctor: %v\n%s", err, b)
	}
	own := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(own, "README.md"), "# ours\n")
	if err := Init(own); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "README.md")); string(b) != "# ours\n" {
		t.Fatalf("init changed an existing README:\n%s", b)
	}
}

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
		if configured {
			if len(added) != 0 {
				t.Errorf("init added %v to a configured root", added)
			}
			if again, _ := InitPlan(root); strings.Join(again, ",") != strings.Join(plan, ",") {
				t.Errorf("InitPlan after init = %v, want %v unchanged", again, plan)
			}
			continue
		}
		if strings.Join(added, ",") != strings.Join(plan, ",") {
			t.Errorf("init added %v, InitPlan said %v", added, plan)
		}
		if again, _ := InitPlan(root); len(again) != 0 {
			t.Errorf("InitPlan after init = %v, want nothing", again)
		}
	}
}

func TestDoctorResolveConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body string
		sel        string
		unset      []string
	}{
		{"absent file", "-", "  ", []string{"kind", "tag"}},
		{"empty file", "", "  ", []string{"kind", "tag"}},
		{"empty [resolve]", "[resolve]\n", "  ", []string{"kind", "tag"}},
		{"kind only", "[resolve]\nkind = \"task\"\n", "task  ", []string{"tag"}},
		{"tag only", "[resolve]\ntag = \"fix-me\"\n", " fix-me ", []string{"kind"}},
		{"all three", "[resolve]\nkind = \"task\"\ntag = \"fix-me\"\ncategory = \"chore\"\n", "task fix-me chore", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(root, DoctorFile)
			if tc.body == "-" {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			} else {
				write(t, p, tc.body)
			}
			c, err := LoadConfig(root)
			if err != nil {
				t.Fatalf("%q: %v", tc.body, err)
			}
			s := c.Doctor.Resolve.Selection()
			if got := s.Kind + " " + s.Tag + " " + s.Category; got != tc.sel || s.Status != "" {
				t.Errorf("selection %+v, want %q", s, tc.sel)
			}
			if got := c.Doctor.Resolve.Unset(); !slices.Equal(got, tc.unset) {
				t.Errorf("Unset() = %v, want %v", got, tc.unset)
			}
		})
	}
	write(t, filepath.Join(root, DoctorFile), "[resolve]\nstatus = \"backlog\"\n")
	if _, err := LoadConfig(root); err == nil {
		t.Error("a [resolve] status key loaded")
	}
}

func TestInitWritesTheResolveSelection(t *testing.T) {
	shipped, err := fs.ReadFile(template, "template/"+DoctorFile)
	if err != nil {
		t.Fatalf("the template ships no %s: %v", DoctorFile, err)
	}
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, DoctorFile))
	if err != nil || string(got) != string(shipped) {
		t.Fatalf("init wrote %q (%v), want the embedded template", got, err)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("fresh init does not validate: %v", errs)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if u := c.Doctor.Resolve.Unset(); len(u) != 0 {
		t.Fatalf("fresh init leaves [resolve] %v unset", u)
	}
	if err := os.Remove(filepath.Join(root, DoctorFile)); err != nil {
		t.Fatal(err)
	}
	plan, err := InitPlan(root)
	if err != nil || !slices.Contains(plan, DoctorFile) {
		t.Fatalf("InitPlan %v (%v) lacks %s", plan, err, DoctorFile)
	}
}

func TestNoResolveDefaultIsAGoLiteral(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	kind, tag := c.Doctor.Resolve.Kind, c.Doctor.Resolve.Tag
	if kind == "" || tag == "" {
		t.Fatalf("the template ships kind %q tag %q", kind, tag)
	}
	module := filepath.Join("..", "..")
	err = filepath.WalkDir(module, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || (n == ".satelle" && p != module) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), tag) {
			t.Errorf("%s holds the shipped [resolve] tag %q", p, tag)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		filepath.Join(module, "internal", "kernel", "config.go"),
		filepath.Join(module, "internal", "cli", "doctor.go"),
		filepath.Join(module, "internal", "cli", "doctor_resolve.go"),
		filepath.Join(module, "internal", "cli", "cmd_doctor.go"),
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"`+kind+`"`) {
			t.Errorf("%s holds the shipped [resolve] kind as the literal %q", f, `"`+kind+`"`)
		}
	}
}

func TestInitWritesTheSessionGuidance(t *testing.T) {
	shipped, err := fs.ReadFile(template, "template/"+SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(root, SessionFile))
	if err != nil || string(written) != string(shipped) {
		t.Fatalf("init did not write the embedded %s (%v)", SessionFile, err)
	}
	if err := os.Remove(filepath.Join(root, SessionFile)); err != nil {
		t.Fatal(err)
	}
	plan, err := InitPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan, SessionFile) {
		t.Errorf("InitPlan for a configured repo lacking %s: %v", SessionFile, plan)
	}
}
