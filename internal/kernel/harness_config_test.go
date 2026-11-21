package kernel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestHarnessLimitsLoadAndValidate(t *testing.T) {
	root, c := initVolatile(t)
	if len(c.Harness) != 0 {
		t.Fatalf("fresh init has harness limits %v", c.Harness)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("no %s: %v", HarnessFile, errs)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, HarnessFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[claude]\ncontext_limit_bytes = 9000\n\n[grok]\ncontext_limit_bytes = 1\n")
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("a good %s: %v", HarnessFile, errs)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness["claude"].ContextLimitBytes != 9000 || c.Harness["grok"].ContextLimitBytes != 1 || len(c.Harness) != 2 {
		t.Errorf("loaded %v", c.Harness)
	}
	for name, body := range map[string]string{
		"a table that is not a harness": "[emacs]\ncontext_limit_bytes = 10\n",
		"a zero limit":                  "[claude]\ncontext_limit_bytes = 0\n",
		"a negative limit":              "[claude]\ncontext_limit_bytes = -4\n",
		"a table with no limit":         "[claude]\n",
	} {
		write(body)
		errs := Validate(root)
		if len(errs) == 0 {
			t.Errorf("%s: no problem", name)
			continue
		}
		joined := ""
		for _, e := range errs {
			joined += e.Error() + "\n"
		}
		if !strings.Contains(joined, HarnessFile) {
			t.Errorf("%s: the problem does not name %s:\n%s", name, HarnessFile, joined)
		}
	}
	write("[claude]\ncontext_limit_bytes = 10\nlimit = 3\n")
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("unknown key in %s failed Validate: %v", HarnessFile, errs)
	}
	c, err = LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness["claude"].ContextLimitBytes != 10 {
		t.Errorf("ContextLimitBytes = %d, want 10", c.Harness["claude"].ContextLimitBytes)
	}
	want := []workflow.UnknownKey{{File: ".satelle/harness.toml", Table: "claude", Key: "limit", Value: "3"}}
	if !slices.Equal(c.UnknownKeys, want) {
		t.Errorf("UnknownKeys = %+v, want %+v", c.UnknownKeys, want)
	}
}
