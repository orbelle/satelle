package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Global config lives under ~/.satelle/ — the one machine-wide touchpoint the
// spec reserves (this machine's project list lives here too). It is kept in a
// file named config.toml, deliberately NOT satelle.toml, so the per-repo
// walk-up (which looks for .satelle/satelle.toml) can never mistake the global
// home for a repo root.
const (
	// GlobalConfigName is the global config filename under the global dir.
	GlobalConfigName = "config.toml"
	// DefaultServiceAddr is the bind address for the background service. Unlike
	// the transient `serve` default (127.0.0.1), the service defaults to all
	// interfaces so it is reachable across the WSL↔Windows boundary in any
	// networking mode. Restrict it to 127.0.0.1 in config to keep it off the LAN.
	DefaultServiceAddr = "0.0.0.0"
	// EnvServerEndpoint overrides [service] endpoint discovery and push
	// (sty_5aa08259 / sty_21a7d16d). Values:
	//
	//	unset          — use config / auto-bootstrap probe as usual
	//	URL            — use that base URL; no default-port probe
	//	none|off|-|""  — disable discovery AND push (hermetic tests set this)
	//
	// It lives here, not in internal/cli, because the serve-side mirror
	// reconciler honours the same off-switch and cannot import cli
	// (sty_e6e467fe).
	EnvServerEndpoint = "SATELLE_SERVER_ENDPOINT"
)

// ServerEndpointEnv inspects EnvServerEndpoint. disabled=true means discovery
// and push must not run. When disabled is false and endpoint is non-empty,
// callers should use it instead of config / probe.
func ServerEndpointEnv() (endpoint string, disabled bool, set bool) {
	v, ok := os.LookupEnv(EnvServerEndpoint)
	if !ok {
		return "", false, false
	}
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "none") || strings.EqualFold(v, "off") || v == "-" {
		return "", true, true
	}
	return v, false, true
}

// GlobalConfig is the machine-wide config at ~/.satelle/config.toml.
type GlobalConfig struct {
	Service  ServiceConfig      `toml:"service"`
	Projects ProjectsConfig     `toml:"projects"`
	UI       UIConfig           `toml:"ui"`
	Hosted   GlobalHostedConfig `toml:"hosted"`
}

// GlobalHostedConfig binds this MACHINE to one hosted satelle-server — the single
// account the operator signs in to, shared across every repo (sty_53ccf845). Like
// UIConfig.Theme it follows the operator rather than a repo, so one `satelle login`
// signs in everywhere. Secret-free: the OAuth tokens live in the per-user
// credential store outside any config (internal/hosted), never here.
type GlobalHostedConfig struct {
	Server string `toml:"server"`
	// Push = false turns every hosted push off for this machine while pulls and
	// the local web UI keep running (a stale machine that must not write). nil
	// means on. Machine-local: this file is never part of a sync bundle.
	Push *bool `toml:"push"`
}

// PushEnabled reports whether this machine may push to the hosted project.
func (h GlobalHostedConfig) PushEnabled() bool {
	return h.Push == nil || *h.Push
}

// MachinePushOff reports whether this machine's global config turns hosted push
// off, and names the setting (with its resolved path) for refusal messages. It
// re-reads the file on every call, so flipping the key needs no restart. An
// unreadable config reports push on: the push verbs surface that error themselves.
func MachinePushOff() (off bool, setting string) {
	return MachinePushOffIn(GlobalDir())
}

// MachinePushOffIn is MachinePushOff for the global config under dir, for a
// caller (doctor) that is handed the machine home rather than resolving it.
func MachinePushOffIn(dir string) (off bool, setting string) {
	path := filepath.Join(dir, GlobalConfigName)
	setting = fmt.Sprintf("[hosted] push = false in %s", path)
	gc, err := loadGlobalAt(path)
	if err != nil {
		return false, setting
	}
	return !gc.Hosted.PushEnabled(), setting
}

// ResolveServer returns the global hosted-server URL, trimmed of whitespace and a
// trailing slash so it matches the credential-store key exactly. Empty when unset.
func (h GlobalHostedConfig) ResolveServer() string {
	return strings.TrimRight(strings.TrimSpace(h.Server), "/")
}

// UIConfig holds user-level UI preferences shared across every repo, so the
// light/dark choice follows the operator rather than a single browser origin.
type UIConfig struct {
	Theme string `toml:"theme"` // "dark" | "light" (empty = light default)
}

// ProjectsConfig is this machine's projects: the repos or directories satelled
// shows. Each project's own .satelle stays the source of truth; this is just
// the list of paths.
type ProjectsConfig struct {
	Paths []string `toml:"paths"`
}

