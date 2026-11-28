package integration

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProjectFilesCoverHookFiles(t *testing.T) {
	files := ProjectFiles()
	for _, want := range []string{".claude/settings.json", ".cursor/hooks.json", ".grok", ".agents/hooks.json", ".mcp.json"} {
		if !slices.Contains(files, want) {
			t.Errorf("ProjectFiles() = %q, lacks %s", files, want)
		}
	}
	if !slices.IsSorted(files) {
		t.Errorf("ProjectFiles() = %q, not sorted", files)
	}
	for _, h := range Hooks() {
		f := filepath.ToSlash(h.File())
		if !slices.ContainsFunc(files, func(p string) bool { return f == p || strings.HasPrefix(f, p+"/") }) {
			t.Errorf("hook file %s of %s lies under no ProjectFiles entry %q", f, h.Name(), files)
		}
	}
}
