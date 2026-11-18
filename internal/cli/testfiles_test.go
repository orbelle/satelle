package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExec(t *testing.T, p, body string) {
	t.Helper()
	writeFile(t, p, body)
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
