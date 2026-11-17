package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

const waitBudget = 10 * time.Second

func isolateHome(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SATELLE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if strings.TrimSpace(os.Getenv("SATELLE_SERVER_ENDPOINT")) == "" {
		t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	}
	return home
}

func addedProject(t testing.TB, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return addPath(t, dir)
}

func addPath(t testing.TB, dir string) string {
	t.Helper()
	if strings.TrimSpace(os.Getenv("SATELLE_HOME")) == "" {
		isolateHome(t)
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	gc.Projects.Add(dir)
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	return config.RepoKey(dir)
}

func eventually(t testing.TB, budget time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !cond() {
		if !time.Now().Before(deadline) {
			t.Fatalf("%s (not met within %s)", msg, budget)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
