package hook

import (
	"os"
	"path/filepath"
	"strings"
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

const maxLinkHops = 40

func CanonicalPath(p string) string { return resolveWalk(p, true) }

func EntryPath(p string) string { return resolveWalk(p, false) }

func JoinLiteral(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return base + string(filepath.Separator) + p
}

func resolveWalk(p string, followFinal bool) string {
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return filepath.Clean(p)
		}
		p = abs
	}
	clean := filepath.Clean(p)
	sep := string(filepath.Separator)
	vol := filepath.VolumeName(p)
	resolved := vol + sep
	rest := strings.Split(filepath.ToSlash(p[len(vol):]), "/")
	hops := 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, name)
		fi, err := os.Lstat(next)
		if err != nil {
			resolved = next
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 || (len(rest) == 0 && !followFinal) {
			resolved = next
			continue
		}
		if hops++; hops > maxLinkHops {
			return clean
		}
		target, err := os.Readlink(next)
		if err != nil {
			resolved = next
			continue
		}
		tvol := filepath.VolumeName(target)
		body := filepath.ToSlash(target[len(tvol):])
		if tvol != "" || strings.HasPrefix(body, "/") {
			if tvol == "" {
				tvol = filepath.VolumeName(resolved)
			}
			resolved = tvol + sep
		}
		rest = append(strings.Split(body, "/"), rest...)
	}
	return resolved
}

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
