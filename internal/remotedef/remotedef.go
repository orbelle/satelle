// Package remotedef reads the part of a remote definition that a node needs: the
// secrets the remote declares, the repos it fetches and the egress it is allowed.
// A node receives the definition from satelle.dev, in the bundle it fetches with
// its own node token, and Parse is the only reader of its remote.toml.
//
// The format and its validation are satelle-server's internal/remotedef: the
// version a node receives was validated there, so Parse decodes only what the
// node uses and tolerates every other key, including any the server adds later.
// It runs nothing, opens nothing and holds no credential.
package remotedef

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// The two scopes a secret is stored under on satelle.dev.
const (
	ScopePersonal = "personal"
	ScopeTeam     = "team"
)

// FromEnv is the value of a secret's From that reads its key from the environment.
const FromEnv = "env"

// FromGitHubApp is the value of a secret's From whose value satelle.dev mints for
// each run: a GitHub App installation token scoped to the definition's [[repo]]
// entries on the secret's Host, revoked when the run ends (satelle-server
// sty_84e32fda). A node reads it like any other key and never mints it.
const FromGitHubApp = "github-app"

// DefaultGitHubHost is the GitHub a github-app secret is minted for when it names
// no host.
const DefaultGitHubHost = "github.com"

// SecretDecl is one secret a remote reads from satelle.dev. Key is the name it is
// stored and read under; Scope is ScopePersonal or ScopeTeam; From says where its
// value comes from (FromEnv, FromGitHubApp, or an argv that prints it); Workspace
// is the team workspace id, for team scope only; Host is the GitHub a github-app
// secret is minted for (DefaultGitHubHost when empty); Permissions is the GitHub
// App permission set satelle.dev mints a github-app secret's token with, name to
// "read" or "write", required for a github-app secret and refused on any other
// (satelle-server sty_5b905ad8).
type SecretDecl struct {
	Key         string            `toml:"key"`
	Scope       string            `toml:"scope"`
	From        any               `toml:"from"`
	Workspace   string            `toml:"workspace"`
	Host        string            `toml:"host"`
	Permissions map[string]string `toml:"permissions"`
}

// IsGitHubApp reports whether the secret's value is a github-app installation
// token.
func (s SecretDecl) IsGitHubApp() bool {
	from, ok := s.From.(string)
	return ok && from == FromGitHubApp
}

// GitHubHost is the GitHub a github-app secret is minted for: Host, or
// DefaultGitHubHost when it names none.
func (s SecretDecl) GitHubHost() string {
	if h := strings.TrimSpace(s.Host); h != "" {
		return h
	}
	return DefaultGitHubHost
}

// Repo is one repository the node fetches, read-only: an https URL and the ref
// (branch, tag or commit) to check out.
type Repo struct {
	URL string `toml:"url"`
	Ref string `toml:"ref"`
}

// Network is the node's egress beyond what the definition already implies.
type Network struct {
	Allow []string `toml:"allow"`
}

// What a wake of the remote drives (Drive.Kind). satelle-server validates the
// field; a node only reads it.
const (
	// DriveStory: each wake drives a story through host.story_command. It is the
	// meaning of a definition that declares no [drive] table.
	DriveStory = "story"
	// DriveTask: each wake runs one execution of the task Drive.Task names.
	DriveTask = "task"
)

// TaskIDPrefix prefixes a task's bare name to make its item id, the convention
// verb.SyncTasks ingests task files under (tasks/tsk_<name>.md).
const TaskIDPrefix = "tsk_"

// Drive is the [drive] table: what a wake of the remote runs. Task is the bare
// name of the task, as satelle-server validates it, not its item id.
type Drive struct {
	Kind string `toml:"kind"`
	Task string `toml:"task"`
}

// Host is the [host] table as far as a node reads it: the commands the node runs
// once, after it has read its definition and before it works a run. The server's
// own host commands (start, network, run, stop) are not the node's and are not
// decoded.
type Host struct {
	Bootstrap [][]string `toml:"bootstrap"`
}

// Definition is the part of remote.toml a node reads.
type Definition struct {
	Secret  []SecretDecl `toml:"secret"`
	Repos   []Repo       `toml:"repo"`
	Network Network      `toml:"network"`
	Drive   Drive        `toml:"drive"`
	Host    Host         `toml:"host"`
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
