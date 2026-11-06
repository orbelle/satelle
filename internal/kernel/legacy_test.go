package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each pre-1.0 marker alone makes LoadConfig name satelle upgrade first; a
// fresh init names nothing.
func TestLegacyMarkersNameUpgrade(t *testing.T) {
	for _, f := range legacyFiles {
		root := filepath.Join(t.TempDir(), ".satelle")
		if err := Init(root); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("[meta]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(root)
		if err == nil || !strings.HasPrefix(err.Error(), root+" is a pre-1.0 .satelle (found: "+f+")") || !strings.Contains(err.Error(), "satelle upgrade --archive") {
			t.Errorf("%s: %v", f, err)
		}
	}
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if m := LegacyMarkers(root); len(m) != 0 {
		t.Errorf("a fresh init has markers %v", m)
	}
	if probs := Validate(root); len(probs) != 0 {
		t.Errorf("a fresh init does not validate: %v", probs)
	}
}
