package cli

// sty_d6e209aa AC4/AC5/AC6: a linked worktree whose own copy of the authored
// process differs from the main tree's is told so — file by file — and the main
// tree's copy governs. A main tree, and a worktree whose copy matches, are told
// nothing.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// divergenceDataDir is deliberately not .satelle: AC6 requires every path to come
// from the resolved process config, never a hard-coded layout.
const divergenceDataDir = "cfg/satelle"

const divergenceMainToml = "data_dir = \"" + divergenceDataDir + "\"\n\n[review]\ngate_create = false\n"

// writeDivergenceProcess authors the feature lane, its check skill and a
// constitution under main's relocated data dir.
func writeDivergenceProcess(t *testing.T, main string) {
	t.Helper()
	writeFile(t, main, ".satelle/satelle.toml", divergenceMainToml)
	writeFile(t, main, divergenceDataDir+"/workflows/done.toml", featureLaneDone)
	writeFile(t, main, divergenceDataDir+"/workflows/step.toml", featureLaneStep)
	writeFile(t, main, divergenceDataDir+"/skills/wt-place-check.md", placeCheckSkill)
	writeFile(t, main, divergenceDataDir+"/constitution.md", "MAIN-CONSTITUTION\n")
}

// createStoryJSON creates a feature story and reads its id from stdout alone. A
// relocated data dir makes `story create` advise on stderr about the create gate
// (it looks for satelle.toml inside the data dir), which must not reach the JSON.
func createStoryJSON(t *testing.T, title string) string {
	t.Helper()
	stdout, stderr, err := runRootSplit(t, "", "story", "create", "--title", title, "--body", "b",
		"--acceptance", "1. a", "--category", "feature")
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, stderr)
	}
	return idOf(t, stdout)
}

// copyProcessInto hand-copies main's relocated data dir into the worktree, the
// way the worktrees under satelle-wt/ carry a copied .satelle folder.
func copyProcessInto(t *testing.T, main, wt string) {
	t.Helper()
	src := filepath.Join(main, filepath.FromSlash(divergenceDataDir))
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		rel, rerr := filepath.Rel(main, p)
		if rerr != nil {
			return rerr
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		writeFile(t, wt, filepath.ToSlash(rel), string(body))
		return nil
	})
	if err != nil {
		t.Fatalf("copy process into worktree: %v", err)
	}
}

// linkedDivergenceFixture is a main tree with an authored process under a
// relocated data dir, and a linked worktree placed outside it. The process is
// left in the worktree.
func linkedDivergenceFixture(t *testing.T) (main, wt string) {
	t.Helper()
	isolateProcessEnv(t)
	base := t.TempDir()
	main = gitMainTree(t, base)
	wt = filepath.Join(base, "wt", "sty_diverge")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "worktree", "add", "-q", "-b", "sty_diverge", wt, "HEAD")
	writeDivergenceProcess(t, main)
	return main, wt
}

// wantDivergenceReport asserts the AC4 report: the main tree governs, the
// worktree's copy is ignored, and every changed and extra file is listed by its
// path relative to the tree root.
func wantDivergenceReport(t *testing.T, surface, text string) {
	t.Helper()
	for _, want := range []string{
		"main tree's copy governs", "worktree's copy is ignored",
		"changed: " + divergenceDataDir + "/skills/wt-place-check.md",
		"extra: " + divergenceDataDir + "/skills/extra.md",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: divergence report must carry %q:\n%s", surface, want, text)
		}
	}
}

func wantNoDivergenceReport(t *testing.T, surface, text string) {
	t.Helper()
	for _, bad := range []string{"worktree's copy is ignored", "carries its own copy of the authored process"} {
		if strings.Contains(text, bad) {
			t.Errorf("%s: no divergence exists, yet the output reports %q:\n%s", surface, bad, text)
		}
	}
}
