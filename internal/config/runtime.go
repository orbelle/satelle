package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
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

func WithheldNames(environ []string) []string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == NodeWithholdEnv {
			v = val
		}
	}
	var out []string
	for _, n := range strings.Split(strings.TrimSpace(v), ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func TargetGitEnv(environ []string) []string {
	out := []string{}
	for _, kv := range GitLookupEnv(environ, WithheldNames(environ)) {
		if name, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(name, "GIT_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
}

func credentialName(name string) bool {
	u := strings.ToUpper(name)
	return strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD")
}

func secretName(name string) bool {
	u := strings.ToUpper(name)
	return credentialName(name) || strings.Contains(u, "_KEY") || strings.Contains(u, "CREDENTIAL") || strings.Contains(u, "AUTH")
}

const secretFloor = 8

var (
	targetBaseEnv  = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LANGUAGE", "TZ", "TMPDIR"}
	targetProxyEnv = []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"}
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func TargetEnvProblem(name string) error {
	switch {
	case !envNamePattern.MatchString(name):
		return fmt.Errorf("%q is not a plain environment variable name", name)
	case secretName(name):
		return fmt.Errorf("%s looks like a credential; a target dispatch never receives one by configuration", name)
	case strings.HasPrefix(name, "GIT_") || strings.HasPrefix(name, "SATELLE_"):
		return fmt.Errorf("%s is a git or satelle variable; a target dispatch never receives one", name)
	}
	return nil
}

func proxySecrets(value string) ([]string, bool) {
	if !strings.Contains(value, "@") {
		return nil, true
	}
	u, err := url.Parse(value)
	if err != nil || u.User == nil {
		return nil, false
	}
	cred, hasPass := u.User.Password()
	if !hasPass {
		cred = u.User.Username()
	}
	if len(cred) < secretFloor {
		return nil, false
	}
	out := []string{value, cred}
	if i := strings.Index(value, "://"); i >= 0 {
		authority := value[i+3:]
		if j := strings.IndexAny(authority, "/?#"); j >= 0 {
			authority = authority[:j]
		}
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			raw := authority[:at]
			if hasPass {
				_, raw, _ = strings.Cut(raw, ":")
			}
			if raw != cred {
				out = append(out, raw)
			}
		}
	}
	return out, true
}

func TargetAgentEnv(environ, auth, extra []string) (env, secrets []string) {
	env = []string{}
	withheld := WithheldNames(environ)
	add := func(v string) {
		if len(v) >= secretFloor && !slices.Contains(secrets, v) {
			secrets = append(secrets, v)
		}
	}
	vars := make([][2]string, 0, len(environ))
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok {
			vars = append(vars, [2]string{name, value})
		}
	}
	for _, v := range vars {
		if slices.Contains(auth, v[0]) {
			add(v[1])
		}
	}
	for _, v := range vars {
		if slices.Contains(withheld, v[0]) || secretName(v[0]) {
			add(v[1])
		}
	}
	for _, v := range vars {
		name, value := v[0], v[1]
		listed := slices.Contains(auth, name) || slices.Contains(targetBaseEnv, name) || strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "XDG_") ||
			(slices.Contains(extra, name) && TargetEnvProblem(name) == nil)
		proxy := slices.Contains(targetProxyEnv, name)
		if !listed && !proxy {
			continue
		}
		if slices.Contains(withheld, name) || strings.HasPrefix(name, "GIT_") || strings.HasPrefix(name, "SATELLE_") {
			continue
		}
		if secretName(name) && !slices.Contains(auth, name) {
			continue
		}
		if proxy {
			s, keep := proxySecrets(value)
			if !keep {
				continue
			}
			for _, x := range s {
				add(x)
			}
		}
		env = append(env, name+"="+value)
	}
	return env, secrets
}

func LogsDir(runtime string) string { return filepath.Join(runtime, "logs") }

var RuntimeKeyDirPattern = regexp.MustCompile(`^[^/]+-[0-9a-f]{8}$`)

func IsRuntimeKeyDir(name string) bool {
	return RuntimeKeyDirPattern.MatchString(name)
}

const RepoPathMarkerName = "repo.path"

func WriteRepoPathMarker(runtimeDir, repoRoot string, env []string) error {
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
	root := repoRoot
	var common string
	var ok bool
	if env != nil {
		common, _, ok = gitCommonIdentityEnv(repoRoot, env)
	} else {
		common, _, ok = gitCommonIdentity(repoRoot)
	}
	if ok {
		root = mainCheckoutRoot(repoRoot, common)
	}
	abs, err := filepath.Abs(root)
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
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	common, _, ok := gitCommonIdentity(root)
	if !ok {
		return root
	}
	return mainCheckoutRoot(root, common)
}

func CanonicalRepoRootEnv(repoRoot string, env []string) string {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		root = "."
	}
	common, _, ok := gitCommonIdentityEnv(root, env)
	if !ok {
		return root
	}
	return mainCheckoutRoot(root, common)
}

func mainCheckoutRoot(root, common string) string {
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
