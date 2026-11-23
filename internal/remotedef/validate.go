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

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

type Finding struct {
	Severity string `json:"severity"`
	Key      string `json:"key"`
	Message  string `json:"message"`
}

func Validate(body []byte) []Finding {
	_, findings := check(body)
	return findings
}

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
	v.checkCredentials(raw)
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
	v.checkConnect(def, sub(raw, "connect"))
	v.checkMachine(def.Machine, sub(raw, "machine"))
	v.checkSetup(def.Setup)
	v.checkSecrets(def)
	v.checkRepos(def)
	for i, h := range def.Network.Allow {
		if !hostPattern.MatchString(h) {
			v.fail(fmt.Sprintf("network.allow[%d]", i), "is not a hostname or *.domain")
		}
	}
	v.checkLifecycle(def.Lifecycle, def.Drive.Kind)
	v.checkDrive(def.Drive, def.Provider)
	return def, v.out
}

func decodeRaw(raw map[string]any) (Definition, error) {
	var def Definition
	b, err := json.Marshal(raw)
	if err != nil {
		return def, err
	}
	return def, json.Unmarshal(b, &def)
}

func sub(raw map[string]any, key string) map[string]any {
	m, _ := raw[key].(map[string]any)
	return m
}

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

func (v *validator) unknown(key string, _ any) { v.fail(key, "unknown key") }

func (v *validator) shape(key string, raw any, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	mismatch := func(msg string) bool {
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

func providerNames() []string {
	out := make([]string, len(Providers))
	for i, p := range Providers {
		out[i] = string(p)
	}
	slices.Sort(out)
	return out
}

func (v *validator) checkProvider(def Definition) {
	switch {
	case def.Provider == "":
		v.fail("provider", "required: one of "+strings.Join(providerNames(), ", "))
	case !slices.Contains(Providers, def.Provider):
		v.fail("provider", "is not one of "+strings.Join(providerNames(), ", "))
	}
}

var placeholderPattern = regexp.MustCompile(`\{[^{}]*\}`)

func (v *validator) checkConnect(def Definition, raw map[string]any) {
	if def.Provider != ProviderCommand {
		if len(raw) > 0 {
			v.fail("connect", `belongs to provider "`+string(ProviderCommand)+`" only`)
		}
		return
	}
	c := def.Connect
	node := []string{PlaceholderNode}
	copyIn := []string{PlaceholderNode, PlaceholderSrc, PlaceholderDst}
	for _, f := range []struct {
		key      string
		argv     []string
		required string
		allowed  []string
	}{
		{"check", c.Check, "", node},
		{"start", c.Start, "the argv list that starts the node {node}", node},
		{"network", c.Network, "[connect] network, the argv list that applies [network] allow to the node {node}: the list on its stdin, one host per line (empty for deny-all), and the hosts the node now allows printed back, one per line", node},
		{"copy_in", c.CopyIn, "the argv list that copies the host path {src} to the node path {dst}", copyIn},
		{"exec", c.Exec, "the argv list that runs a command on the node {node}; the command's argv follows it", node},
		{"stop", c.Stop, "the argv list that stops the node {node} and removes it", node},
	} {
		at := "connect." + f.key
		if len(f.argv) == 0 {
			if f.required != "" {
				v.fail(at, "required: "+f.required)
			}
			continue
		}
		if strings.TrimSpace(f.argv[0]) == "" {
			v.fail(at+"[0]", "is empty")
		}
		for i, a := range f.argv {
			for _, p := range placeholderPattern.FindAllString(a, -1) {
				if !slices.Contains(f.allowed, p) {
					v.fail(fmt.Sprintf("%s[%d]", at, i), "names "+p+", which satelle does not substitute")
				}
			}
		}
	}
	if len(c.CopyIn) > 0 {
		joined := strings.Join(c.CopyIn, "\x00")
		for _, p := range []string{PlaceholderSrc, PlaceholderDst} {
			if !strings.Contains(joined, p) {
				v.fail("connect.copy_in", "names no "+p+": copy_in copies {src} on this machine to {dst} on the node")
			}
		}
	}
}

var SwapPattern = regexp.MustCompile(`^[0-9]+[KMG]$`)

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

var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

func validKey(k string) bool { return keyPattern.MatchString(k) }

func (v *validator) checkSecrets(def Definition) {
	s := def.Secrets
	switch s.Provider {
	case "", SecretsProviderSatelle:
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

const overrideForms = `must be "satelle:<name>" (a team value satelle holds, granted to the remote) or { provider = "github-app", permissions = { <name> = "read" or "write" } }`

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

func hasRepoOn(repos []Repo, host string) bool {
	for _, r := range repos {
		if u, ok := httpsURL(r.URL); ok && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}

var githubPermissionPattern = regexp.MustCompile(`^[a-z_]+$`)

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

func (v *validator) checkDrive(d Drive, p Provider) {
	if d.Checkout {
		switch {
		case d.Kind != DriveTask:
			v.fail("drive.checkout", "belongs to the task kind only")
		case p != ProviderDockerLocal:
			v.fail("drive.checkout", "is given only by a docker-local node: the fire checks the firing tree's HEAD out in the node")
		}
	}
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

func enumList[T ~string](vals []T) string {
	names := make([]string, len(vals))
	for i, k := range vals {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

func parseMessage(err error) string {
	var pe toml.ParseError
	if errors.As(err, &pe) {
		return fmt.Sprintf("not valid TOML at line %d, column %d", pe.Position.Line, pe.Position.Col)
	}
	return "a value has a type the definition does not accept"
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	versionPattern  = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	varRefPattern   = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
	hostPattern     = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
	repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func httpsURL(raw string) (u *url.URL, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	return u, err == nil && u.Scheme == "https" && u.Hostname() != ""
}

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
			if secretishKey(key) && carriesValue(val) {
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

func secretishKey(k string) bool {
	l := strings.ToLower(k)
	for _, marker := range []string{"token", "secret", "password", "apikey", "api_key", "api-key"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return l == "key" || strings.HasSuffix(l, "_key") || strings.HasSuffix(l, "-key")
}

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
