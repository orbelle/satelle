package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/kernel"
)

// preV1Repo writes a pre-1.0 .satelle into dir (a git repo), as
// tests/upgrade_fixture_test.go does: a satelle.toml whose [review] table v1
// does not know, deployed.version and workflows/step.toml. It returns the
// markers kernel.LegacyMarkers finds.
func preV1Repo(t *testing.T, dir string) []string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		gittest.Git(t, dir, "init", "-q")
	}
	for name, body := range map[string]string{
		"satelle.toml":        "[review]\ngate_create = true\n",
		"deployed.version":    "satelle.version: 0.0.879\n",
		"workflows/step.toml": "[meta]\nname = \"step\"\n",
	} {
		p := filepath.Join(dir, ".satelle", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := kernel.LegacyMarkers(filepath.Join(dir, ".satelle"))
	if len(m) == 0 {
		t.Fatal("the fixture is not pre-1.0")
	}
	return m
}

// newPreV1Repo is preV1Repo in a fresh temp dir, symlinks resolved.
func newPreV1Repo(t *testing.T) (string, []string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir, preV1Repo(t, dir)
}

// registerProjects adds paths to the isolated home's [projects] paths.
func registerProjects(t *testing.T, paths ...string) {
	t.Helper()
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		gc.Projects.Add(p)
	}
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
}

// The needs-upgrade line names the path, the markers, the command and the
// skill, on one line; registeredLegacy lists only the pre-1.0 projects, once
// per root.
func TestNeedsUpgradeLineAndRegisteredLegacy(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	old, m := newPreV1Repo(t)
	line := needsUpgradeLine(old, m)
	for _, want := range []string{old + ": needs upgrade", "deployed.version", "`satelle upgrade --archive`", "satelle-upgrade skill"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "\n") {
		t.Errorf("the line is not one line: %q", line)
	}
	registerProjects(t, dir, old, old+string(filepath.Separator), filepath.Join(t.TempDir(), "gone"))
	ps := registeredLegacy()
	if len(ps) != 1 || ps[0].Path != old || ps[0].Root != old {
		t.Fatalf("registeredLegacy = %+v, want only %s", ps, old)
	}
}