// Add adds an absolute project path, de-duplicated. Reports whether it was
// newly added.
func (p *ProjectsConfig) Add(path string) bool {
	for _, r := range p.Paths {
		if r == path {
			return false
		}
	}
	p.Paths = append(p.Paths, path)
	return true
}

// Remove drops a project path. Reports whether it was present.
func (p *ProjectsConfig) Remove(path string) bool {
	out := p.Paths[:0]
	found := false
	for _, r := range p.Paths {
		if r == path {
			found = true
			continue
		}
		out = append(out, r)
	}
	p.Paths = out
	return found
}

// ServiceConfig configures the background web service (`satelle service` /
// satelled). Listen keys are machine-scope only — never read from a repo file.
type ServiceConfig struct {
	// Port the service listens on; zero means DefaultWebPort.
	Port int `toml:"port"`
	// Addr the service binds; empty means DefaultServiceAddr (0.0.0.0).
	Addr string `toml:"addr"`
	// Endpoint is the CLI→satelled base URL (e.g. http://127.0.0.1:8787).
	// Empty derives http://127.0.0.1:<ResolvePort()>. SATELLE_SERVER_ENDPOINT
	// overrides; none|off|-|"" disables discovery and push.
	Endpoint string `toml:"endpoint"`
	// Repo is install-scope only: the working directory recorded by
	// `satelle service install` for the unit/status line. It is not "the repo
	// satelled serves" — the mirror is N partitions. Empty until install.
	Repo string `toml:"repo"`
}

// ResolvePort returns the service port, defaulting when unset.
func (s ServiceConfig) ResolvePort() int {
	if s.Port > 0 {
		return s.Port
	}
	return DefaultWebPort
}

// ResolveAddr returns the service bind address, defaulting when unset.
func (s ServiceConfig) ResolveAddr() string {
	if a := strings.TrimSpace(s.Addr); a != "" {
		return a
	}
	return DefaultServiceAddr
}

// ResolveEndpoint returns the CLI→satelled base URL: SATELLE_SERVER_ENDPOINT
// (none|off|-|"" disables), then [service] endpoint, then
// http://127.0.0.1:<ResolvePort()>.
func (s ServiceConfig) ResolveEndpoint() string {
	if ep, disabled, set := ServerEndpointEnv(); set {
		if disabled {
			return ""
		}
		return ep
	}
	if e := strings.TrimSpace(s.Endpoint); e != "" {
		return e
	}
	return fmt.Sprintf("http://127.0.0.1:%d", s.ResolvePort())
}

// GlobalDir returns the machine-wide satelle home (~/.satelle), honoring the
// SATELLE_HOME override (used by tests). Falls back to ".satelle-global" in CWD
// only if the home directory cannot be resolved.
//
// Under `go test` (testing.Testing()), resolving without SATELLE_HOME panics
// instead of writing the developer's real ~/.satelle (sty_c36c211f). That is
// the enforcement seam for every package: tests must call testutil.IsolateHome
// (or set SATELLE_HOME themselves). We import testing for testing.Testing() —
// available since go 1.22; this module is go 1.26 — rather than an Args[0]
// heuristic, so the signal is accurate for both `go test` and `go test -c`.
func GlobalDir() string {
	if v := strings.TrimSpace(os.Getenv("SATELLE_HOME")); v != "" {
		return v
	}
	if testing.Testing() {
		panic("satelle: test resolved GlobalDir() with no SATELLE_HOME set — use testutil.IsolateHome(t); refusing to touch the real ~/.satelle")
	}
	return defaultGlobalDir()
}

func defaultGlobalDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".satelle-global"
	}
	return filepath.Join(home, ".satelle")
}

// IsGlobalDir reports whether dir is the machine-wide satelle home. Unlike
// GlobalDir it never panics, so a read-only repo walk-up can ask it: the global
// home is named .satelle like a repo's data dir and must not be mistaken for one.
func IsGlobalDir(dir string) bool {
	g := strings.TrimSpace(os.Getenv("SATELLE_HOME"))
	if g == "" {
		g = defaultGlobalDir()
	}
	if filepath.Clean(dir) == filepath.Clean(g) {
		return true
	}
	di, err := os.Stat(dir)
	if err != nil {
		return false
	}
	gi, err := os.Stat(g)
	return err == nil && os.SameFile(di, gi)
}

// GlobalConfigPath returns the path to the global config file.
func GlobalConfigPath() string {
	return filepath.Join(GlobalDir(), GlobalConfigName)
}

// LoadGlobal reads the global config, returning a zero-value GlobalConfig (which
// resolves to defaults) when the file is absent. A present-but-malformed file is
// an error.
func LoadGlobal() (GlobalConfig, error) {
	return loadGlobalAt(GlobalConfigPath())
}

