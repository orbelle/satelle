package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitBudget bounds how long a test waits for an asynchronous condition.
const waitBudget = 10 * time.Second

// isolateHome points SATELLE_HOME and XDG_CONFIG_HOME at a temp dir and turns
// satelled discovery off, so a test never touches real state.
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

// eventually polls cond until it holds or budget passes.
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
