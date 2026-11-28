package hook

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/bobmcallan/satelle/internal/gitfacts"
)

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

func TreeOf(dir string) string { return gitRootOf(dir) }

var (
	commonMu    sync.Mutex
	commonCache = map[string]string{}
)

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

func sameRepo(a, b string) bool {
	ca, ok := commonDirOf(a)
	if !ok {
		return false
	}
	cb, ok := commonDirOf(b)
	return ok && ca == cb
}
