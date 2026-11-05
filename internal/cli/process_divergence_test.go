package cli

// sty_d6e209aa AC4/AC5/AC6: a linked worktree whose own copy of the authored
// process differs from the main tree's is told so — file by file — and the main
// tree's copy governs. A main tree, and a worktree whose copy matches, are told
// nothing.

import (
	"strings"
	"testing"
)

// divergenceDataDir is deliberately not .satelle: AC6 requires every path to come
// from the resolved process config, never a hard-coded layout.
const divergenceDataDir = "cfg/satelle"

const divergenceMainToml = "data_dir = \"" + divergenceDataDir + "\"\n\n[review]\ngate_create = false\n"

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
