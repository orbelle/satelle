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
