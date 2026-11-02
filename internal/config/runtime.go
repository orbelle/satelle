// runtime.go — home-keyed runtime plane (epic:substrate-planes / sty_4660bbe1).
//
// Authored substrate stays under <repo>/.satelle (ResolveDataDir). Runtime state
// — the per-repo DB, logs, backups, and the stories attachment cache — lives
// under ~/.satelle/<repo-key>/ (ResolveRuntimeDir). One repo-key maps to one
// isolated dir (never a flat multi-repo bag); see decision-local-db-placement
// and decision-substrate-planes-local-first.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// RepoKey returns a stable directory key for repoRoot under GlobalDir().
// Multiple worktrees / cwd forms of the same repository hash identically.
//
// Derivation:
//  1. Prefer git identity via `git rev-parse --git-common-dir` (worktree-collapsing:
//     a linked worktree returns the main repo's .git).
//  2. Fall back to symlink-resolved absolute path for non-git trees.
//
// The key is `<sanitised-basename>-<sha256[:8]>` so `ls ~/.satelle` stays legible
// while collisions stay improbable. Never reads os.Getwd(); the root is an argument.
//
// Note: a tree that gains a .git later (init → git init) can flip identity. Callers
// that need continuity across that flip should use ResolveRuntimeDir, which prefers
// an already-existing runtime dir under either key.
func RepoKey(repoRoot string) string {
	identity, base := repoIdentity(repoRoot)
	return keyFromIdentity(identity, base)
}

// pathRepoKey is the path-only form of RepoKey (ignores git). Used to find a
// runtime dir that was created before the tree became a git repo.
func pathRepoKey(repoRoot string) string {
	identity, base := pathIdentity(repoRoot)
	return keyFromIdentity(identity, base)
}

func keyFromIdentity(identity, base string) string {
	sum := sha256.Sum256([]byte(identity))
	short := hex.EncodeToString(sum[:])[:8]
	return sanitiseBasename(base) + "-" + short
}

// repoIdentity returns a canonical identity string and a human basename for keying.
func repoIdentity(repoRoot string) (identity, basename string) {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	// Git common-dir: collapses worktrees onto the main repo's .git.
	if id, base, ok := gitCommonIdentity(root); ok {
		return id, base
	}
	return pathIdentity(root)
}

// pathIdentity is the non-git identity: symlink-resolved absolute path.
func pathIdentity(repoRoot string) (identity, basename string) {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	abs = filepath.Clean(abs)
	return abs, filepath.Base(abs)
}

// gitIdentityMemo is the scoped memo of gitCommonIdentity. It is off unless a
// caller holds MemoGitIdentity: a long-lived process must see a tree gain a .git.
var gitIdentityMemo struct {
	mu      sync.Mutex
	holders int
	byRoot  map[string]gitIdentityResult
}

type gitIdentityResult struct {
	identity, basename string
	ok                 bool
}

// MemoGitIdentity makes gitCommonIdentity run `git rev-parse` once per repo root
// until the returned release is called. A short-lived hook call holds it so that
// resolving the config, the runtime dir and the store key share one spawn
// instead of starting git for each (sty_a74551d6 AC5). Holds nest; the memo is
// dropped when the last one is released.
func MemoGitIdentity() (release func()) {
	gitIdentityMemo.mu.Lock()
	gitIdentityMemo.holders++
	if gitIdentityMemo.byRoot == nil {
		gitIdentityMemo.byRoot = map[string]gitIdentityResult{}
	}
	gitIdentityMemo.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			gitIdentityMemo.mu.Lock()
			defer gitIdentityMemo.mu.Unlock()
			if gitIdentityMemo.holders--; gitIdentityMemo.holders == 0 {
				gitIdentityMemo.byRoot = nil
			}
		})
	}
}

// gitCommonIdentity resolves identity via git's common dir. Returns ok=false when
// git is unavailable or the path is not a git work tree.
func gitCommonIdentity(repoRoot string) (identity, basename string, ok bool) {
	gitIdentityMemo.mu.Lock()
	memo := gitIdentityMemo.byRoot != nil
	r, hit := gitIdentityMemo.byRoot[repoRoot]
	gitIdentityMemo.mu.Unlock()
	if hit {
		return r.identity, r.basename, r.ok
	}
	identity, basename, ok = gitCommonIdentityUncached(repoRoot)
	if memo {
		gitIdentityMemo.mu.Lock()
		if gitIdentityMemo.byRoot != nil {
			gitIdentityMemo.byRoot[repoRoot] = gitIdentityResult{identity, basename, ok}
		}
		gitIdentityMemo.mu.Unlock()
	}
	return identity, basename, ok
}

