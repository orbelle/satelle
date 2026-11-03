// Package remotedef reads and validates a remote definition (remote.toml),
// schema v1. A node receives the definition from satelle.dev, in the bundle it
// fetches with its own node token, and Parse is the only reader of its
// remote.toml.
//
// The format and its rules are satelle-server's internal/remotedef: this package
// mirrors its keys, rules and finding paths (satelle-server 0.1.265, commit
// 02e27f8) so a node refuses exactly what the server refuses. v1 is the only
// shape. A definition in the earlier shape is refused, each old key named with
// its v1 replacement, and there is no second reader for it.
//
// The definition is data. Nothing here runs a command, opens a file or holds a
// credential, and no finding carries a submitted value.
package remotedef

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"
)

// SchemaVersion is the one definition schema version this package reads.
const SchemaVersion = 1

// Definition is the decoded remote.toml, schema v1. The toml tags are the
// accepted key set and the json tags give the same names, as on the server, so
// the decode of the shape-checked table is a JSON round trip.
type Definition struct {
	// Version is the schema version; it must be 1.
	Version int `toml:"version" json:"version"`
	// Name is the definition's own name, as the team knows it.
	Name string `toml:"name" json:"name,omitempty"`
	// Provider is the platform that hosts the node (see Providers).
	Provider Provider `toml:"provider" json:"provider"`
	// SatelleVersion pins the satelle the node runs (MAJOR.MINOR.PATCH).
	SatelleVersion string `toml:"satelle_version" json:"satelle_version"`
	// Environment is the vault environment the definition's secrets resolve in
	// (such as dev, stg or prd). It is required when [secrets] declares a name or
	// a satelle: override. The client checks its form; the server also checks it
	// against the workspace's environments and takes it from the stored
	// definition, never from the node.
	Environment string `toml:"environment" json:"environment,omitempty"`
	// Machine is the workload's machine, carried to the node unchanged.
	Machine Machine `toml:"machine" json:"machine,omitzero"`
	// Setup is what the node runs as it comes up.
	Setup Setup `toml:"setup" json:"setup,omitzero"`
	// Identity is who the remote acts as: validated and carried, not acted on.
	Identity Identity `toml:"identity" json:"identity,omitzero"`
	// Secrets are the keys the node reads; a value never appears.
	Secrets Secrets `toml:"secrets" json:"secrets,omitzero"`
	// Repos are fetched onto the node read-only, each at its ref: the checkout
	// fence (remotenode.FetchRepos refuses anything else).
	Repos   []Repo  `toml:"repo" json:"repo,omitempty"`
	Network Network `toml:"network" json:"network,omitzero"`
	// Lifecycle is how long a node lives and how many may live at once.
	Lifecycle Lifecycle `toml:"lifecycle" json:"lifecycle,omitzero"`
	// Trigger is the event that starts a node; nil starts one only on request.
	Trigger *Trigger `toml:"trigger" json:"trigger,omitempty"`
	// Drive says what drives the node.
	Drive Drive `toml:"drive" json:"drive,omitzero"`
}

// Provider is a platform that hosts a node.
type Provider string

// ProviderFlySprite hosts the node on a Fly sprite.
const ProviderFlySprite Provider = "fly-sprite"

// Providers lists every Provider. The server maps each to a runner catalog
// type; a node checks only that the provider is one of them.
var Providers = []Provider{ProviderFlySprite}

// Machine is the [machine] table: workload data, validated at machine.<key> and
// carried to the node unchanged. Swap is a size: digits then K, M or G; the node
// makes a fresh swapfile of it for each job (remotenode.EnableSwap).
type Machine struct {
	Region string `toml:"region" json:"region,omitempty"`
	Size   string `toml:"size" json:"size,omitempty"`
	Swap   string `toml:"swap" json:"swap,omitempty"`
	Image  string `toml:"image" json:"image,omitempty"`
}

// Setup is the [setup] table: shell commands the node runs, each one string.
// OnCreate runs once, after the node joins and reads this definition and before
// it works the run; PostStart runs each time the node starts. A failing command
// fails the node's start.
type Setup struct {
	OnCreate  []string `toml:"on_create" json:"on_create,omitempty"`
	PostStart []string `toml:"post_start" json:"post_start,omitempty"`
}

