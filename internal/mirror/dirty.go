package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dirty marks are how a CLI push that did not land reaches the serve-side
// repair. The CLI writes one file per repo (no network, no wait) when its drain
// or seat push fails; satelled's reconciler reseeds every marked repo on its
// short retry tick and clears the mark, so the change reaches the page within
// seconds rather than at the next full pass.

// DirtyDir is the directory holding dirty marks under the machine home.
func DirtyDir(globalDir string) string {
	return filepath.Join(globalDir, DefaultDirName, "dirty")
}

func dirtyFile(globalDir, repoPath string) string {
	sum := sha256.Sum256([]byte(repoPath))
	return filepath.Join(DirtyDir(globalDir), hex.EncodeToString(sum[:8]))
}

// MarkDirty records that repoPath's latest state did not reach the mirror.
// Idempotent; the file holds the path.
func MarkDirty(globalDir, repoPath string) error {
	repoPath = strings.TrimSpace(repoPath)
	if globalDir == "" || repoPath == "" {
		return nil
	}
	if err := os.MkdirAll(DirtyDir(globalDir), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dirtyFile(globalDir, repoPath), []byte(repoPath), 0o644)
}

// DirtyRepos lists the repo paths currently marked dirty, sorted.
func DirtyRepos(globalDir string) []string {
	entries, err := os.ReadDir(DirtyDir(globalDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(DirtyDir(globalDir), e.Name()))
		if err != nil {
			continue
		}
		if p := strings.TrimSpace(string(b)); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ClearDirty removes repoPath's mark. Clearing BEFORE the reseed runs means a
// mark written during the reseed survives it, costing at most one extra reseed.
func ClearDirty(globalDir, repoPath string) error {
	err := os.Remove(dirtyFile(globalDir, strings.TrimSpace(repoPath)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