func gitCommonIdentityUncached(repoRoot string) (identity, basename string, ok bool) {
	// `--path-format=absolute` makes git print the common dir absolute from any
	// worktree; git < 2.31 rejects the flag, so retry the bare form, which may
	// print a path relative to repoRoot.
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		out, err = exec.Command("git", "-C", repoRoot, "rev-parse", "--git-common-dir").Output()
		if err != nil {
			return "", "", false
		}
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", "", false
	}
	// A relative result (the pre-2.31 form) is resolved against repoRoot first.
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoRoot, common)
	}
	abs, err := filepath.Abs(common)
	if err != nil {
		return "", "", false
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	abs = filepath.Clean(abs)
	// Basename of the parent of .git (the main working tree root).
	base := filepath.Base(filepath.Dir(abs))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = filepath.Base(abs)
	}
	return abs, base, true
}

// sanitiseBasename keeps only filesystem-safe runes so the key is a single path segment.
func sanitiseBasename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "repo"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "repo"
	}
	// Cap length so a long monorepo name does not make paths absurd.
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// RuntimeResolution is the result of ResolveRuntimeDir: the absolute runtime
// directory and whether it is the pre-migration legacy layout under data_dir.
type RuntimeResolution struct {
	Dir    string // absolute path holding satelle.db, logs/, backups/, stories/
	Legacy bool   // true when Dir is still <data_dir> because migration has not run
}

// ResolveRuntimeDir returns where runtime state lives for repoRoot.
//
// Policy (sty_4660bbe1):
//   - An explicit `db` override wins: runtime dir is the parent of that path.
//   - Prefer an existing home-keyed dir: first the current RepoKey (git-aware),
//     then the path-only key (so a tree that was inited non-git and later gained
//     a .git keeps its original ledger).
//   - Else, if a legacy <data_dir>/satelle.db exists, return the legacy dir with
//     Legacy=true so open keeps working and callers can emit a deprecation note
//     (no silent move — migrate is an explicit command).
//   - Else (fresh repo) return the home-keyed path under the current RepoKey.
//
// data_dir overrides do NOT relocate runtime; they only relocate authored substrate.
func (c Config) ResolveRuntimeDir(repoRoot string) RuntimeResolution {
	if p := strings.TrimSpace(c.DB); p != "" {
		db := resolveUnder(repoRoot, p)
		return RuntimeResolution{Dir: filepath.Dir(db), Legacy: false}
	}

	primary := filepath.Join(GlobalDir(), RepoKey(repoRoot))
	if fileExists(filepath.Join(primary, DefaultDBName)) {
		return RuntimeResolution{Dir: primary, Legacy: false}
	}
	// Continuity across "init then git init": path-key DB created before .git.
	pathKey := filepath.Join(GlobalDir(), pathRepoKey(repoRoot))
	if pathKey != primary && fileExists(filepath.Join(pathKey, DefaultDBName)) {
		return RuntimeResolution{Dir: pathKey, Legacy: false}
	}

	legacyDir := c.ResolveDataDir(repoRoot)
	legacyDB := filepath.Join(legacyDir, DefaultDBName)
	if fileExists(legacyDB) {
		return RuntimeResolution{Dir: legacyDir, Legacy: true}
	}
	return RuntimeResolution{Dir: primary, Legacy: false}
}

// ResolveRuntimeDB returns the absolute path of the per-repo sqlite database under
// the resolved runtime dir. An explicit `db` config still wins via ResolveRuntimeDir.
func (c Config) ResolveRuntimeDB(repoRoot string) string {
	return filepath.Join(c.ResolveRuntimeDir(repoRoot).Dir, DefaultDBName)
}

// ResolveLogsDir is the sole storage path for this repo's runtime logs
// (~/.satelle/<repo-key>/logs). The in-repo .satelle/logs pointer is not
// storage; it is a symlink to this directory.
func (c Config) ResolveLogsDir(repoRoot string) string {
	return filepath.Join(c.ResolveRuntimeDir(repoRoot).Dir, "logs")
}

// LegacyRuntimeNote is the one-line deprecation message when a repo still has
// its database under the authored data dir. Empty when not legacy.
func (c Config) LegacyRuntimeNote(repoRoot string) string {
	res := c.ResolveRuntimeDir(repoRoot)
	if !res.Legacy {
		return ""
	}
	target := filepath.Join(GlobalDir(), RepoKey(repoRoot))
	return fmt.Sprintf(
		"satelle: runtime state still under %s — run `satelle runtime migrate` to move it to %s (see decision-substrate-planes-local-first)",
		res.Dir, target)
}

// fileExists reports a regular file (or any non-dir node) at path.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// RuntimeKeyDirPattern matches a home-keyed runtime directory name
// (`<sanitised-basename>-<sha256[:8]>`). Shared by CLI list and the
// integration host-surface guard (sty_c36c211f).
var RuntimeKeyDirPattern = regexp.MustCompile(`^[^/]+-[0-9a-f]{8}$`)

// IsRuntimeKeyDir reports whether name is a home-keyed runtime key segment.
func IsRuntimeKeyDir(name string) bool {
	return RuntimeKeyDirPattern.MatchString(name)
}

