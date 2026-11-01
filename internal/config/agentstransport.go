// What may leave this machine in a shared agents layer (sty_89b9df20). Every push of the agents layer — `satelle sync config push`,
// `satelle publish push`, `satelle sync bindings push` — sends the file's bytes
// exactly as authored, so the file itself must not carry anything that belongs to
// one machine. CheckAgents is the ONE rule set for that: the pushes refuse on it
// and doctor/init warn on it (agentvalidate.sharedMachineRefs). The content it
// names has a home — agents.local.toml, which never leaves the machine — so a
// refusal points there instead of the transport rewriting the file behind the
// operator's back.
//
// The rules, each checked against the decoded layer:
//
//   - settings env: a literal value under a settings `env` sub-table;
//   - settings secret: a literal value under a secret-shaped settings key;
//   - settings path: an absolute-path string anywhere in settings, including
//     nested tables and lists;
//   - command path: an absolute command token;
//   - profile: profile= (a machine-wide catalog name) or the retired harness=;
//   - global roles: [defaults] use_global_roles = true;
//   - binding env: a literal value in a binding's own env table.
//
// A pure `${VAR}` reference is a variable NAME, not a value: it travels, and it
// is what lets the receiving machine's [vars] fail-fast fire when the value is
// missing. A blank value means "declared, unsatisfied" and travels too.
package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// varRefPattern matches a value that is exactly one ${NAME} reference.
var varRefPattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// IsVarRef reports whether v is a pure ${NAME} reference — a variable name,
// not a value, and therefore safe to transport.
func IsVarRef(v string) bool {
	return varRefPattern.MatchString(strings.TrimSpace(v))
}

// MoveToLocalAgents is the pointer every refusal ends with: the one place
// machine-specific agent configuration belongs.
const MoveToLocalAgents = "move it into " + DefaultDataDir + "/" + LocalAgentsRel + ", which never leaves this machine"

// The rule names a TransportRefusal carries.
const (
	RuleSettingsEnv    = "settings-env"
	RuleSettingsSecret = "settings-secret"
	RuleSettingsPath   = "settings-path"
	RuleCommandPath    = "command-path"
	RuleProfile        = "profile"
	RuleHarness        = "harness"
	RuleGlobalRoles    = "global-roles"
	RuleBindingEnv     = "binding-env"
)

// TransportRefusal is one piece of machine-specific content found in a shared
// agents layer: the file, the section ([executor], [reviewer], a binding name or
// [defaults]) and the field it sits in.
type TransportRefusal struct {
	Rule    string
	File    string
	Section string
	Field   string
	Reason  string
}

// Error names the file, section and field and points at agents.local.toml.
func (r TransportRefusal) Error() string {
	return fmt.Sprintf("%s [%s] %s: %s — %s", r.File, r.Section, r.Field, r.Reason, MoveToLocalAgents)
}

// CarriesValue reports whether v is a literal that must not travel: anything but
// a blank or a pure ${VAR} reference.
func CarriesValue(v any) bool {
	s, ok := v.(string)
	if !ok {
		return true
	}
	return strings.TrimSpace(s) != "" && !IsVarRef(s)
}

// CheckAgents reports every machine-specific value in a shared agents layer, in a
// fixed order: [defaults], [executor], [reviewer], then named bindings sorted.
func CheckAgents(a AgentsConfig) []TransportRefusal {
	var out []TransportRefusal
	if a.Defaults.UseGlobalRoles {
		out = append(out, TransportRefusal{Rule: RuleGlobalRoles, File: AgentsConfigName, Section: "defaults", Field: "use_global_roles",
			Reason: "use_global_roles = true reaches into the ~/.satelle [roles] catalog of whichever machine reads the file"})
	}
	out = append(out, checkBinding("executor", a.Executor)...)
	out = append(out, checkBinding("reviewer", a.Reviewer)...)
	for _, n := range sortedKeys(a.Agents) {
		out = append(out, checkBinding(n, a.Agents[n])...)
	}
	return out
}

// CheckAgentsTransport decodes an agents-layer body and checks it. A body that
// does not decode is an ERROR — it is never judged clean.
func CheckAgentsTransport(body []byte) ([]TransportRefusal, error) {
	ac, err := decodeAgents(string(body), false)
	if err != nil {
		return nil, fmt.Errorf("check agents layer: %w", err)
	}
	return CheckAgents(ac), nil
}

// RefusalsError folds refusals into one error naming each, or nil when there are
// none. verb names the command that refused.
func RefusalsError(verb string, refusals []TransportRefusal) error {
	if len(refusals) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s refused: %s carries machine-specific content, which would not work on another machine:", verb, AgentsConfigName)
	for _, r := range refusals {
		b.WriteString("\n  " + r.Error())
	}
	return fmt.Errorf("%s", b.String())
}

