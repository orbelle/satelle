package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// linkedWorktreeOf makes a satelle main tree and a linked worktree with no
// satelle.toml of its own, chdirs into the worktree and returns both roots.
func linkedWorktreeOf(t *testing.T) (main, wt string) {
	t.Helper()
	t.Setenv("SATELLE_CONFIG", "")
	base := t.TempDir()
	main = mkSatelleTree(t, filepath.Join(base, "main"))
	wt = filepath.Join(base, "wt", "sty_00000001")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	addWorktree(t, main, wt, "sty_00000001")
	t.Chdir(wt)
	return main, wt
}

func TestLoadProcessReturnsPathOnError(t *testing.T) {
	main, _ := linkedWorktreeOf(t)
	cfgPath := writeSatelleToml(t, main, ConfigName, "[gate\n")

	_, _, _, path, err := LoadInvokingProcess()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("LoadInvokingProcess err = %v, want *ParseError", err)
	}
	if !sameResolvedPath(path, cfgPath) {
		t.Fatalf("path = %q, want %q", path, cfgPath)
	}
	_, _, path, err = LoadProcess(Config{}, "")
	if !errors.As(err, &pe) || !sameResolvedPath(path, cfgPath) {
		t.Fatalf("LoadProcess path = %q err = %v", path, err)
	}

	// An overlay syntax error names the overlay.
	writeSatelleToml(t, main, ConfigName, "[gate]\n")
	local := writeSatelleToml(t, main, LocalConfigName, "[gate\n")
	_, _, _, path, err = LoadInvokingProcess()
	if !errors.As(err, &pe) || !sameResolvedPath(path, local) {
		t.Fatalf("overlay: path = %q err = %v, want %q", path, err, local)
	}
}

func TestLoadInvokingProcessLocationParseErrorReturnsLocationPath(t *testing.T) {
	t.Setenv("SATELLE_CONFIG", "")
	root := t.TempDir()
	p := writeSatelleToml(t, root, ConfigName, "[gate\n")
	t.Chdir(root)
	_, _, _, path, err := LoadInvokingProcess()
	var pe *ParseError
	if !errors.As(err, &pe) || !sameResolvedPath(path, p) {
		t.Fatalf("path = %q err = %v", path, err)
	}
}
