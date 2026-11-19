package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceTableIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	path := filepath.Join(home, GlobalConfigName)
	for _, body := range []string{
		"[workspace]\nrepos = [\"/a\", \"/b\"]\n",
		"[projects]\npaths = [\"/new\"]\n[workspace]\nrepos = [\"/old\"]\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		gc, err := LoadGlobal()
		if err == nil || !strings.Contains(err.Error(), "[workspace]") || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "[projects] paths") {
			t.Fatalf("%q: want a refusal naming [workspace], %s and [projects] paths; got %v", body, path, err)
		}
		if len(gc.Projects.Paths) != 0 {
			t.Fatalf("%q: refused config still yields projects %v", body, gc.Projects.Paths)
		}
	}
}

func TestProjectsTableLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, GlobalConfigName), []byte("[projects]\npaths = [\"/a\", \"/b\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gc, err := LoadGlobal()
	if err != nil || strings.Join(gc.Projects.Paths, ",") != "/a,/b" {
		t.Fatalf("loaded projects %v (%v)", gc.Projects.Paths, err)
	}
}

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