// RepoPathMarkerName is the file written into a runtime dir recording the
// absolute repo root that owns it (sty_c36c211f AC2 — forward resolution).
const RepoPathMarkerName = "repo.path"

// WriteRepoPathMarker records repoRoot into runtimeDir/repo.path so
// `satelle runtime list` can reverse-map keys. Best-effort: no-op when
// runtimeDir is empty or equals the repo's data dir (legacy layout).
func WriteRepoPathMarker(runtimeDir, repoRoot string) error {
	runtimeDir = strings.TrimSpace(runtimeDir)
	repoRoot = strings.TrimSpace(repoRoot)
	if runtimeDir == "" || repoRoot == "" {
		return nil
	}
	// Only mark home-keyed dirs (parent is GlobalDir, basename is a key).
	base := filepath.Base(runtimeDir)
	if !IsRuntimeKeyDir(base) {
		return nil
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	abs, err := filepath.Abs(CanonicalRepoRoot(repoRoot))
	if err != nil {
		abs = repoRoot
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	// The marker is an IDENTITY sink, not a location: the runtime dir is keyed by
	// RepoKey, which COLLAPSES worktrees, so writing the invoking root made the
	// marker flip to whichever tree opened last. After `git worktree remove` the
	// main repo's whole plane then read as stale and `runtime reap` could offer to
	// delete it — a data-loss path (sty_cd219594).
	return os.WriteFile(filepath.Join(runtimeDir, RepoPathMarkerName), []byte(abs+"\n"), 0o644)
}

// ReadRepoPathMarker returns the recorded repo root for a runtime dir, or "" if absent.
func ReadRepoPathMarker(runtimeDir string) string {
	b, err := os.ReadFile(filepath.Join(runtimeDir, RepoPathMarkerName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// CanonicalRepoRoot returns the MAIN working tree root for repoRoot, so a linked
// git worktree identifies as the repository it belongs to rather than as a
// separate project of its own.
//
// `git rev-parse --git-common-dir` already resolves from any worktree to the main
// repository's .git — the same fact RepoKey collapses worktrees with. FindDataDir
// still keys on a literal .satelle in the tree it is given, so the DATA dir is
// deliberately left alone; what is canonicalised here is IDENTITY: the path a
// repo registers under and the slug its mirror partition carries. The process
// read-plane is not this function: once config is loaded, it follows the data
// dir the main tree's loaded config names.
//
// Without this, registering from a worktree added a second project row and
// relabelled the parent repo's partition after its own directory name, so
// /r/<parent> returned 404 until a `satelle workspace add` from the main tree put
// it back — on every worktree dispatch (sty_cd219594, sty_dfc9b100).
//
// Falls back to repoRoot unchanged when the tree is not a git worktree of a
// satelle-governed main tree, so the ordinary case is untouched.
//
// OWNERSHIP RULE — identity versus location. Every IDENTITY sink goes through
// this: the workspace-registry path, the mirror partition's slug, its path and
// ProjectName, and the runtime dir's repo.path marker. Identity NAMES the
// repository, and a worktree and its parent are the same repository, so identity
// must not move per worktree. The .satelle/deployed.version stamp is read the
// same way (cli.stampDataDir): it records which binary the REPOSITORY's
// substrate was aligned to, and .satelle is gitignored, so a worktree never
// carries it (sty_5bf84ba4). Every LOCATION use of this function keeps the
// invoking root: FindDataDir, the edit tree, and runtime-dir resolution (which
// collapses worktrees by RepoKey on its own). The process read-plane is a
// separate resolution after config is loaded, and it follows the data dir the
// main tree's loaded config names. Canonicalising only SOME identity
// sinks is worse than canonicalising none — that is how the partition's slug
// read `satelle` while its path still read the worktree, so the audit is by SINK
// (grep IdentityMeta, ProjectName, RepoPathMarker, and the registry writers)
// rather than by the shape of the call.
func CanonicalRepoRoot(repoRoot string) string {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	common, _, ok := gitCommonIdentity(root)
	if !ok {
		return root
	}
	// The identity is the .git DIRECTORY; the main working tree is its parent.
	if filepath.Base(common) != ".git" {
		return root
	}
	main := filepath.Dir(common)
	if _, err := os.Stat(filepath.Join(main, DefaultDataDir)); err != nil {
		return root
	}
	return main
}

// RepoName is the name a repo goes by in the workspace registry and the web
// partition: the base of its canonical root, so a worktree and its main tree are
// one name. It is what `satelle workspace list` shows and what a cross-repo
// `depends-on:<repo>/<id>` names.
//
// It is NOT the hosted project slug. Slugify and ResolveBoundProject own that
// different fact — the project a repo syncs to, which a `[sync] project` override
// can change and which is slugified — so a repo can sync as one name and be
// registered as another. Do not use either for registry identity.
func RepoName(repoRoot string) string {
	return filepath.Base(CanonicalRepoRoot(repoRoot))
}
