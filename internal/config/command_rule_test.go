package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSatelleToml(t *testing.T, dir, name, body string) string {
	t.Helper()
	sat := filepath.Join(dir, DefaultDataDir)
	if err := os.MkdirAll(sat, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sat, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadParseErrorIsTyped(t *testing.T) {
	root := t.TempDir()
	p := writeSatelleToml(t, root, ConfigName, "[gate\n")
	_, path, err := Load(p)
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Path != p || path != p {
		t.Fatalf("err = %v path = %q", err, path)
	}
	if !strings.HasPrefix(err.Error(), "config: parse "+p+": ") {
		t.Fatalf("message changed: %q", err.Error())
	}
}
