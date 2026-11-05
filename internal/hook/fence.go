package hook

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/bobmcallan/satelle/internal/gitfacts"
)

// gitRootOf walks up from abs for a .git entry (a directory, or the file of a
// linked worktree or submodule) and returns that tree's root, "" when abs is
// in no working tree. A path not yet created resolves from its parent.
func gitRootOf(abs string) string {
	abs = filepath.Clean(abs)
	if abs == "" || abs == "." {
		return ""
	}
	dir := abs
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		dir = filepath.Dir(abs)
	}
	for {
		if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// TreeOf is the working tree dir is in, "" when none.
func TreeOf(dir string) string { return gitRootOf(dir) }

var (
	commonMu    sync.Mutex
	commonCache = map[string]string{}
)

// commonDirOf is the git common dir of the tree at root, memoised for the
// process; only successes are cached.
func commonDirOf(root string) (string, bool) {
	commonMu.Lock()
	dir, ok := commonCache[root]
	commonMu.Unlock()
	if ok {
		return dir, true
	}
	dir, err := gitfacts.Repo{Dir: root}.CommonDir()
	if err != nil || dir == "" {
		return "", false
	}
	commonMu.Lock()
	commonCache[root] = dir
	commonMu.Unlock()
	return dir, true
}

// sameRepo reports whether the trees at a and b share one git common dir (a
// main tree and its linked worktrees). Any lookup failure is false, so the
// fence fails closed.
func sameRepo(a, b string) bool {
	ca, ok := commonDirOf(a)
	if !ok {
		return false
	}
	cb, ok := commonDirOf(b)
	return ok && ca == cb
}
