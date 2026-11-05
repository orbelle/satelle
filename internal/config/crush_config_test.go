package config

import (
	"os"
	"path/filepath"
	"testing"
)

// loadToml writes body as a repo's .satelle/satelle.toml and loads it.
func loadToml(t *testing.T, body string) (Config, error) {
	t.Helper()
	satelleDir := filepath.Join(t.TempDir(), ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(satelleDir, ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	return cfg, err
}
