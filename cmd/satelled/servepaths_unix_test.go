//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestServeGateFilesListsEmbeddedFiles(t *testing.T) {
	requireGitAndGo(t)
	root := repoRoot(t)
	code, out := gateIn(t, root, nil, "--files", "./cmd/satelle", "./cmd/satelled")
	if code != 0 {
		t.Fatalf("--files: exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	has := func(l string) bool {
		for _, x := range lines {
			if x == l {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"built\tinternal/help/topics/queues.md",
		"pattern\tinternal/help/topics",
		"pattern\tinternal/kernel/template",
	} {
		if !has(want) {
			t.Errorf("--files lacks %q", want)
		}
	}
	serve := false
	for _, l := range lines {
		kind, path, ok := strings.Cut(l, "\t")
		if !ok || (kind != "built" && kind != "excluded" && kind != "pattern") {
			t.Errorf("--files line %q is not <built|excluded|pattern><TAB><path>", l)
		}
		if strings.HasSuffix(path, "_test.go") && !strings.Contains(path, "/topics/") {
			t.Errorf("--files lists a test file: %q", l)
		}
		serve = serve || (kind == "built" && strings.HasPrefix(path, "internal/serve/"))
	}
	if !serve {
		t.Error("--files lists no internal/serve source")
	}
	if code, out := gateIn(t, root, nil, "--files", "./cmd/nosuchpkg"); code != 3 || !strings.Contains(out, "go list") {
		t.Errorf("--files on a missing package: exit %d, want 3 with go's reason\n%s", code, out)
	}
}
