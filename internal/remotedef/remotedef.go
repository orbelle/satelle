package remotedef

import (
	"maps"
	"net"
	"net/url"
	"path"
	"slices"
	"strings"
)

const SchemaVersion = 1

type Definition struct {
	Version        int       `toml:"version" json:"version"`
	Name           string    `toml:"name" json:"name,omitempty"`
	Provider       Provider  `toml:"provider" json:"provider"`
	SatelleVersion string    `toml:"satelle_version" json:"satelle_version"`
	Machine        Machine   `toml:"machine" json:"machine,omitzero"`
	Setup          Setup     `toml:"setup" json:"setup,omitzero"`
	Secrets        Secrets   `toml:"secrets" json:"secrets,omitzero"`
	Repos          []Repo    `toml:"repo" json:"repo,omitempty"`
	Network        Network   `toml:"network" json:"network,omitzero"`
	Lifecycle      Lifecycle `toml:"lifecycle" json:"lifecycle,omitzero"`
	Drive          Drive     `toml:"drive" json:"drive,omitzero"`
	Connect        Connect   `toml:"connect" json:"connect,omitzero"`
}

type Provider string

const (
	ProviderDockerLocal Provider = "docker-local"
	ProviderFlySprite   Provider = "fly-sprite"
	ProviderCommand     Provider = "command"
)

var Providers = []Provider{ProviderDockerLocal, ProviderFlySprite, ProviderCommand}

type Connect struct {
	Check  []string `toml:"check" json:"check,omitempty"`
	Start  []string `toml:"start" json:"start,omitempty"`
	CopyIn []string `toml:"copy_in" json:"copy_in,omitempty"`
	Exec   []string `toml:"exec" json:"exec,omitempty"`
	Stop   []string `toml:"stop" json:"stop,omitempty"`
}

const (
	PlaceholderNode = "{node}"
	PlaceholderSrc  = "{src}"
	PlaceholderDst  = "{dst}"
)

type Machine struct {
	Region string `toml:"region" json:"region,omitempty"`
	Size   string `toml:"size" json:"size,omitempty"`
	Swap   string `toml:"swap" json:"swap,omitempty"`
	Image  string `toml:"image" json:"image,omitempty"`
}

type Setup struct {
	OnCreate  []string `toml:"on_create" json:"on_create,omitempty"`
	PostStart []string `toml:"post_start" json:"post_start,omitempty"`
}

const SecretsProviderSatelle = "satelle"

type Secrets struct {
	Provider string                   `toml:"provider" json:"provider,omitempty"`
	Names    []string                 `toml:"names" json:"names,omitempty"`
	Override map[string]OverrideValue `toml:"override" json:"override,omitempty"`
}

type OverrideValue any

const OverrideGitHubApp = "github-app"

const overrideSatellePrefix = "satelle:"

const DefaultGitHubHost = "github.com"

type SecretSource string

const (
	SourceBackend  SecretSource = "backend"
	SourceSatelle  SecretSource = "satelle"
	SourceRunToken SecretSource = "github-app"
)

type SecretEntry struct {
	Key         string
	Source      SecretSource
	Name        string
	Permissions map[string]string
}

func (d Definition) SecretEntries() []SecretEntry {
	var out []SecretEntry
	for _, n := range d.Secrets.Names {
		if _, over := d.Secrets.Override[n]; !over {
			out = append(out, SecretEntry{Key: n, Source: SourceBackend, Name: n})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(d.Secrets.Override)) {
		if e, ok := parseOverride(k, d.Secrets.Override[k]); ok {
			out = append(out, e)
		}
	}
	return out
}

func parseOverride(key string, v OverrideValue) (SecretEntry, bool) {
	switch t := v.(type) {
	case string:
		name, ok := strings.CutPrefix(t, overrideSatellePrefix)
		if !ok || name == "" {
			return SecretEntry{}, false
		}
		return SecretEntry{Key: key, Source: SourceSatelle, Name: name}, true
	case map[string]any:
		if p, _ := t["provider"].(string); p != OverrideGitHubApp {
			return SecretEntry{}, false
		}
		perms := map[string]string{}
		raw, _ := t["permissions"].(map[string]any)
		for n, l := range raw {
			if s, ok := l.(string); ok {
				perms[n] = s
			}
		}
		return SecretEntry{Key: key, Source: SourceRunToken, Name: key, Permissions: perms}, true
	}
	return SecretEntry{}, false
}

type Repo struct {
	URL string `toml:"url" json:"url"`
	Ref string `toml:"ref" json:"ref"`
}

type Network struct {
	Allow []string `toml:"allow" json:"allow,omitempty"`
}

func (n Network) Permits(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || strings.ContainsAny(host, "%[]") {
		return false
	}
	literal := net.ParseIP(host) != nil || !dnsName(host)
	for _, e := range n.Allow {
		e = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(e)), ".")
		if e == "" {
			continue
		}
		if e == host {
			return true
		}
		if d, ok := strings.CutPrefix(e, "*."); ok && !literal && d != "" && strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func dnsName(host string) bool {
	for _, c := range host {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

type LifecycleScope string

const (
	ScopeRun LifecycleScope = "run"
	ScopeJob LifecycleScope = "job"
)

var LifecycleScopes = []LifecycleScope{ScopeRun, ScopeJob}

type Lifecycle struct {
	Scope       LifecycleScope `toml:"scope" json:"scope,omitempty"`
	IdleDestroy string         `toml:"idle_destroy" json:"idle_destroy,omitempty"`
	MaxNodes    *int           `toml:"max_nodes" json:"max_nodes,omitempty"`
	KeepWarm    *int           `toml:"keep_warm" json:"keep_warm,omitempty"`
}

type DriveKind string

const (
	DriveTask DriveKind = "task"
	DriveItem DriveKind = "item"
)

var DriveKinds = []DriveKind{DriveTask, DriveItem}

func (k DriveKind) KeepsWarm() bool { return k == DriveTask }

type Drive struct {
	Kind     DriveKind `toml:"kind" json:"kind,omitempty"`
	Task     string    `toml:"task" json:"task,omitempty"`
	Checkout bool      `toml:"checkout" json:"checkout,omitempty"`
}

func (d Definition) RunsTask() bool { return d.Drive.Kind == DriveTask }

func (d Definition) Allow() []string { return d.Network.Allow }

func RepoName(repoURL string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".git")
}
