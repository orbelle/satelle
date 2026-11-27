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

func TestSaveGlobalHerdrOnlyWhenSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	if err := SaveGlobal(GlobalConfig{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, GlobalConfigName))
	if strings.Contains(string(b), "herdr") {
		t.Fatalf("an unset herdr home renders:\n%s", b)
	}
	if err := SaveGlobal(GlobalConfig{Herdr: HerdrConfig{Home: "/srv/herdr"}}); err != nil {
		t.Fatal(err)
	}
	gc, err := LoadGlobal()
	if err != nil || gc.Herdr.Home != "/srv/herdr" {
		t.Fatalf("herdr home round trip = %q (%v)", gc.Herdr.Home, err)
	}
}

func TestWakeQueuesMatchesRepoRoot(t *testing.T) {
	repo := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if got := (GlobalConfig{}).WakeQueues(repo); len(got) != 0 {
		t.Fatalf("no [[wake]] wakes %v", got)
	}
	gc := GlobalConfig{Wake: []WakeConfig{
		{Project: repo, Queue: "release"},
		{Project: t.TempDir(), Queue: "other"},
		{Project: link, Queue: "release"},
		{Project: link, Queue: "drift"},
	}}
	if got := gc.WakeQueues(link); strings.Join(got, ",") != "release,drift" {
		t.Fatalf("a symlinked root wakes %v, want release,drift", got)
	}
	if got := gc.WakeQueues(t.TempDir()); len(got) != 0 {
		t.Fatalf("another root wakes %v", got)
	}
}

func TestSaveGlobalKeepsWake(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	if err := SaveGlobal(GlobalConfig{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, GlobalConfigName))
	if strings.Contains(string(b), "\n[[wake]]") || !strings.Contains(string(b), "# [[wake]]") {
		t.Fatalf("an empty wake list renders more than the commented example:\n%s", b)
	}
	repo := t.TempDir()
	want := []WakeConfig{{Project: repo, Queue: "release"}}
	if err := SaveGlobal(GlobalConfig{Wake: want, Herdr: HerdrConfig{Home: "/h"}}); err != nil {
		t.Fatal(err)
	}
	gc, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	gc, err = LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if len(gc.Wake) != 1 || gc.Wake[0] != want[0] || gc.Herdr.Home != "/h" {
		t.Fatalf("after load and save, wake %+v herdr %q", gc.Wake, gc.Herdr.Home)
	}
}
