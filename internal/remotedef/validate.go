package remotedef

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// The keys, rules, finding paths and messages below mirror satelle-server's
// internal/remotedef/validate.go at 02e27f8 (satelle-server 0.1.265). The checks
// that need server state are not run here: the runner catalog, remote_id
// visibility and whether each secret is held. The one rule the client adds is
// keep_warm never above max_nodes (checkLifecycle).

// Severities a Finding carries. Only an error makes a definition invalid.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Finding is one defect. Key is the path of the key at fault in remote.toml; the
// message never carries a submitted value.
type Finding struct {
	Severity string `json:"severity"`
	Key      string `json:"key"`
	Message  string `json:"message"`
}

// Validate checks a remote.toml body against schema v1 and returns every
// finding, in the order the checks run, so one defect never hides another. A
// definition in the earlier shape is refused at each old key with the v1 key
// that replaced it.
func Validate(body []byte) []Finding {
	_, findings := check(body)
	return findings
}

// Errors are the error findings of fs.
func Errors(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == SeverityError {
			out = append(out, f)
		}
	}
	return out
}

type validator struct{ out []Finding }

func (v *validator) fail(key, msg string) {
	v.out = append(v.out, Finding{SeverityError, key, msg})
}

// check judges remote.toml's own text and returns the decoded definition with
// the findings: the credential rule, its shape against the v1 type (every key
// outside v1 and every value of the wrong type is a finding at its path, an
// earlier-shape key naming its v1 replacement), then each v1 rule.
func check(body []byte) (Definition, []Finding) {
	v := &validator{}
	var raw map[string]any
	if _, err := toml.Decode(string(body), &raw); err != nil {
		v.fail("parse", parseMessage(err))
		return Definition{}, v.out
	}
	if raw == nil {
		raw = map[string]any{}
	}
	_, hadVersion := raw["version"]
	// The credential rule reads the definition as written, old and unknown keys
	// included, so a literal credential is named wherever it sits.
	v.checkCredentials(raw)
	// A key of the wrong type is named here and dropped from raw, so the rest of
	// the definition still decodes and is judged.
	v.shapeTable("", raw, reflect.TypeFor[Definition]())
	def, err := decodeRaw(raw)
	if err != nil {
		v.fail("parse", "a value has a type the definition does not accept")
		return Definition{}, v.out
	}

	_, versionOK := raw["version"]
	switch {
	case !hadVersion:
		v.fail("version", "required: version = 1, the definition schema this server reads")
	case versionOK && def.Version != SchemaVersion:
		v.fail("version", "must be 1, the definition schema this server reads")
	}
	if def.Name != "" && !namePattern.MatchString(def.Name) {
		v.fail("name", "must be lower-case letters, digits, - or _, starting with a letter or digit")
	}
	switch {
	case strings.TrimSpace(def.SatelleVersion) == "":
		v.fail("satelle_version", "required: pin the satelle version as MAJOR.MINOR.PATCH")
	case !versionPattern.MatchString(def.SatelleVersion):
		v.fail("satelle_version", "is not a MAJOR.MINOR.PATCH version")
	}
	v.checkProvider(def)
	v.checkMachine(def.Machine, sub(raw, "machine"))
	v.checkSetup(def.Setup)
	if _, ok := raw["identity"]; ok && strings.TrimSpace(def.Identity.Principal) == "" {
		v.fail("identity.principal", "required when [identity] is present: who the remote acts as")
	}
	v.checkSecrets(def)
	v.checkEnvironment(def)
	v.checkRepos(def)
	for i, h := range def.Network.Allow {
		if !hostPattern.MatchString(h) {
			v.fail(fmt.Sprintf("network.allow[%d]", i), "is not a hostname or *.domain")
		}
	}
	v.checkLifecycle(def.Lifecycle, def.Drive.Kind)
	v.checkTrigger(def.Trigger)
	v.checkDrive(def.Drive)
	return def, v.out
}

