package cli

import (
	"os"
	"testing"
)

// TestMain keeps the suite hermetic: a session token in the caller's
// environment would make the CLI act as a signed-in hosted session, and an
// unisolated XDG_CONFIG_HOME would point credential lookups at the operator's.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("SATELLE_TOKEN")
	xdg, err := os.MkdirTemp("", "satelle-cli-xdg-")
	if err == nil {
		_ = os.Setenv("XDG_CONFIG_HOME", xdg)
	}
	code := m.Run()
	if xdg != "" {
		_ = os.RemoveAll(xdg)
	}
	os.Exit(code)
}