func checkBinding(section string, b AgentBinding) []TransportRefusal {
	var out []TransportRefusal
	add := func(rule, field, reason string) {
		out = append(out, TransportRefusal{Rule: rule, File: AgentsConfigName, Section: section, Field: field, Reason: reason})
	}
	if strings.TrimSpace(b.Profile) != "" {
		add(RuleProfile, "profile", fmt.Sprintf("profile=%q resolves only against the ~/.satelle catalog of the machine that has it", b.Profile))
	}
	if strings.TrimSpace(b.Harness) != "" {
		add(RuleHarness, "harness", fmt.Sprintf("harness=%q is retired and names this machine's harness", b.Harness))
	}
	for _, tok := range strings.Fields(b.CommandTemplate()) {
		if filepath.IsAbs(tok) {
			add(RuleCommandPath, "command", fmt.Sprintf("the absolute path %q belongs to one machine; name the executable bare", tok))
		}
	}
	for _, k := range sortedKeys(b.Env) {
		if CarriesValue(b.Env[k]) {
			add(RuleBindingEnv, "env."+k, "a literal env value belongs to one machine; only a pure ${VAR} reference travels")
		}
	}
	for _, r := range checkSettings(b.Settings, "settings", false) {
		r.File, r.Section = AgentsConfigName, section
		out = append(out, r)
	}
	return out
}

// checkSettings walks the settings tree. Under an `env` sub-table every literal
// is refused; elsewhere a literal under a secret-shaped key is; absolute-path
// strings are refused anywhere else, including list entries such as
// permissions.additionalDirectories. Nested tables and lists recurse.
func checkSettings(m map[string]any, path string, underEnv bool) []TransportRefusal {
	var out []TransportRefusal
	for _, k := range sortedKeys(m) {
		field := path + "." + k
		switch child := m[k].(type) {
		case map[string]any:
			out = append(out, checkSettings(child, field, underEnv || strings.EqualFold(k, "env"))...)
		case []any:
			out = append(out, checkSettingsList(child, field)...)
		case []map[string]any:
			// An array of tables decodes to this, not []any.
			out = append(out, checkSettingsList(tablesToList(child), field)...)
		default:
			switch {
			case underEnv:
				if CarriesValue(child) {
					out = append(out, TransportRefusal{Rule: RuleSettingsEnv, Field: field, Reason: "a literal env value belongs to one machine; only a pure ${VAR} reference travels"})
				}
			case SecretishKey(k):
				if CarriesValue(child) {
					out = append(out, TransportRefusal{Rule: RuleSettingsSecret, Field: field, Reason: "a literal secret-shaped value must not be shared; only a pure ${VAR} reference travels"})
				}
			default:
				out = append(out, checkSettingsScalar(child, field)...)
			}
		}
	}
	return out
}

func tablesToList(tables []map[string]any) []any {
	l := make([]any, len(tables))
	for i, t := range tables {
		l[i] = t
	}
	return l
}

func checkSettingsList(l []any, path string) []TransportRefusal {
	var out []TransportRefusal
	for i, v := range l {
		field := fmt.Sprintf("%s[%d]", path, i)
		switch child := v.(type) {
		case map[string]any:
			out = append(out, checkSettings(child, field, false)...)
		case []any:
			out = append(out, checkSettingsList(child, field)...)
		case []map[string]any:
			out = append(out, checkSettingsList(tablesToList(child), field)...)
		default:
			out = append(out, checkSettingsScalar(child, field)...)
		}
	}
	return out
}

func checkSettingsScalar(v any, field string) []TransportRefusal {
	if s, ok := v.(string); ok && filepath.IsAbs(s) {
		return []TransportRefusal{{Rule: RuleSettingsPath, Field: field, Reason: fmt.Sprintf("the absolute path %q belongs to one machine", s)}}
	}
	return nil
}

// SecretishKey reports whether a settings key names a credential-shaped value.
func SecretishKey(k string) bool {
	l := strings.ToLower(k)
	for _, marker := range []string{"token", "secret", "password", "apikey", "api_key", "api-key"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return l == "key" || strings.HasSuffix(l, "_key") || strings.HasSuffix(l, "-key")
}

// PruneUnsatisfied drops what a workspace author left blank — env keys with ""
// values and blank secret/settings.env leaves — so a layer contributes only what
// it actually carries. Used on the workspace tier before it is merged: a blank
// there is "declared, unsatisfied", and must never shadow the machine's own
// environment or settings.local.json (sty_01949949 AC3).
func PruneUnsatisfied(b AgentBinding) AgentBinding {
	out := b
	if len(b.Env) > 0 {
		env := make(map[string]string, len(b.Env))
		for k, v := range b.Env {
			if strings.TrimSpace(v) != "" {
				env[k] = v
			}
		}
		out.Env = env
		if len(env) == 0 {
			out.Env = nil
		}
	}
	if len(b.Settings) > 0 {
		out.Settings = pruneBlankSettings(b.Settings)
		if len(out.Settings) == 0 {
			out.Settings = nil
		}
	}
	return out
}

func pruneBlankSettings(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch child := v.(type) {
		case map[string]any:
			if p := pruneBlankSettings(child); len(p) > 0 {
				out[k] = p
			}
		case string:
			if strings.TrimSpace(child) != "" {
				out[k] = child
			}
		default:
			out[k] = v
		}
	}
	return out
}