func loadGlobalAt(path string) (GlobalConfig, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return GlobalConfig{}, nil
	}
	if err != nil {
		return GlobalConfig{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var gc GlobalConfig
	if _, err := toml.Decode(string(b), &gc); err != nil {
		return GlobalConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	// A machine config written before projects were named projects keeps its
	// list: the old [workspace] repos table is read once and carried into
	// [projects] paths; the next save writes only [projects].
	var legacy struct {
		Old struct {
			Repos []string `toml:"repos"`
		} `toml:"workspace"`
	}
	if _, err := toml.Decode(string(b), &legacy); err == nil && len(gc.Projects.Paths) == 0 {
		gc.Projects.Paths = legacy.Old.Repos
	}
	return gc, nil
}

// SaveGlobal writes the global config to ~/.satelle/config.toml, creating the
// dir as needed. It renders a documented template (a fixed, satelle-managed
// shape) from the resolved values rather than re-encoding, so the file stays
// readable and self-explanatory.
func SaveGlobal(gc GlobalConfig) error {
	dir := GlobalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	repos := "[]"
	if len(gc.Projects.Paths) > 0 {
		quoted := make([]string, len(gc.Projects.Paths))
		for i, r := range gc.Projects.Paths {
			quoted[i] = fmt.Sprintf("%q", r)
		}
		repos = "[" + strings.Join(quoted, ", ") + "]"
	}
	body := fmt.Sprintf(globalTemplate, gc.Service.ResolvePort(), gc.Service.ResolveAddr(), strings.TrimSpace(gc.Service.Endpoint), gc.Service.Repo, repos, gc.UI.Theme, gc.Hosted.ResolveServer())
	body += renderHostedPush(gc.Hosted.Push)
	path := GlobalConfigPath()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// SaveGlobalHostedServer records the machine-wide hosted-server URL in the global
// config, preserving the other sections (load-modify-save is correct here because
// SaveGlobal re-renders the whole managed template). It is the global analogue of
// the retired repo-file SaveHostedServer; `satelle login` calls it so one sign-in
// serves every repo. Tokens never pass through here — they go to the credential
// store.
func SaveGlobalHostedServer(server string) error {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		return fmt.Errorf("config: hosted server URL is empty")
	}
	gc, err := LoadGlobal()
	if err != nil {
		return err
	}
	gc.Hosted.Server = server
	return SaveGlobal(gc)
}

// ClearGlobalHostedServer removes the machine-wide hosted-server binding (the
// "remove server" action), leaving the other sections intact. Separate from
// SaveGlobalHostedServer, which guards against an empty URL.
func ClearGlobalHostedServer() error {
	gc, err := LoadGlobal()
	if err != nil {
		return err
	}
	gc.Hosted.Server = ""
	return SaveGlobal(gc)
}

// renderHostedPush continues the template's [hosted] section with the push key,
// commented out while it is unset so the default stays on.
func renderHostedPush(push *bool) string {
	const doc = "# push = false turns every hosted push off for this machine; pulls and the\n" +
		"# local web UI keep running. Never synced.\n"
	if push == nil {
		return doc + "# push = false\n"
	}
	return doc + fmt.Sprintf("push = %t\n", *push)
}

// globalTemplate is the documented global config shape. Order/format are fixed
// so SaveGlobal produces a stable, human-readable file.
const globalTemplate = `# satelle global config (machine-wide, ~/.satelle/config.toml).
# Managed by ` + "`satelle service`" + `; safe to hand-edit, then re-run
# ` + "`satelle service install`" + ` to apply changes to the running service.

[service]
# port the background web service listens on.
port = %d
# addr it binds. 0.0.0.0 is reachable from Windows when satelle runs in WSL;
# set to "127.0.0.1" to keep the service off the local network.
addr = %q
# endpoint the CLI publishes mutation events to. Unset derives
# http://127.0.0.1:<port>. SATELLE_SERVER_ENDPOINT=none disables push.
endpoint = %q
# repo is install-scope only: the working directory recorded by
# 'service install' for the unit/status line. Not "the repo satelled serves"
# — the mirror holds N partitions.
repo = %q

[projects]
# this machine's projects (repos or directories) satelled shows. Manage with
# 'satelle project add|remove|list'.
paths = %s

[ui]
# light/dark theme shared across every repo's web UI ("dark" | "" = light).
# Set by the theme toggle in the web header; follows the operator across repos.
theme = %q

[hosted]
# the hosted satelle-server this machine signs in to (e.g. https://satelle.dev).
# Set by 'satelle login --server <url>'; follows the operator across repos so one
# sign-in serves every project. Tokens are stored separately (never here).
server = %q
`
