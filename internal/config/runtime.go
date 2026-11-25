package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
)

func RepoKey(repoRoot string) string {
	identity, base := repoIdentity(repoRoot)
	return keyFromIdentity(identity, base)
}

func pathRepoKey(repoRoot string) string {
	identity, base := pathIdentity(repoRoot)
	return keyFromIdentity(identity, base)
}

func keyFromIdentity(identity, base string) string {
	sum := sha256.Sum256([]byte(identity))
	short := hex.EncodeToString(sum[:])[:8]
	return sanitiseBasename(base) + "-" + short
}

func repoIdentity(repoRoot string) (identity, basename string) {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	if id, base, ok := gitCommonIdentity(root); ok {
		return id, base
	}
	return pathIdentity(root)
}

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

var gitIdentityMemo struct {
	mu      sync.Mutex
	holders int
	byRoot  map[string]gitIdentityResult
}

type gitIdentityResult struct {
	identity, basename string
	ok                 bool
}

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

func gitCommonIdentity(repoRoot string) (identity, basename string, ok bool) {
	gitIdentityMemo.mu.Lock()
	memo := gitIdentityMemo.byRoot != nil
	r, hit := gitIdentityMemo.byRoot[repoRoot]
	gitIdentityMemo.mu.Unlock()
	if hit {
		return r.identity, r.basename, r.ok
	}
	identity, basename, ok = gitCommonIdentityEnv(repoRoot, nil)
	if memo {
		gitIdentityMemo.mu.Lock()
		if gitIdentityMemo.byRoot != nil {
			gitIdentityMemo.byRoot[repoRoot] = gitIdentityResult{identity, basename, ok}
		}
		gitIdentityMemo.mu.Unlock()
	}
	return identity, basename, ok
}

func gitCommonIdentityEnv(repoRoot string, env []string) (identity, basename string, ok bool) {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		if env != nil {
			cmd.Env = env
		}
		return cmd.Output()
	}
	out, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		out, err = git("rev-parse", "--git-common-dir")
		if err != nil {
			return "", "", false
		}
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", "", false
	}
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
	base := filepath.Base(filepath.Dir(abs))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = filepath.Base(abs)
	}
	return abs, base, true
}

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
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func RuntimeDir(repoRoot string) string {
	return filepath.Join(GlobalDir(), RepoKey(repoRoot))
}

func RuntimeDirEnv(repoRoot string, env []string) string {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	identity, base, ok := gitCommonIdentityEnv(root, env)
	if !ok {
		identity, base = pathIdentity(root)
	}
	return filepath.Join(GlobalDir(), keyFromIdentity(identity, base))
}

func GitLookupEnv(environ, withhold []string) []string {
	out := []string{}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || slices.Contains(withhold, name) || credentialName(name) {
			continue
		}
		if name == "PATH" || name == "HOME" || name == "XDG_CONFIG_HOME" || strings.HasPrefix(name, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

func credentialName(name string) bool {
	u := strings.ToUpper(name)
	return strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD")
}

func LogsDir(runtime string) string { return filepath.Join(runtime, "logs") }

var RuntimeKeyDirPattern = regexp.MustCompile(`^[^/]+-[0-9a-f]{8}$`)

func IsRuntimeKeyDir(name string) bool {
	return RuntimeKeyDirPattern.MatchString(name)
}

const RepoPathMarkerName = "repo.path"

func WriteRepoPathMarker(runtimeDir, repoRoot string) error {
	return WriteRepoPathMarkerEnv(runtimeDir, repoRoot, nil)
}

// WriteRepoPathMarkerEnv is WriteRepoPathMarker whose git lookup runs with
// env (nil keeps the process environment).
func WriteRepoPathMarkerEnv(runtimeDir, repoRoot string, env []string) error {
	runtimeDir = strings.TrimSpace(runtimeDir)
	repoRoot = strings.TrimSpace(repoRoot)
	if runtimeDir == "" || repoRoot == "" {
		return nil
	}
	base := filepath.Base(runtimeDir)
	if !IsRuntimeKeyDir(base) {
		return nil
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	abs, err := filepath.Abs(canonicalRepoRootEnv(repoRoot, env))
	if err != nil {
		abs = repoRoot
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	return os.WriteFile(filepath.Join(runtimeDir, RepoPathMarkerName), []byte(abs+"\n"), 0o644)
}

func ReadRepoPathMarker(runtimeDir string) string {
	b, err := os.ReadFile(filepath.Join(runtimeDir, RepoPathMarkerName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func CanonicalRepoRoot(repoRoot string) string {
	return canonicalRepoRootEnv(repoRoot, nil)
}

func canonicalRepoRootEnv(repoRoot string, env []string) string {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	var common string
	var ok bool
	if env == nil {
		common, _, ok = gitCommonIdentity(root)
	} else {
		common, _, ok = gitCommonIdentityEnv(root, env)
	}
	if !ok {
		return root
	}
	if filepath.Base(common) != ".git" {
		return root
	}
	main := filepath.Dir(common)
	if _, err := os.Stat(filepath.Join(main, DefaultDataDir)); err != nil {
		return root
	}
	return main
}

func RepoName(repoRoot string) string {
	return filepath.Base(CanonicalRepoRoot(repoRoot))
}