// Identity is the [identity] table. Principal is who the remote acts as; it is
// validated and carried, not yet acted on.
type Identity struct {
	Principal string `toml:"principal" json:"principal,omitempty"`
}

// SecretsProviderSatelle is the secrets backend [secrets] names resolve from:
// satelle-vault's own sealed store. Omitting secrets.provider means the same, and
// it is the only explicit value accepted.
const SecretsProviderSatelle = "satelle"

// retiredSecretsProviderInfisical is the backend satelle-vault retired (satelle-
// server sty_02359fe3); a definition that still names it is refused with the
// replacement.
const retiredSecretsProviderInfisical = "infisical"

// Secrets is the [secrets] table. Each of Names is a key resolved server-side
// from the secrets backend. Override maps a key the node reads to another
// source: a GitHub App run token ({ provider = "github-app", permissions = {…} })
// or a team value satelle holds in its own store ("satelle:<name>"). The node
// reads every one by its key through the node API and never resolves one itself.
type Secrets struct {
	Provider string                   `toml:"provider" json:"provider,omitempty"`
	Names    []string                 `toml:"names" json:"names,omitempty"`
	Override map[string]OverrideValue `toml:"override" json:"override,omitempty"`
}

// OverrideValue is one [secrets.override] entry: the string "satelle:<name>", or
// a table { provider = "github-app", permissions = { <name> = "read"|"write" } }.
// It is untyped so each form is judged at its own path.
type OverrideValue any

// OverrideGitHubApp is the provider of an override whose value is a run's GitHub
// installation token, minted by satelle.dev and limited to the definition's
// [[repo]] entries on github.com.
const OverrideGitHubApp = "github-app"

// overrideSatellePrefix opens an override held in satelle's own store.
const overrideSatellePrefix = "satelle:"

// DefaultGitHubHost is the host a github-app override mints for.
const DefaultGitHubHost = "github.com"

// SecretSource is where one key the node reads comes from.
type SecretSource string

// The sources of a key the node reads.
const (
	// SourceBackend is a [secrets] name, read from the secrets backend.
	SourceBackend SecretSource = "backend"
	// SourceSatelle is a "satelle:<name>" override, read from satelle's store.
	SourceSatelle SecretSource = "satelle"
	// SourceRunToken is a github-app override: a run token minted for the node.
	SourceRunToken SecretSource = "github-app"
)

// SecretEntry is one key the node reads and where it comes from. Name is the key
// the value is held under: the [secrets] name itself, the <name> of
// "satelle:<name>", or the key for a github-app run token. Permissions are a
// github-app override's, exactly.
type SecretEntry struct {
	Key         string
	Source      SecretSource
	Name        string
	Permissions map[string]string
}

// SecretEntries lists every key the definition's node reads, each once: the
// [secrets] names in declaration order, then the overrides in key order. An
// override entry that does not parse is left out (the validator names it), as is
// a name an override also carries.
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

// GitHubAppKey is the key of the definition's first github-app override, in key
// order, when its host is host ("" matches any): the variable a run's GitHub
// token is held in. ok is false when the definition declares none.
func (d Definition) GitHubAppKey(host string) (key string, ok bool) {
	for _, e := range d.SecretEntries() {
		if e.Source == SourceRunToken && (host == "" || strings.EqualFold(host, DefaultGitHubHost)) {
			return e.Key, true
		}
	}
	return "", false
}

// parseOverride reads one override value into its entry; ok is false for a value
// in neither form.
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

// Repo is one repository the node fetches, read-only: an https URL and the ref
// (branch, tag or commit) to check out.
type Repo struct {
	URL string `toml:"url" json:"url"`
	Ref string `toml:"ref" json:"ref"`
}

// Network is the node's egress beyond what the definition already implies.
type Network struct {
	// Allow are extra hostnames (or *.domain) the node may reach.
	Allow []string `toml:"allow" json:"allow,omitempty"`
}

// LifecycleScope is how long a node lives.
type LifecycleScope string

