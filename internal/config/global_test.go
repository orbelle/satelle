package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A machine config written before projects were named projects keeps its
// list: [workspace] repos is read into [projects] paths, and the next save
// writes only [projects]. A config with both keeps [projects].
func TestLegacyWorkspaceTableMigratesToProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	path := filepath.Join(home, GlobalConfigName)
	if err := os.WriteFile(path, []byte("[workspace]\nrepos = [\"/a\", \"/b\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gc, err := LoadGlobal()
	if err != nil || strings.Join(gc.Projects.Paths, ",") != "/a,/b" {
		t.Fatalf("loaded projects %v (%v)", gc.Projects.Paths, err)
	}
	if err := SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "[projects]") || !strings.Contains(string(b), `paths = ["/a", "/b"]`) || strings.Contains(string(b), "[workspace]") {
		t.Fatalf("saved config:\n%s", b)
	}
	if err := os.WriteFile(path, []byte("[projects]\npaths = [\"/new\"]\n[workspace]\nrepos = [\"/old\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gc, err := LoadGlobal(); err != nil || strings.Join(gc.Projects.Paths, ",") != "/new" {
		t.Fatalf("with both tables, projects = %v (%v)", gc.Projects.Paths, err)
	}
}

// [upgrade] start_clean is empty by default, round-trips through SaveGlobal,
// survives a hosted-server save, and matches a symlinked root.
func TestUpgradeStartClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	gc, err := LoadGlobal()
	if err != nil || len(gc.Upgrade.StartClean) != 0 {
		t.Fatalf("default start_clean %v (%v)", gc.Upgrade.StartClean, err)
	}
	if err := SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, GlobalConfigName))
	if !strings.Contains(string(b), "[upgrade]") || !strings.Contains(string(b), "start_clean = []") {
		t.Fatalf("a zero config renders:\n%s", b)
	}
	repo := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	gc.Upgrade.StartClean = []string{repo}
	if err := SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobalHostedServer("https://example.com"); err != nil {
		t.Fatal(err)
	}
	gc, _ = LoadGlobal()
	if len(gc.Upgrade.StartClean) != 1 || gc.Upgrade.StartClean[0] != repo {
		t.Fatalf("after the saves, start_clean = %v", gc.Upgrade.StartClean)
	}
	if clean, setting := gc.StartCleanFor(link); !clean || !strings.Contains(setting, "[upgrade] start_clean in ") {
		t.Errorf("a symlinked root: %v %q", clean, setting)
	}
	if clean, _ := gc.StartCleanFor(t.TempDir()); clean {
		t.Error("an unlisted root is start-clean")
	}
}
