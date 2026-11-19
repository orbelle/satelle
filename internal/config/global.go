package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const (
	GlobalConfigName   = "config.toml"
	DefaultServiceAddr = "0.0.0.0"
	EnvServerEndpoint  = "SATELLE_SERVER_ENDPOINT"
)

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

type GlobalConfig struct {
	Service  ServiceConfig      `toml:"service"`
	Projects ProjectsConfig     `toml:"projects"`
	Upgrade  UpgradeConfig      `toml:"upgrade"`
	UI       UIConfig           `toml:"ui"`
	Hosted   GlobalHostedConfig `toml:"hosted"`
}

type GlobalHostedConfig struct {
	Server string `toml:"server"`
	Push   *bool  `toml:"push"`
}

func (h GlobalHostedConfig) PushEnabled() bool {
	return h.Push == nil || *h.Push
}

func MachinePushOff() (off bool, setting string) {
	return MachinePushOffIn(GlobalDir())
}

func MachinePushOffIn(dir string) (off bool, setting string) {
	path := filepath.Join(dir, GlobalConfigName)
	setting = fmt.Sprintf("[hosted] push = false in %s", path)
	gc, err := loadGlobalAt(path)
	if err != nil {
		return false, setting
	}
	return !gc.Hosted.PushEnabled(), setting
}

func (h GlobalHostedConfig) ResolveServer() string {
	return strings.TrimRight(strings.TrimSpace(h.Server), "/")
}

type UIConfig struct {
	Theme string `toml:"theme"`
}

type UpgradeConfig struct {
	StartClean []string `toml:"start_clean"`
}

func (gc GlobalConfig) StartCleanFor(repoRoot string) (bool, string) {
	resolved := func(p string) string {
		root := filepath.Clean(CanonicalRepoRoot(p))
		if r, err := filepath.EvalSymlinks(root); err == nil {
			return r
		}
		return root
	}
	want := resolved(repoRoot)
	for _, r := range gc.Upgrade.StartClean {
		if resolved(r) == want {
			return true, "[upgrade] start_clean in " + GlobalConfigPath()
		}
	}
	return false, ""
}

func tomlStrings(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

type ProjectsConfig struct {
	Paths []string `toml:"paths"`
}

func (p ProjectsConfig) Keys() []string {
	release := MemoGitIdentity()
	defer release()
	keys := make([]string, len(p.Paths))
	for i, path := range p.Paths {
		keys[i] = RepoKey(path)
	}
	return keys
}

func (p *ProjectsConfig) Add(path string) bool {
	for _, r := range p.Paths {
		if r == path {
			return false
		}
	}
	p.Paths = append(p.Paths, path)
	return true
}

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

type ServiceConfig struct {
	Port     int    `toml:"port"`
	Addr     string `toml:"addr"`
	Endpoint string `toml:"endpoint"`
	Repo     string `toml:"repo"`
}

func (s ServiceConfig) ResolvePort() int {
	if s.Port > 0 {
		return s.Port
	}
	return DefaultWebPort
}

func (s ServiceConfig) ResolveAddr() string {
	if a := strings.TrimSpace(s.Addr); a != "" {
		return a
	}
	return DefaultServiceAddr
}

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

func GlobalConfigPath() string {
	return filepath.Join(GlobalDir(), GlobalConfigName)
}

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
	md, err := toml.Decode(string(b), &gc)
	if err != nil {
		return GlobalConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if md.IsDefined("workspace") {
		return GlobalConfig{}, fmt.Errorf("config: %s has a [workspace] table, which satelle no longer reads: list this machine's projects as [projects] paths = [\"/path/to/repo\", ...] and delete [workspace]", path)
	}
	return gc, nil
}

func SaveGlobal(gc GlobalConfig) error {
	dir := GlobalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	body := fmt.Sprintf(globalTemplate, gc.Service.ResolvePort(), gc.Service.ResolveAddr(), strings.TrimSpace(gc.Service.Endpoint), gc.Service.Repo,
		tomlStrings(gc.Projects.Paths), tomlStrings(gc.Upgrade.StartClean), gc.UI.Theme, gc.Hosted.ResolveServer())
	body += renderHostedPush(gc.Hosted.Push)
	path := GlobalConfigPath()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

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

func ClearGlobalHostedServer() error {
	gc, err := LoadGlobal()
	if err != nil {
		return err
	}
	gc.Hosted.Server = ""
	return SaveGlobal(gc)
}

func renderHostedPush(push *bool) string {
	const doc = "# push = false turns every hosted push off for this machine; pulls and the\n" +
		"# local web UI keep running. Never synced.\n"
	if push == nil {
		return doc + "# push = false\n"
	}
	return doc + fmt.Sprintf("push = %t\n", *push)
}

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

[upgrade]
# repo roots that start clean on v1: 'satelle upgrade --archive' still archives
# their old .satelle, but 'satelle upgrade --from' refuses to import their old
# ledger (see 'satelle help upgrade').
start_clean = %s

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