// The scopes a node may live for.
const (
	// ScopeRun: the node is stopped when its run ends (the default).
	ScopeRun LifecycleScope = "run"
	// ScopeJob: the node is left running when its run ends.
	ScopeJob LifecycleScope = "job"
)

// LifecycleScopes lists every LifecycleScope.
var LifecycleScopes = []LifecycleScope{ScopeRun, ScopeJob}

// Lifecycle is the [lifecycle] table. IdleDestroy is a positive duration an
// idle node may live; MaxNodes, when set, caps the live nodes of the definition
// per remote. KeepWarm, for a task drive only, is how many idle nodes the server
// keeps warm (0, the default, keeps none), never above MaxNodes.
type Lifecycle struct {
	Scope       LifecycleScope `toml:"scope" json:"scope,omitempty"`
	IdleDestroy string         `toml:"idle_destroy" json:"idle_destroy,omitempty"`
	MaxNodes    *int           `toml:"max_nodes" json:"max_nodes,omitempty"`
	KeepWarm    *int           `toml:"keep_warm" json:"keep_warm,omitempty"`
}

// Trigger is the [trigger] table. On is "story.status:<status>", a story
// entering <status>. A definition with no [trigger] starts a node only on request.
type Trigger struct {
	On string `toml:"on" json:"on"`
}

// TriggerOnStatusPrefix opens a trigger that fires on a story entering a status.
const TriggerOnStatusPrefix = "story.status:"

// DriveKind is what drives a remote.
type DriveKind string

// The kinds a remote may be driven by.
const (
	// DriveTask: each wake runs one execution of the task Drive.Task names.
	DriveTask DriveKind = "task"
	// DriveStory: a node is woken for one story and drives it.
	DriveStory DriveKind = "story"
)

// DriveKinds lists every DriveKind.
var DriveKinds = []DriveKind{DriveTask, DriveStory}

// KeepsWarm reports whether [lifecycle] keep_warm is accepted for this drive
// kind: a task node waits for work, a story node is woken for one story.
func (k DriveKind) KeepsWarm() bool { return k == DriveTask }

// TaskIDPrefix prefixes a task's bare name to make its item id, the convention
// verb.SyncTasks ingests task files under (tasks/tsk_<name>.md).
const TaskIDPrefix = "tsk_"

// Drive is the [drive] table. Task is the bare name of the task a task-kind wake
// runs, not its item id.
type Drive struct {
	Kind DriveKind `toml:"kind" json:"kind,omitempty"`
	Task string    `toml:"task" json:"task,omitempty"`
}

// RunsTask reports whether a wake of the remote runs a task.
func (d Definition) RunsTask() bool { return d.Drive.Kind == DriveTask }

// TaskID is the item id of the task a wake runs: TaskIDPrefix + Drive.Task.
func (d Definition) TaskID() string { return TaskIDPrefix + d.Drive.Task }

// Allow is the egress allowlist the definition declares.
func (d Definition) Allow() []string { return d.Network.Allow }

// RepoName is the directory a repo is cloned into: the last element of its URL
// path without a .git suffix.
func RepoName(repoURL string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".git")
}

// RepoSlug is a repo URL's host and owner/name path: github.com and
// orbelle/satelle for https://github.com/orbelle/satelle.git. ok is false for a
// URL that is not host/owner/name.
func RepoSlug(repoURL string) (host, slug string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if strings.Count(p, "/") != 1 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return "", "", false
	}
	return u.Hostname(), p, true
}

// EnvDefinition is the environment variable a node's join sets, for the life of
// the fire, to the definition it read (EnvValue), so a command the run starts —
// `satelle pr` on a PR-approval remote — knows the repo and the credential the
// remote declares. It carries key names, never a secret's value.
const EnvDefinition = "SATELLE_NODE_DEFINITION"

// EnvValue is d as EnvDefinition carries it.
func (d Definition) EnvValue() (string, error) {
	b, err := json.Marshal(d)
	return string(b), err
}

// FromEnvValue reads a definition EnvValue wrote.
func FromEnvValue(v string) (Definition, error) {
	var d Definition
	if err := json.Unmarshal([]byte(v), &d); err != nil {
		return Definition{}, fmt.Errorf("%s: %w", EnvDefinition, err)
	}
	return d, nil
}
