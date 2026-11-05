package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// build-version.sh prints the base on the clean tagged commit and marks any
// other build +<sha12>, and -dirty on a dirty tree.
func TestBuildVersionMarksDevBuilds(t *testing.T) {
	script, err := filepath.Abs("build-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stamp := func() string {
		t.Helper()
		cmd := exec.Command("bash", script, "1.0.0")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), gittest.Env()...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("build-version.sh: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gittest.Git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", "f")
	gittest.Git(t, dir, "commit", "-q", "-m", "one")
	gittest.Git(t, dir, "tag", "v1.0.0")
	if got := stamp(); got != "1.0.0" {
		t.Errorf("on the tagged commit: %q, want 1.0.0", got)
	}
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "two")
	sha := gittest.Git(t, dir, "rev-parse", "--short=12", "HEAD")
	if got := stamp(); got != "1.0.0+"+sha {
		t.Errorf("after the tag: %q, want 1.0.0+%s", got, sha)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stamp(); got != "1.0.0+"+sha+"-dirty" {
		t.Errorf("dirty tree: %q, want 1.0.0+%s-dirty", got, sha)
	}
}