// decodeRaw decodes the shape-checked raw table into a Definition. The toml and
// json tags name the same keys, so a JSON round trip of the raw table is the
// decode of the TOML it came from.
func decodeRaw(raw map[string]any) (Definition, error) {
	var def Definition
	b, err := json.Marshal(raw)
	if err != nil {
		return def, err
	}
	return def, json.Unmarshal(b, &def)
}

// sub is raw's table at key, nil when there is none.
func sub(raw map[string]any, key string) map[string]any {
	m, _ := raw[key].(map[string]any)
	return m
}

// oldKeys are the keys of the earlier definition shape, each with what replaced
// it in v1 or that v1 removed it: satelle-server's map, verbatim. A finding on
// one names its path and this message; lifecycle and trigger changed type in v1,
// so they are named here when they hold the old type.
var oldKeys = map[string]string{
	"remote_id":          "was removed in v1, with no replacement: a remote is bound to its definition path by connect",
	"wake_key":           "was removed in v1, with no replacement: a runner-backed remote is woken by starting a node",
	"spend_cap_usd":      "was removed in v1, with no replacement: a trigger's params.spend_cap_usd caps a run",
	"lifecycle":          `the earlier lifecycle string is refused: v1 uses the [lifecycle] table, scope = "run" or "job"`,
	"secret":             "[[secret]] was replaced in v1 by [secrets]: list backend keys in names, and a github-app or satelle:<name> value in [secrets.override]",
	"runner":             `[runner] was replaced in v1 by provider = "fly-sprite"`,
	"host.bootstrap":     "was replaced in v1 by [setup] on_create and post_start, each a list of commands",
	"host.name":          "was replaced in v1 by name",
	"host.story_command": "was removed in v1, with no replacement: a story node drives the story it is woken for",
	"host.env":           "was removed in v1, with no replacement",
	"identity.key":       "was replaced in v1 by identity.principal",
	"identity.oidc":      "was replaced in v1 by identity.principal",
	"keepalive":          "was removed in v1, with no replacement",
	"segment":            "was removed in v1, with no replacement",
	"trigger":            `the earlier [[trigger]] array is refused: v1 declares one [trigger] table, on = "story.status:<status>"`,
	"preflight":          "was removed in v1, with no replacement",
	"step":               "was removed in v1, with no replacement",
	"mcp":                "was removed in v1, with no replacement",
}

// oldTables are earlier-shape tables whose keys are named one by one.
var oldTables = map[string]bool{"host": true}

// tomlFields maps the toml keys of struct type t to their field types.
func tomlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		if name, _, _ := strings.Cut(f.Tag.Get("toml"), ","); name != "" && name != "-" {
			out[name] = f.Type
		}
	}
	return out
}

// shapeTable holds the raw table m at path to struct type t: a key t does not
// carry is a finding (an earlier-shape key names its replacement) and so is a
// value of the wrong type. Each such key is dropped from m.
func (v *validator) shapeTable(at string, m map[string]any, t reflect.Type) {
	fields := tomlFields(t)
	for _, k := range sortedKeys(m) {
		key := joinKey(at, k)
		ft, ok := fields[k]
		if !ok {
			v.unknown(key, m[k])
			delete(m, k)
			continue
		}
		if !v.shape(key, m[k], ft) {
			delete(m, k)
		}
	}
}

// unknown reports a key v1 does not carry: an earlier-shape key with what
// replaced it, an array of them once per element, anything else as unknown.
func (v *validator) unknown(key string, val any) {
	if t, ok := val.(map[string]any); ok && oldTables[key] && len(t) > 0 {
		for _, k := range sortedKeys(t) {
			v.unknown(key+"."+k, t[k])
		}
		return
	}
	msg, old := oldKeys[key]
	if !old {
		parent, _, _ := strings.Cut(key, ".")
		if !oldTables[parent] {
			v.fail(key, "unknown key")
			return
		}
		msg = "was removed in v1, with no replacement"
	}
	if tables, ok := val.([]map[string]any); ok && len(tables) > 0 {
		for i := range tables {
			v.fail(fmt.Sprintf("%s[%d]", key, i), msg)
		}
		return
	}
	v.fail(key, msg)
}

