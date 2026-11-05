package cli

import (
	"testing"
)

// sty_bcf837ff: the foreign-tree fence recognises a linked worktree of the
// session's own repository, and does not fence temp roots.

// fenceTempRootsElsewhere points the fence's temp exemption at a directory no
// test repo lives under. Test repos are t.TempDir()s, which the real temp roots
// would exempt, so a foreign-repo case would pass without exercising the fence.
func fenceTempRootsElsewhere(t *testing.T) {
	t.Helper()
	old := containmentTempRoots
	containmentTempRoots = func() []string { return []string{"/nonexistent-satelle-temp-root"} }
	t.Cleanup(func() { containmentTempRoots = old })
}