// modelOrderTable renders [model_order]: a single-name rank as a string, a
// multi-name rank as an array, so the bytes decode back to the same ranks.
func modelOrderTable(mo map[string][]ModelRank) map[string]any {
	t := map[string]any{}
	for exe, ranks := range mo {
		if len(ranks) == 0 {
			continue
		}
		list := make([]any, 0, len(ranks))
		for _, r := range ranks {
			if len(r) == 1 {
				list = append(list, r[0])
			} else {
				list = append(list, []string(r))
			}
		}
		t[exe] = list
	}
	return t
}

// EncodeAgents marshals an agents layer to TOML in the flat form LoadAgents
// reads: [defaults] when set, [executor], [reviewer], then every named binding
// as a top-level table (never the retired nested [agents.<name>]). Only
// non-empty fields are written, so a round trip through decodeAgents is
// lossless for everything the loader keeps. Tables and keys are emitted sorted,
// so the bytes are deterministic.
func EncodeAgents(a AgentsConfig) ([]byte, error) {
	tables := map[string]map[string]any{}
	if d := defaultsTable(a.Defaults); len(d) > 0 {
		tables["defaults"] = d
	}
	if t := modelOrderTable(a.ModelOrder); len(t) > 0 {
		tables["model_order"] = t
	}
	if t := bindingTable(a.Executor); len(t) > 0 {
		tables["executor"] = t
	}
	if t := bindingTable(a.Reviewer); len(t) > 0 {
		tables["reviewer"] = t
	}
	for name, b := range a.Agents {
		switch name {
		case "defaults", "models", "model_order", "executor", "reviewer", "agents":
			continue // cannot be a named binding in the flat form
		}
		t := bindingTable(b)
		if len(t) == 0 {
			t = map[string]any{} // an empty named table still declares the name
		}
		tables[name] = t
	}
	// One Encode over the whole document, so nested env/settings tables are
	// written as [<name>.env] under their binding rather than as stray
	// top-level tables. The encoder sorts keys, so the bytes are deterministic.
	doc := make(map[string]any, len(tables))
	var empty []string
	for n, t := range tables {
		if len(t) == 0 {
			empty = append(empty, n) // the encoder elides an empty table; declare it by hand
			continue
		}
		doc[n] = t
	}
	var buf bytes.Buffer
	if len(doc) > 0 {
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			return nil, fmt.Errorf("encode agents layer: %w", err)
		}
	}
	sort.Strings(empty)
	for _, n := range empty {
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n\n")) {
			buf.WriteString("\n")
		}
		fmt.Fprintf(&buf, "[%s]\n", n)
	}
	return buf.Bytes(), nil
}

func defaultsTable(d AgentsDefaults) map[string]any {
	out := map[string]any{}
	if s := strings.TrimSpace(d.Secondary); s != "" {
		out["secondary"] = s
	}
	if d.UseGlobalRoles {
		out["use_global_roles"] = true
	}
	return out
}

func bindingTable(b AgentBinding) map[string]any {
	out := map[string]any{}
	set := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	set("interface", b.Interface)
	set("command", b.Command)
	set("tools", b.Tools)
	set("model", b.Model)
	set("role", b.Role)
	set("principles", b.Principles)
	set("timeout", b.Timeout)
	set("idle_timeout", b.IdleTimeout)
	set("busy_timeout", b.BusyTimeout)
	set("effort", b.Effort)
	set("secondary", b.Secondary)
	set("isolation", b.Isolation)
	set("auth", b.Auth)
	set("collect_doc", b.CollectDoc)
	set("placement", b.Placement)
	set("remote", b.Remote)
	set("remote_path", b.RemotePath)
	set("profile", b.Profile)
	if b.InjectPrinciples != nil {
		out["inject_principles"] = *b.InjectPrinciples
	}
	if len(b.Env) > 0 {
		env := make(map[string]string, len(b.Env))
		for k, v := range b.Env {
			env[k] = v
		}
		out["env"] = env
	}
	if len(b.Settings) > 0 {
		out["settings"] = b.Settings
	}
	return out
}