// shape holds one raw value at key to type t, and reports whether it fits. An
// interface type is judged by its owner (an override, in checkSecrets).
func (v *validator) shape(key string, raw any, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	mismatch := func(msg string) bool {
		if old, ok := oldKeys[key]; ok {
			msg = old
		}
		v.fail(key, msg)
		return false
	}
	switch t.Kind() {
	case reflect.String:
		if _, ok := raw.(string); !ok {
			return mismatch("must be a string")
		}
	case reflect.Int:
		if _, ok := raw.(int64); !ok {
			return mismatch("must be an integer")
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Struct {
			tables, ok := raw.([]map[string]any)
			if !ok {
				return mismatch("must be an array of tables")
			}
			for i, m := range tables {
				v.shapeTable(fmt.Sprintf("%s[%d]", key, i), m, t.Elem())
			}
			return true
		}
		list, ok := raw.([]any)
		if !ok {
			return mismatch("must be a list")
		}
		fits := true
		for i, x := range list {
			if !v.shape(fmt.Sprintf("%s[%d]", key, i), x, t.Elem()) {
				fits = false
			}
		}
		return fits
	case reflect.Map:
		m, ok := raw.(map[string]any)
		if !ok {
			return mismatch("must be a table")
		}
		for _, k := range sortedKeys(m) {
			if !v.shape(joinKey(key, k), m[k], t.Elem()) {
				delete(m, k)
			}
		}
	case reflect.Struct:
		m, ok := raw.(map[string]any)
		if !ok {
			return mismatch("must be a table")
		}
		v.shapeTable(key, m, t)
	}
	return true
}

// providerNames lists the providers, sorted.
func providerNames() []string {
	out := make([]string, len(Providers))
	for i, p := range Providers {
		out[i] = string(p)
	}
	slices.Sort(out)
	return out
}

// checkProvider holds provider to one of Providers. The server also checks the
// provider's runner type against its catalog; a node has no catalog.
func (v *validator) checkProvider(def Definition) {
	switch {
	case def.Provider == "":
		v.fail("provider", "required: one of "+strings.Join(providerNames(), ", "))
	case !slices.Contains(Providers, def.Provider):
		v.fail("provider", "is not one of "+strings.Join(providerNames(), ", "))
	}
}

// SwapPattern is a swap size: digits then K, M or G.
var SwapPattern = regexp.MustCompile(`^[0-9]+[KMG]$`)

// checkMachine holds [machine] to non-empty strings and swap to a size. raw is
// the [machine] table as written, so a key present but blank is told from one
// left out.
func (v *validator) checkMachine(m Machine, raw map[string]any) {
	for _, f := range []struct{ key, val string }{{"region", m.Region}, {"size", m.Size}, {"image", m.Image}} {
		if _, ok := raw[f.key]; ok && strings.TrimSpace(f.val) == "" {
			v.fail("machine."+f.key, "is empty; omit the key or give a value")
		}
	}
	if _, ok := raw["swap"]; ok && !SwapPattern.MatchString(m.Swap) {
		v.fail("machine.swap", "must be a size: digits then K, M or G, such as 8G")
	}
}

// checkSetup holds each [setup] command to a non-empty string.
func (v *validator) checkSetup(s Setup) {
	for _, f := range []struct {
		key  string
		cmds []string
	}{{"setup.on_create", s.OnCreate}, {"setup.post_start", s.PostStart}} {
		for i, c := range f.cmds {
			if strings.TrimSpace(c) == "" {
				v.fail(fmt.Sprintf("%s[%d]", f.key, i), "is empty")
			}
		}
	}
}

// keyPattern is a well-formed secret key name: satelle-server's
// secrets.ValidKey.
var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

func validKey(k string) bool { return keyPattern.MatchString(k) }

// checkSecrets judges [secrets]: the backend provider (omitted or "satelle", the
// satelle vault; the retired "infisical" is refused naming the replacement), each
// name a well-formed key listed once, and each override a well-formed key not
// also a name, holding "satelle:<name>" or a github-app table.
func (v *validator) checkSecrets(def Definition) {
	s := def.Secrets
	switch s.Provider {
	case "", SecretsProviderSatelle:
	case retiredSecretsProviderInfisical:
		v.fail("secrets.provider", `"`+retiredSecretsProviderInfisical+`" is retired: [secrets] names resolve from the satelle vault; omit secrets.provider or set it to "`+SecretsProviderSatelle+`"`)
	default:
		v.fail("secrets.provider", `is not "`+SecretsProviderSatelle+`", the one secrets backend (the satelle vault)`)
	}
	seen := map[string]int{}
	for i, n := range s.Names {
		at := fmt.Sprintf("secrets.names[%d]", i)
		switch j, dup := seen[n]; {
		case !validKey(n):
			v.fail(at, "is not a well-formed key name (A-Z, 0-9 and _)")
		case dup:
			v.fail(at, fmt.Sprintf("duplicate of secrets.names[%d]", j))
		default:
			seen[n] = i
		}
	}
	for _, k := range slices.Sorted(maps.Keys(s.Override)) {
		at := "secrets.override." + k
		if !validKey(k) {
			v.fail(at, "is not a well-formed key name (A-Z, 0-9 and _)")
			continue
		}
		if _, dup := seen[k]; dup {
			v.fail(at, "is also listed in secrets.names; a key comes from one source")
		}
		v.checkOverride(at, s.Override[k], def)
	}
}

// checkEnvironment holds environment to a vault environment name and requires it
// when the definition reads a value from the vault: a [secrets] name or a
// satelle: override. A github-app override is minted, not held, so it needs none.
// The server also checks the name against the workspace's environments.
func (v *validator) checkEnvironment(def Definition) {
	if def.Environment != "" {
		if !environmentPattern.MatchString(def.Environment) {
			v.fail("environment", "must be lower-case letters, digits or -, starting with a letter, at most 32 characters")
		}
		return
	}
	needs := len(def.Secrets.Names) > 0
	for _, e := range def.SecretEntries() {
		needs = needs || e.Source == SourceSatelle
	}
	if needs {
		v.fail("environment", "required when [secrets] declares a name or a satelle: override: the vault environment its values resolve in")
	}
}

// overrideForms says what an override may hold.
const overrideForms = `must be "satelle:<name>" (a team value satelle holds, granted to the remote) or { provider = "github-app", permissions = { <name> = "read" or "write" } }`

// checkOverride judges one [secrets.override] value.
func (v *validator) checkOverride(at string, val OverrideValue, def Definition) {
	switch t := val.(type) {
	case string:
		name, ok := strings.CutPrefix(t, overrideSatellePrefix)
		if !ok {
			v.fail(at, overrideForms)
		} else if !validKey(name) {
			v.fail(at, "names a satelle value that is not a well-formed key name (A-Z, 0-9 and _)")
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if k != "provider" && k != "permissions" {
				v.fail(at+"."+k, "unknown key: an override table holds provider and permissions")
			}
		}
		if p, _ := t["provider"].(string); p != OverrideGitHubApp {
			v.fail(at+".provider", `must be "`+OverrideGitHubApp+`"`)
			return
		}
		if !hasRepoOn(def.Repos, DefaultGitHubHost) {
			v.fail(at, fmt.Sprintf("a github-app token needs a [[repo]] on %s: the token is limited to the declared repos on its host", DefaultGitHubHost))
		}
		v.checkPermissions(at+".permissions", t["permissions"])
	default:
		v.fail(at, overrideForms)
	}
}

// hasRepoOn reports whether any repo is an owner/name repository on host. The
// server normalises the host through its gitcred package; github.com is the one
// host a github-app override mints for, compared case-insensitively here.
func hasRepoOn(repos []Repo, host string) bool {
	for _, r := range repos {
		if h, _, ok := RepoSlug(r.URL); ok && strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// githubPermissionPattern is a GitHub App permission name.
var githubPermissionPattern = regexp.MustCompile(`^[a-z_]+$`)

// checkPermissions judges a github-app override's permissions table: present and
// non-empty, each name a GitHub App permission name and each level read or write.
func (v *validator) checkPermissions(at string, raw any) {
	perms, _ := raw.(map[string]any)
	if len(perms) == 0 {
		v.fail(at, `required: a table of GitHub App permission name to "read" or "write", such as { contents = "read", pull_requests = "write" }: the token asks for exactly those`)
		return
	}
	for _, name := range sortedKeys(perms) {
		if !githubPermissionPattern.MatchString(name) {
			v.fail(at, "has a name that is not a GitHub App permission name (lowercase letters and _, such as pull_requests)")
			continue
		}
		if lvl, _ := perms[name].(string); lvl != "read" && lvl != "write" {
			v.fail(at+"."+name, `must be "read" or "write"`)
		}
	}
}

// checkLifecycle holds [lifecycle] to a known scope, a positive idle_destroy
// duration, a positive max_nodes, and a non-negative keep_warm declared only for
// a drive kind that keeps nodes warm. keep_warm above max_nodes is the client's
// own rule: the server keeps no more warm nodes than max_nodes allows, so a
// definition that asks for more is refused rather than silently capped.
func (v *validator) checkLifecycle(l Lifecycle, kind DriveKind) {
	if l.KeepWarm != nil {
		switch {
		case *l.KeepWarm < 0:
			v.fail("lifecycle.keep_warm", "must be a non-negative integer: how many idle nodes to keep warm")
		case !kind.KeepsWarm():
			v.fail("lifecycle.keep_warm", `is accepted only when [drive] kind is "task"`)
		case l.MaxNodes != nil && *l.MaxNodes >= 1 && *l.KeepWarm > *l.MaxNodes:
			v.fail("lifecycle.keep_warm", "must not exceed lifecycle.max_nodes: a warm node is a live node")
		}
	}
	if l.Scope != "" && !slices.Contains(LifecycleScopes, l.Scope) {
		v.fail("lifecycle.scope", "is not "+enumList(LifecycleScopes))
	}
	if l.IdleDestroy != "" {
		if d, err := time.ParseDuration(l.IdleDestroy); err != nil || d <= 0 {
			v.fail("lifecycle.idle_destroy", "is not a positive duration, such as 30m or 2h")
		}
	}
	if l.MaxNodes != nil && *l.MaxNodes < 1 {
		v.fail("lifecycle.max_nodes", "must be a positive integer: the most live nodes of this definition a remote may have")
	}
}

// maxStatusLen bounds the status a trigger names.
const maxStatusLen = 100

// checkTrigger holds [trigger] on to "story.status:<status>"; any other value is
// refused.
func (v *validator) checkTrigger(t *Trigger) {
	if t == nil {
		return
	}
	on := t.On
	status, isStatus := strings.CutPrefix(on, TriggerOnStatusPrefix)
	switch {
	case strings.TrimSpace(on) == "":
		v.fail("trigger.on", "required: story.status:<status>, the event that starts a node")
	case !isStatus:
		v.fail("trigger.on", "must be story.status:<status>")
	case status == "" || len(status) > maxStatusLen || strings.ContainsAny(status, " \t\r\n"):
		v.fail("trigger.on", fmt.Sprintf("must name a status of 1 to %d characters with no whitespace after story.status:", maxStatusLen))
	}
}

// checkDrive holds [drive] to a known kind and a task for the task kind only.
func (v *validator) checkDrive(d Drive) {
	switch {
	case d.Kind == "":
		v.fail("drive.kind", "required: one of "+enumList(DriveKinds))
		return
	case !slices.Contains(DriveKinds, d.Kind):
		v.fail("drive.kind", "is not one of "+enumList(DriveKinds))
		return
	}
	if d.Kind != DriveTask {
		if d.Task != "" {
			v.fail("drive.task", "belongs to the task kind only")
		}
		return
	}
	switch {
	case strings.TrimSpace(d.Task) == "":
		v.fail("drive.task", "required: the name of the satelle task each wake runs")
	case !namePattern.MatchString(d.Task):
		v.fail("drive.task", "must be lower-case letters, digits, - or _, starting with a letter or digit")
	}
}

// enumList reads "a, b, c" off a string-kinded enum.
func enumList[T ~string](vals []T) string {
	names := make([]string, len(vals))
	for i, k := range vals {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// parseMessage describes a decode failure without quoting the input: the TOML
// parser's own text can carry the offending line.
func parseMessage(err error) string {
	var pe toml.ParseError
	if errors.As(err, &pe) {
		return fmt.Sprintf("not valid TOML at line %d, column %d", pe.Position.Line, pe.Position.Col)
	}
	return "a value has a type the definition does not accept"
}

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	versionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	varRefPattern  = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
	// hostPattern is a hostname or address, optionally with a leading "*." wildcard.
	hostPattern = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
	// repoNamePattern is a directory name a clone can safely have.
	repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// environmentPattern is a vault environment name, such as dev, stg or prd.
	environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// httpsURL parses raw as an https URL with a host; ok is false for anything else.
func httpsURL(raw string) (u *url.URL, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	return u, err == nil && u.Scheme == "https" && u.Hostname() != ""
}

// checkRepos holds each repo to an https URL with a host, no credential in it, a
// usable clone directory name, and a ref git cannot read as an option.
func (v *validator) checkRepos(def Definition) {
	seen := map[string]int{}
	for i, r := range def.Repos {
		urlKey := fmt.Sprintf("repo[%d].url", i)
		refKey := fmt.Sprintf("repo[%d].ref", i)
		switch u, ok := httpsURL(r.URL); {
		case strings.TrimSpace(r.URL) == "":
			v.fail(urlKey, "required: the https URL of the repository")
		case !ok:
			v.fail(urlKey, "must be an https URL with a host")
		case u.User != nil:
			v.fail(urlKey, "must not carry a credential; the node reads a repository without one")
		case !hostPattern.MatchString(u.Hostname()):
			v.fail(urlKey, "host is not a well-formed hostname")
		default:
			name := strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".git")
			if !repoNamePattern.MatchString(name) || name == ".." {
				v.fail(urlKey, "has no usable repository name in its path")
			} else if j, dup := seen[name]; dup {
				v.fail(urlKey, fmt.Sprintf("clones into the same directory as repo[%d]", j))
			} else {
				seen[name] = i
			}
		}
		switch ref := r.Ref; {
		case strings.TrimSpace(ref) == "":
			v.fail(refKey, "required: the branch, tag or commit to check out")
		case strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\r\n"):
			v.fail(refKey, "is not a plain ref (no leading - and no whitespace)")
		}
	}
}

// checkCredentials refuses a credential-shaped key holding a literal value at any
// depth of remote.toml as written: a ${VAR} reference is accepted, anything else
// is a value. [secrets] is exempt: it holds key names and sources, judged by
// checkSecrets.
func (v *validator) checkCredentials(raw map[string]any) {
	var walk func(path, key string, val any)
	walk = func(path, key string, val any) {
		switch t := val.(type) {
		case map[string]any:
			for _, k := range sortedKeys(t) {
				walk(joinKey(path, k), k, t[k])
			}
		case []map[string]any:
			for i, item := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), key, item)
			}
		case []any:
			for i, item := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), key, item)
			}
		default:
			// An earlier-shape key (wake_key, identity.key) held a name, not a
			// value, and is refused by its own finding.
			if _, old := oldKeys[path]; !old && secretishKey(key) && carriesValue(val) {
				v.fail(path, "credential-shaped key holds a literal value; hold a ${VAR} reference, or declare the secret in [secrets]")
			}
		}
	}
	for _, k := range sortedKeys(raw) {
		if k == "secrets" {
			continue
		}
		walk(k, k, raw[k])
	}
}

// secretishKey reports whether a key names a credential-shaped value.
func secretishKey(k string) bool {
	l := strings.ToLower(k)
	for _, marker := range []string{"token", "secret", "password", "apikey", "api_key", "api-key"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return l == "key" || strings.HasSuffix(l, "_key") || strings.HasSuffix(l, "-key")
}

// carriesValue reports whether v is a literal: anything but a blank or a pure
// ${VAR} reference.
func carriesValue(v any) bool {
	s, ok := v.(string)
	if !ok {
		return true
	}
	return strings.TrimSpace(s) != "" && !varRefPattern.MatchString(strings.TrimSpace(s))
}

func joinKey(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
