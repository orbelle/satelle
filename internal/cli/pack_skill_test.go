package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitShipsThePackSkillAndValidates(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	if _, err := os.Stat(filepath.Join(dir, ".satelle", "skills", "satelle-pack.md")); err != nil {
		t.Fatalf("init did not write the pack skill: %v", err)
	}
	if out := must(t, "validate"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("validate after init:\n%s", out)
	}
	if out := must(t, "validate", "--json"); strings.Contains(out, "satelle-pack") {
		t.Errorf("validate --json lists the shipped pack skill:\n%s", out)
	}
}
