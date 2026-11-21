package kernel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

const skillRel = "skills/satelle-workflow-standard.md"

func initRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRemovedRecordIsHonoured(t *testing.T) {
	root := initRoot(t)
	for _, rel := range []string{SessionFile, skillRel} {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, InitFile), "category = \"x\"\nremoved = [\""+skillRel+"\"]\n")
	if plan, _ := InitPlan(root); !slices.Equal(plan, []string{SessionFile}) {
		t.Errorf("InitPlan = %v, want only %s", plan, SessionFile)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Errorf("a shipped removal is a problem: %v", errs)
	}
	write(t, filepath.Join(root, InitFile), "category = \"x\"\nremoved = [\""+skillRel+"\"]\nbogus = 1\n")
	if plan, _ := InitPlan(root); !slices.Equal(plan, []string{SessionFile}) {
		t.Errorf("with an unknown key InitPlan = %v, want only %s", plan, SessionFile)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Errorf("an unknown key in init.toml failed Validate: %v", errs)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []workflow.UnknownKey{{File: ".satelle/init.toml", Key: "bogus", Value: "1"}}
	if !slices.Equal(c.UnknownKeys, want) {
		t.Errorf("UnknownKeys = %+v, want %+v", c.UnknownKeys, want)
	}
}

func TestRemovedNamesOnlyShippedFiles(t *testing.T) {
	cases := map[string]string{
		"skills/not-shipped.md": "is not a file this release ships",
		"../x":                  "is not a file this release ships",
		"/etc/passwd":           "is not a file this release ships",
		"":                      "is not a file this release ships",
		SettingsFile:            "which a configured repo keeps",
		InitFile:                "which a configured repo keeps",
	}
	for rel, want := range cases {
		root := initRoot(t)
		write(t, filepath.Join(root, InitFile), "category = \"x\"\nremoved = [\""+rel+"\"]\n")
		errs := Validate(root)
		if !slices.ContainsFunc(errs, func(e error) bool { return strings.Contains(e.Error(), want) }) {
			t.Errorf("removed %q: problems %v, want one with %q", rel, errs, want)
		}
	}
}

func TestInitCategoryIsAnyStringAndNeverAProblem(t *testing.T) {
	root := initRoot(t)
	if err := os.Remove(filepath.Join(root, InitFile)); err != nil {
		t.Fatal(err)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Errorf("no init.toml: %v", errs)
	}
	if p := InitPolicy(root); p.CategorySet() || p.Category != "" {
		t.Errorf("no init.toml: %+v", p)
	}
	for _, body := range []string{"", "category = \"   \"\n"} {
		write(t, filepath.Join(root, InitFile), body)
		if errs := Validate(root); len(errs) != 0 {
			t.Errorf("init.toml %q: %v", body, errs)
		}
		if InitPolicy(root).CategorySet() {
			t.Errorf("init.toml %q sets a category", body)
		}
	}
	for _, cat := range []string{"cat sub's", "Sub/Strate", "a:b"} {
		write(t, filepath.Join(root, InitFile), "category = '''"+cat+"'''\n")
		if got := InitPolicy(root).Category; got != cat {
			t.Errorf("category %q read as %q", cat, got)
		}
		if errs := Validate(root); len(errs) != 0 {
			t.Errorf("category %q: %v", cat, errs)
		}
	}
}
