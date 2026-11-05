package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// writeRemote writes body as .satelle/remotes/<name>/remote.toml under repo.
func writeRemote(t *testing.T, repo, name, body string) string {
	t.Helper()
	p := filepath.Join(repo, config.DefaultDataDir, remotesDirName, name, "remote.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
