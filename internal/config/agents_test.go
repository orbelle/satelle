package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// TestTimeoutDuration pins the per-binding dispatch bound resolution (sty_446c38b7):
// a set value parses, an empty value inherits the caller's default, and a
// malformed or non-positive value is an error.
func TestTimeoutDuration(t *testing.T) {
	def := 20 * time.Minute
	cases := []struct {
		name    string
		timeout string
		want    time.Duration
		wantErr bool
	}{
		{"set value parses", "45m", 45 * time.Minute, false},
		{"empty inherits the default", "", def, false},
		{"malformed is an error", "notaduration", 0, true},
		{"non-positive is an error", "0s", 0, true},
	}
	for _, c := range cases {
		got, err := AgentBinding{Timeout: c.timeout}.TimeoutDuration(def)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want an error, got %v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: TimeoutDuration = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLoadAgentsFailsFastOnBadTimeout pins AC1: a malformed [<section>] timeout is
// rejected at LOAD, naming the offending section — not deferred to dispatch time.
func TestLoadAgentsFailsFastOnBadTimeout(t *testing.T) {
	dir := t.TempDir()
	body := "[worker]\nharness = \"claude {system}\"\ntimeout = \"nope\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAgents(dir)
	if err == nil {
		t.Fatal("LoadAgents must reject a malformed binding timeout")
	}
	if !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should name the [worker] timeout, got: %v", err)
	}

	// A well-formed timeout loads and is readable on the binding.
	ok := "[worker]\nharness = \"claude {system}\"\ntimeout = \"45m\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(ok), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatalf("a valid timeout should load: %v", err)
	}
	if b := ac.Agents["worker"]; b.Timeout != "45m" {
		t.Errorf("worker binding timeout = %q, want 45m", b.Timeout)
	}
}

func TestLoadAgentsDefault(t *testing.T) {
	ac, err := LoadAgents(t.TempDir()) // no actors.toml present
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if got := ac.ReviewerBinding(); got.Tools != DefaultReviewerTools || got.Command != DefaultReviewerCommand {
		t.Errorf("reviewer default = %+v, want tools=%q harness=%q", got, DefaultReviewerTools, DefaultReviewerCommand)
	}
	if got := ac.ExecutorBinding(); got.Command != DefaultExecutorCommand {
		t.Errorf("executor default harness = %q, want %q", got.Command, DefaultExecutorCommand)
	}
}

// TestLoadAgentsOnlyAgentsToml proves the loader reads agents.toml and that the
// legacy actors.toml is NO LONGER loaded (sty_7db2ed7d): a repo carrying only the
// retired filename resolves to defaults (the zero config), not its bindings.
func TestLoadAgentsOnlyAgentsToml(t *testing.T) {
	// Canonical agents.toml: loads.
	canon := t.TempDir()
	if err := os.WriteFile(filepath.Join(canon, AgentsConfigName), []byte("[reviewer]\nmodel = \"canon\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ac, err := LoadAgents(canon); err != nil || ac.Reviewer.Model != "canon" {
		t.Fatalf("canonical agents.toml: ac=%+v err=%v", ac, err)
	}

	// Legacy actors.toml only: NOT loaded — resolves to the zero config.
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, ActorsConfigName), []byte("[reviewer]\nmodel = \"legacy\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ac, err := LoadAgents(legacy); err != nil || ac.Reviewer.Model != "" {
		t.Fatalf("legacy actors.toml must not load: ac=%+v err=%v", ac, err)
	}
}

// TestNamedBinding proves a named agent declared under flat [name] resolves as
// an isolated binding, and that an undeclared name reports ok=false so the caller
// falls back to the in-loop executor (sty_b2222b8a). Nested [agents.name] is no
// longer a live dual-read (breaking surface).
func TestNamedBinding(t *testing.T) {
	dir := t.TempDir()
	body := "[commit-agent]\ncommand = \"claude -p --allowedTools {tools}\"\ntools = \"Read,Bash(git:*)\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	b, ok := ac.NamedBinding("commit-agent")
	if !ok || b.Tools != "Read,Bash(git:*)" || b.CommandTemplate() != "claude -p --allowedTools {tools}" {
		t.Errorf("commit-agent binding = %+v ok=%v, want the declared command+tools", b, ok)
	}
	if _, ok := ac.NamedBinding("nope"); ok {
		t.Error("an undeclared named agent must report ok=false (fall back to in-loop)")
	}
	// Nested form is ignored (not dual-read).
	nestedDir := t.TempDir()
	nested := "[agents.commit-agent]\ncommand = \"claude -p\"\ntools = \"Read\"\n"
	if err := os.WriteFile(filepath.Join(nestedDir, AgentsConfigName), []byte(nested), 0o644); err != nil {
		t.Fatal(err)
	}
	nac, err := LoadAgents(nestedDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nac.NamedBinding("commit-agent"); ok {
		t.Error("nested [agents.name] must not resolve without migrate")
	}
}

// TestFlatNamedAgent proves a named agent declared in the FLAT top-level form
// [<name>] resolves as a named isolated agent, while [executor]/[reviewer] in the
// same file remain the built-in roles (not named agents) — sty_6e0ba71c.
func TestFlatNamedAgent(t *testing.T) {
	dir := t.TempDir()
	body := "[reviewer]\nmodel = \"sonnet\"\n" +
		"[commit-agent]\ncommand = \"claude -p --allowedTools {tools}\"\ntools = \"Read,Bash(git:*)\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	// Flat [commit-agent] resolves as a named agent.
	b, ok := ac.NamedBinding("commit-agent")
	if !ok || b.Tools != "Read,Bash(git:*)" || b.CommandTemplate() != "claude -p --allowedTools {tools}" {
		t.Errorf("flat [commit-agent] = %+v ok=%v, want the declared command+tools", b, ok)
	}
	// [reviewer] stays a built-in ROLE, not a named agent.
	if ac.Reviewer.Model != "sonnet" {
		t.Errorf("reviewer role model = %q, want sonnet", ac.Reviewer.Model)
	}
	if _, ok := ac.NamedBinding("reviewer"); ok {
		t.Error("[reviewer] is a built-in role, must NOT be a named agent")
	}
	if _, ok := ac.NamedBinding("executor"); ok {
		t.Error("[executor] is a built-in role, must NOT be a named agent")
	}
}

func TestLoadAgentsOverride(t *testing.T) {
	dir := t.TempDir()
	body := "[reviewer]\ntools = \"Read\"\nharness = \"other-harness\"\n[executor]\nharness = \"claude -p\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if got := ac.ReviewerBinding(); got.Tools != "Read" || got.Harness != "other-harness" {
		t.Errorf("reviewer override = %+v, want tools=Read harness=other-harness", got)
	}
	if got := ac.ExecutorBinding(); got.Harness != "claude -p" {
		t.Errorf("executor override harness = %q, want claude -p", got.Harness)
	}
}

// TestCommandHarnessAlias pins the harness→command rename with back-compat
// (sty_17cae74b): the field is now `command`; a pre-rename file authored with the
// deprecated `harness` key still resolves via CommandTemplate(), and `command`
// wins when a (transitional) file sets both. Drives the on-disk→resolved path
// through LoadAgents + ReviewerBinding so it proves the whole seam, not just the
// struct method — this is what keeps existing satelle/satelle-server agents.toml
// resolving their reviewer across the upgrade.
func TestCommandHarnessAlias(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"command only", "[reviewer]\ncommand = \"claude -p {system}\"\n", "claude -p {system}"},
		// harness= is no longer a runtime fallback (breaking); raw Command stays empty.
		{"harness alone leaves Command empty", "[reviewer]\nharness = \"grok -p {payload}\"\n", ""},
		{"command wins over harness field", "[reviewer]\ncommand = \"claude win\"\nharness = \"grok lose\"\n", "claude win"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			ac, err := LoadAgents(dir)
			if err != nil {
				t.Fatalf("LoadAgents: %v", err)
			}
			// Read raw binding (not ReviewerBinding, which fills defaults).
			got := ac.Reviewer.CommandTemplate()
			if got != c.want {
				t.Errorf("CommandTemplate() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLoadAgentsSettingsTable pins AC1/AC2: a binding's `settings` table decodes
// as a generic map[string]any (env, model, and a nested permissions.allow array)
// with no dedicated schema struct — LoadAgents's existing PrimitiveDecode-into-map
// path already handles a nested table for free. A binding with no settings table
// yields a nil map (not an empty-but-present one), so it materialises to no
// {settings} value at all.
func TestLoadAgentsSettingsTable(t *testing.T) {
	dir := t.TempDir()
	body := "[worker]\n" +
		"harness = \"claude {system} --settings {settings}\"\n" +
		"[worker.settings]\n" +
		"model = \"glm-4.6\"\n" +
		"[worker.settings.env]\n" +
		"ANTHROPIC_BASE_URL = \"https://api.z.ai/api/anthropic\"\n" +
		"ANTHROPIC_AUTH_TOKEN = \"${GLM_API_KEY}\"\n" +
		"[worker.settings.permissions]\n" +
		"allow = [\"Read\", \"Bash(git:*)\"]\n" +
		"[reviewer]\n" +
		"model = \"sonnet\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	b, ok := ac.NamedBinding("worker")
	if !ok {
		t.Fatal("worker binding not found")
	}
	if got := b.Settings["model"]; got != "glm-4.6" {
		t.Errorf("settings.model = %v, want glm-4.6", got)
	}
	env, ok := b.Settings["env"].(map[string]any)
	if !ok {
		t.Fatalf("settings.env = %T, want map[string]any", b.Settings["env"])
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.z.ai/api/anthropic" {
		t.Errorf("settings.env.ANTHROPIC_BASE_URL = %v", env["ANTHROPIC_BASE_URL"])
	}
	perms, ok := b.Settings["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("settings.permissions = %T, want map[string]any", b.Settings["permissions"])
	}
	allow, ok := perms["allow"].([]any)
	if !ok || len(allow) != 2 || allow[0] != "Read" || allow[1] != "Bash(git:*)" {
		t.Errorf("settings.permissions.allow = %v", perms["allow"])
	}
	// A binding with no settings table has a nil map.
	if ac.Reviewer.Settings != nil {
		t.Errorf("reviewer settings = %v, want nil (no settings table declared)", ac.Reviewer.Settings)
	}
}

func TestInjectPrinciplesDefaultsOnAndToggles(t *testing.T) {
	// Absent from agents.toml → default ON.
	if !(AgentBinding{}).InjectsPrinciples() {
		t.Error("an unset binding must inject principles by default")
	}
	// inject_principles is no longer a runtime fallback — use principles=.
	dir := t.TempDir()
	body := "[reviewer]\nprinciples = \"none\"\n[commit-agent]\nprinciples = \"session\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if ac.ReviewerBinding().InjectsPrinciples() {
		t.Error("principles = none must disable injection for the reviewer")
	}
	if nb, ok := ac.NamedBinding("commit-agent"); !ok || !nb.InjectsPrinciples() {
		t.Errorf("named agent with principles = session must inject: ok=%v binding=%+v", ok, nb)
	}
}

func TestResolvedRole(t *testing.T) {
	tests := []struct {
		section string
		role    string
		want    string
	}{
		{"reviewer", "", "reviewer"},
		{"reviewer", "agent", "agent"},
		{"planner", "", "agent"},
		{"planner", "reviewer", "reviewer"},
		{"planner", "AGENT", "agent"},
	}
	for _, tc := range tests {
		got := ResolvedRole(tc.section, AgentBinding{Role: tc.role})
		if got != tc.want {
			t.Errorf("ResolvedRole(%q, role=%q) = %q, want %q", tc.section, tc.role, got, tc.want)
		}
	}
}

func TestResolvedPrinciples(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name string
		b    AgentBinding
		want string
	}{
		{"default", AgentBinding{}, PrinciplesSession},
		{"explicit session", AgentBinding{Principles: "session"}, PrinciplesSession},
		{"none", AgentBinding{Principles: "none"}, PrinciplesNone},
		{"all", AgentBinding{Principles: "all"}, PrinciplesAll},
		// inject_principles alias is no longer a runtime fallback (defaults to session).
		{"legacy inject field ignored", AgentBinding{InjectPrinciples: &off}, PrinciplesSession},
		{"comma list", AgentBinding{Principles: "system, project"}, "system,project"},
	}
	for _, tc := range tests {
		got := tc.b.ResolvedPrinciples()
		if got != tc.want {
			t.Errorf("%s: ResolvedPrinciples() = %q, want %q", tc.name, got, tc.want)
		}
		injects := tc.want != PrinciplesNone
		if tc.b.InjectsPrinciples() != injects {
			t.Errorf("%s: InjectsPrinciples() = %v, want %v", tc.name, tc.b.InjectsPrinciples(), injects)
		}
	}
	_ = on
}

// TestResolvedInterfaceAndLoad pins epic:agent-dispatch-transport interface field.
func TestResolvedInterfaceAndLoad(t *testing.T) {
	if got := (AgentBinding{}).ResolvedInterface(); got != InterfaceCommand {
		t.Errorf("empty interface = %q, want command", got)
	}
	if got := (AgentBinding{Interface: "acp"}).ResolvedInterface(); got != InterfaceACP {
		t.Errorf("acp = %q", got)
	}
	if got := (AgentBinding{Interface: "stream"}).ResolvedInterface(); got != InterfaceStream {
		t.Errorf("stream = %q", got)
	}
	if !(AgentBinding{Interface: "acp"}).IsACP() {
		t.Error("IsACP should be true")
	}
	if !(AgentBinding{Interface: "stream"}).IsStream() {
		t.Error("IsStream should be true")
	}
	if (AgentBinding{Interface: "acp"}).IsStream() {
		t.Error("IsStream should be false for acp")
	}
	// Retired typesafe is an ordinary unknown string at resolve time; load rejects it.
	if got := (AgentBinding{Interface: "typesafe"}).ResolvedInterface(); got != "typesafe" {
		t.Errorf("typesafe passthrough = %q", got)
	}
	if (AgentBinding{Interface: "typesafe"}).IsACP() || (AgentBinding{Interface: "typesafe"}).IsStream() {
		t.Error("typesafe must not report as acp/stream")
	}

	dir := t.TempDir()
	// Unknown interfaces fail at load (rpc and retired typesafe alike).
	for _, iface := range []string{"rpc", "typesafe"} {
		bad := "[reviewer]\ninterface = \"" + iface + "\"\ncommand = \"claude -p {system}\"\n"
		if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAgents(dir); err == nil || !strings.Contains(err.Error(), "interface") {
			t.Fatalf("want interface load error for %q, got %v", iface, err)
		}
	}

	// Valid acp + stream + omit interface still load.
	ok := "[reviewer]\ninterface = \"acp\"\ncommand = \"grok agent stdio\"\ntools = \"read_file\"\n[planner]\ncommand = \"claude -p {system}\"\n[orchestrator]\ninterface = \"stream\"\ncommand = \"claude -p --input-format stream-json --output-format stream-json --verbose --allowedTools {tools}\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(ok), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ac.Reviewer.ResolvedInterface() != InterfaceACP {
		t.Errorf("reviewer interface = %q", ac.Reviewer.ResolvedInterface())
	}
	if ac.Agents["planner"].ResolvedInterface() != InterfaceCommand {
		t.Errorf("planner should default command")
	}
	if ac.Agents["orchestrator"].ResolvedInterface() != InterfaceStream {
		t.Errorf("orchestrator interface = %q", ac.Agents["orchestrator"].ResolvedInterface())
	}
}

func TestResolveSecondary(t *testing.T) {
	dir := t.TempDir()
	body := `
[defaults]
secondary = "fallback"

[reviewer]
command = "claude -p {system}"

[fallback]
command = "grok -p {payload}"
model = "grok-4.5"
`
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ac.Defaults.Secondary != "fallback" {
		t.Fatalf("defaults.secondary = %q", ac.Defaults.Secondary)
	}
	sec, name, ok := ac.ResolveSecondary("reviewer", ac.Reviewer)
	if !ok || name != "fallback" {
		t.Fatalf("resolve secondary for reviewer: ok=%v name=%q", ok, name)
	}
	if sec.Model != "grok-4.5" && !strings.Contains(sec.Command, "grok") {
		t.Fatalf("secondary binding = %+v", sec)
	}
}

// TestResolveIdleTimeout pins the resolution ladder ResolveIdleTimeout walks
// (sty_752c4ef2): a binding's own idle_timeout= wins, else [defaults]
// idle_timeout, else the caller's shipped default — and a malformed
// [defaults] idle_timeout is rejected at LOAD, not deferred to dispatch.
func TestResolveIdleTimeout(t *testing.T) {
	def := 5 * time.Minute
	ac := AgentsConfig{Defaults: AgentsDefaults{IdleTimeout: "10m"}}

	t.Run("binding wins over defaults", func(t *testing.T) {
		got, err := ac.ResolveIdleTimeout(AgentBinding{IdleTimeout: "3m"}, def)
		if err != nil {
			t.Fatal(err)
		}
		if got != 3*time.Minute {
			t.Fatalf("resolved = %v, want 3m", got)
		}
	})

	t.Run("defaults wins over the shipped default", func(t *testing.T) {
		got, err := ac.ResolveIdleTimeout(AgentBinding{}, def)
		if err != nil {
			t.Fatal(err)
		}
		if got != 10*time.Minute {
			t.Fatalf("resolved = %v, want 10m (from [defaults])", got)
		}
	})

	t.Run("shipped default when neither is set", func(t *testing.T) {
		bare := AgentsConfig{}
		got, err := bare.ResolveIdleTimeout(AgentBinding{}, def)
		if err != nil {
			t.Fatal(err)
		}
		if got != def {
			t.Fatalf("resolved = %v, want the shipped default %v", got, def)
		}
	})

	t.Run("malformed defaults idle_timeout is rejected at load", func(t *testing.T) {
		dir := t.TempDir()
		body := "[defaults]\nidle_timeout = \"notaduration\"\n\n[reviewer]\ncommand = \"claude -p {system}\"\n"
		if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAgents(dir); err == nil {
			t.Fatal("LoadAgents must reject a malformed [defaults] idle_timeout")
		} else if !strings.Contains(err.Error(), "idle_timeout") || !strings.Contains(err.Error(), "defaults") {
			t.Errorf("error should name [defaults] idle_timeout, got: %v", err)
		}
	})
}

func TestLoadAgentsEffortAndSecondaryFields(t *testing.T) {
	dir := t.TempDir()
	body := "[reviewer]\ncommand = \"claude -p {system}\"\neffort = \"medium\"\nsecondary = \"x\"\n"
	if err := os.WriteFile(filepath.Join(dir, AgentsConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ac, err := LoadAgents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ac.Reviewer.Effort != "medium" {
		t.Fatalf("effort = %q", ac.Reviewer.Effort)
	}
	if ac.Reviewer.Secondary != "x" {
		t.Fatalf("secondary = %q", ac.Reviewer.Secondary)
	}
}

// TestGrantsContextChannel covers the pull-context predicate that BOTH the
// runtime dispatch refusal (internal/agentstep) and `satelle agent validate`
// consult (sty_87c0ef37). It carries the cases the agentstep table asserted
// before the predicate moved here, so the two paths cannot drift apart.
func TestGrantsContextChannel(t *testing.T) {
	cases := []struct {
		tools string
		want  bool
	}{
		{"", false},
		{"Read,Grep,Glob", false}, // Read is claude's disk-read tool, not the unknown adapter's
		{"write,search_replace", false},
		{"grep,list_dir,write", false},
		{"Bash(satelle:*)", true},
		{"Read,Bash(satelle:*)", true},
		{"Bash", true},
		{"Bash(*)", true},
		{"*", true},
		{"read_file", true},
		{"read_file,write,search_replace", true},
		// Quoted TOML tokens must be judged identically to bare ones — the reason
		// the predicate carries its own quote-stripping tokenizer.
		{`"Bash(satelle:*)"`, true},
		{` 'read_file' , grep `, true},
		{`"Read","Grep"`, false},
	}
	for _, tc := range cases {
		if got := GrantsContextChannel("", tc.tools); got != tc.want {
			t.Errorf("GrantsContextChannel(%q) = %v, want %v", tc.tools, got, tc.want)
		}
	}
}

// The disk-read tool that gives a dispatched agent a context channel is the
// adapter's own (sty_58a9bdc8): pi's is `read`, which is not a channel on another
// adapter, and `read_file` is not one on pi.
func TestGrantsContextChannelIsAdapterAware(t *testing.T) {
	for _, tc := range []struct {
		adapter, tools string
		want           bool
	}{
		{"pi", "read", true},
		{"pi", "read,grep,find,ls", true},
		{"pi", `"read"`, true},
		{"pi", "grep,find,ls", false},
		{"pi", "read_file", false},
		{"pi", "Bash(satelle:*)", true},
		{"claude", "read", false},
		{"claude", "Read", true},
		{"claude", "Read,Grep,Glob", true},
		{"claude", "Grep,Glob", false},
		{"claude", "read_file", false},
		{"grok", "read", false},
		{"grok", "read_file", true},
		{"unknown", "read", false},
	} {
		if got := GrantsContextChannel(tc.adapter, tc.tools); got != tc.want {
			t.Errorf("GrantsContextChannel(%q, %q) = %v, want %v", tc.adapter, tc.tools, got, tc.want)
		}
	}
}

// BindingGrantsContextChannel classifies the adapter from the binding's own command.
func TestBindingGrantsContextChannel(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    AgentBinding
		want bool
	}{
		{"pi with read", AgentBinding{Command: "pi -p {system}", Tools: "read,grep"}, true},
		{"pi path with read", AgentBinding{Command: "/usr/bin/pi -p {system}", Tools: "read"}, true},
		{"pi without read", AgentBinding{Command: "pi -p {system}", Tools: "grep,find"}, false},
		{"pi with --model naming grok", AgentBinding{Command: "pi -p --model openrouter/x-ai/grok-4 {system}", Tools: "read"}, true},
		{"claude with read", AgentBinding{Command: "claude -p {system}", Tools: "read"}, false},
		{"grok with read_file", AgentBinding{Command: "grok -p {system}", Tools: "read_file"}, true},
	} {
		if got := BindingGrantsContextChannel(tc.b); got != tc.want {
			t.Errorf("%s: BindingGrantsContextChannel = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestShellGrantToken names the token a reviewer finding is about; it is a
// reporting helper, never the channel decision.
func TestShellGrantToken(t *testing.T) {
	cases := []struct {
		tools string
		want  string
	}{
		{"read_file,grep,list_dir", ""},
		{"Read,Grep,Glob", ""},
		{"", ""},
		{"Read,Bash(satelle:*)", "Bash(satelle:*)"},
		{"Bash", "Bash"},
		{"Bash(*)", "Bash(*)"},
		{"*", "*"},
		{"Bash(go test)", "Bash(go test)"}, // any shell grant, not just satelle
		{`"Bash(satelle:*)"`, "Bash(satelle:*)"},
	}
	for _, tc := range cases {
		if got := ShellGrantToken(tc.tools); got != tc.want {
			t.Errorf("ShellGrantToken(%q) = %q, want %q", tc.tools, got, tc.want)
		}
	}
}

// TestIsInLoopCommand — an in-loop binding is performed by the driving session
// and never dispatched, so it is exempt from the context-channel requirement.
func TestIsInLoopCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"in-loop", true},
		{"  in-loop  ", true},
		{"IN-LOOP", true},
		{"", false}, // blank is NOT in-loop: bootstrap wires a runner directly
		{"claude -p", false},
		{"in-loop extra", false},
	}
	for _, tc := range cases {
		if got := IsInLoopCommand(tc.cmd); got != tc.want {
			t.Errorf("IsInLoopCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// TestResolveInterface_ExplicitWins — an explicit interface= is never
// overridden, whatever the use (AC3, epic:model-selection child 2).
func TestResolveInterface_ExplicitWins(t *testing.T) {
	var ac AgentsConfig
	for _, use := range []InterfaceUse{UseOneShot, UseLive} {
		b := AgentBinding{Interface: "command"}
		if iface, reason := ac.ResolveInterface(b, use); iface != InterfaceCommand || reason != "explicit" {
			t.Errorf("explicit command, use=%v: got (%q, %q)", use, iface, reason)
		}
		b = AgentBinding{Interface: "acp"}
		if iface, reason := ac.ResolveInterface(b, use); iface != InterfaceACP || reason != "explicit" {
			t.Errorf("explicit acp, use=%v: got (%q, %q)", use, iface, reason)
		}
	}
}

// TestResolveInterface_OneShotAlwaysCommand — a one-shot dispatch (gate
// reviewer, planner, edge advisor) with no interface= always resolves to
// command, regardless of the binding's command line (AC2).
func TestResolveInterface_OneShotAlwaysCommand(t *testing.T) {
	var ac AgentsConfig
	cases := []AgentBinding{
		{},                              // no command at all
		{Command: "claude -p {system}"}, // claude-shaped
		{Command: "grok agent stdio"},   // acp-shaped spawn line
	}
	for _, b := range cases {
		iface, reason := ac.ResolveInterface(b, UseOneShot)
		if iface != InterfaceCommand || reason != "one-shot default" {
			t.Errorf("ResolveInterface(%+v, UseOneShot) = (%q, %q), want (command, one-shot default)", b, iface, reason)
		}
	}
}

// TestResolveInterface_LiveUnsetClaude — a stream-SHAPED claude command used
// live resolves to stream, the shipped default order's first candidate the
// CLI can serve (AC1).
func TestResolveInterface_LiveUnsetClaude(t *testing.T) {
	var ac AgentsConfig
	b := AgentBinding{Command: "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}"}
	iface, reason := ac.ResolveInterface(b, UseLive)
	if iface != InterfaceStream || reason != "live use" {
		t.Errorf("ResolveInterface(claude, UseLive) = (%q, %q), want (stream, live use)", iface, reason)
	}
}

// TestResolveInterface_LiveUnsetClaudeOneShotShaped — a claude command
// AUTHORED one-shot (the [reviewer]/DefaultReviewerCommand shape: `-p
// --output-format json … {system}`) used live must NOT be waved through as
// stream just because its first token is claude: {system} rides the first
// live-session message, not argv, so newStreamRunner rejects the command and
// this must fall back to command — the existing not-live-capable WARN/refusal
// then names the gap explicitly, rather than the session opening broken (the
// AC6 regression: a relay coder/consult binding with an authored one-shot
// command silently "succeeding" resolution and then failing, or opening
// broken, at session-open time).
func TestResolveInterface_LiveUnsetClaudeOneShotShaped(t *testing.T) {
	var ac AgentsConfig
	b := AgentBinding{Command: "claude -p --output-format json --append-system-prompt {system} --allowedTools {tools}"}
	iface, reason := ac.ResolveInterface(b, UseLive)
	if iface != InterfaceCommand || reason != "live use: not live-capable" {
		t.Errorf("ResolveInterface(one-shot claude, UseLive) = (%q, %q), want (command, live use: not live-capable)", iface, reason)
	}
}

// TestResolveInterface_LiveUnsetACPSpawn — any non-claude spawn line used live
// resolves to acp (AC1).
func TestResolveInterface_LiveUnsetACPSpawn(t *testing.T) {
	var ac AgentsConfig
	b := AgentBinding{Command: "gemini --experimental-acp"}
	iface, reason := ac.ResolveInterface(b, UseLive)
	if iface != InterfaceACP || reason != "live use" {
		t.Errorf("ResolveInterface(gemini, UseLive) = (%q, %q), want (acp, live use)", iface, reason)
	}
}

// TestResolveInterface_LiveCursor (sty_10c52ab3): cursor offers command and ACP
// only. `cursor-agent acp` used live resolves to acp; the one-shot template (or a
// line without the acp subcommand) is not live-capable and a stream binding to
// cursor never is.
func TestResolveInterface_LiveCursor(t *testing.T) {
	var ac AgentsConfig
	for command, want := range map[string]string{
		"cursor-agent acp":                     InterfaceACP,
		agentcli.CursorCommand:                 InterfaceCommand,
		"cursor-agent -p --output-format json": InterfaceCommand,
	} {
		if iface, _ := ac.ResolveInterface(AgentBinding{Command: command}, UseLive); iface != want {
			t.Errorf("ResolveInterface(%q, UseLive) = %q, want %q", command, iface, want)
		}
	}
	if liveCapableInterface("cursor-agent -p --input-format stream-json --output-format stream-json", InterfaceStream) {
		t.Error("a stream binding to cursor must not be live-capable")
	}
	if !liveCapableInterface("cursor-agent acp", InterfaceACP) {
		t.Error("cursor acp must be live-capable")
	}
}

// TestResolveInterface_LivePreferenceFromConfig (AC4): the preference order
// between live-capable transports is read from [defaults] live_interfaces,
// not hardcoded — editing ONLY that config changes ResolveInterface's result,
// no code change. An UNAUTHORED command's own capability still has a floor,
// though: acp has no default spawn line (DefaultCommandFor("acp") == ""), so
// narrowing the order to acp-only must NOT "resolve" an empty command to acp
// with a blank command that could never open — it must fall back to command
// and name the gap.
func TestResolveInterface_LivePreferenceFromConfig(t *testing.T) {
	bodyDefault := "[critic]\nrole = \"reviewer\"\n"
	acDefault, err := loadAgentsBody(bodyDefault)
	if err != nil {
		t.Fatalf("load default body: %v", err)
	}
	critic, ok := acDefault.RawBinding("critic")
	if !ok {
		t.Fatal("critic binding not found")
	}
	iface, reason := acDefault.ResolveInterface(critic, UseLive)
	if iface != InterfaceCommand || reason != "live use: no command" {
		t.Fatalf("default order: got (%q, %q), want (command, live use: no command)", iface, reason)
	}
	if eb := acDefault.EffectiveBinding(critic, UseLive); eb.Command != "" {
		t.Errorf("default order: effective command = %q, want empty — no compiled live default", eb.Command)
	}
	// An authored command still resolves by the configured order.
	critic.Command = agentcli.DefaultClaudeStreamCommand
	if iface, reason = acDefault.ResolveInterface(critic, UseLive); iface != InterfaceStream || reason != "live use" {
		t.Fatalf("authored stream command: got (%q, %q), want (stream, live use)", iface, reason)
	}
	// One-shot still fills the command default.
	critic.Command = ""
	if eb := acDefault.EffectiveBinding(critic, UseOneShot); eb.Command != DefaultReviewerCommand {
		t.Errorf("one-shot effective command = %q, want the reviewer default", eb.Command)
	}

	bodyACPOnly := "[defaults]\nlive_interfaces = [\"acp\"]\n\n[critic]\nrole = \"reviewer\"\n"
	acACPOnly, err := loadAgentsBody(bodyACPOnly)
	if err != nil {
		t.Fatalf("load acp-only body: %v", err)
	}
	criticACPOnly, ok := acACPOnly.RawBinding("critic")
	if !ok {
		t.Fatal("critic binding not found (acp-only)")
	}
	iface, reason = acACPOnly.ResolveInterface(criticACPOnly, UseLive)
	if iface != InterfaceCommand || reason != "live use: no command" {
		t.Fatalf("acp-only order: got (%q, %q), want (command, live use: no command) — no compiled live default", iface, reason)
	}
	if eb := acACPOnly.EffectiveBinding(criticACPOnly, UseLive); eb.Command != "" {
		t.Errorf("acp-only order: effective command = %q, want empty — never a fabricated spawn line", eb.Command)
	}

	// A command actually SHAPED for acp (a non-claude token) resolves to acp
	// under that same narrowed order — the gap above was about an unauthored
	// command with no default line, not about acp being unreachable.
	bodySpawner := "[defaults]\nlive_interfaces = [\"acp\"]\n\n[spawner]\nrole = \"agent\"\ncommand = \"gemini --experimental-acp\"\n"
	acSpawner, err := loadAgentsBody(bodySpawner)
	if err != nil {
		t.Fatalf("load spawner body: %v", err)
	}
	spawner, ok := acSpawner.RawBinding("spawner")
	if !ok {
		t.Fatal("spawner binding not found")
	}
	iface, reason = acSpawner.ResolveInterface(spawner, UseLive)
	if iface != InterfaceACP || reason != "live use" {
		t.Fatalf("spawner: got (%q, %q), want (acp, live use)", iface, reason)
	}

	if _, err := loadAgentsBody("[defaults]\nlive_interfaces = [\"bogus\"]\n"); err == nil {
		t.Error("live_interfaces with an unknown token must fail load")
	}
}

// TestLiveBinding_NoCommandYet — a live binding with no command authored is
// never filled in with a provider's line: it stays empty so the open refuses.
func TestLiveBinding_NoCommandYet(t *testing.T) {
	ac, err := loadAgentsBody("[coder]\nrole = \"agent\"\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b, ok := ac.LiveBinding("coder")
	if !ok {
		t.Fatal("coder binding not found")
	}
	if b.Command != "" {
		t.Errorf("command = %q, want empty (no compiled live default)", b.Command)
	}
}

func TestLiveCommandRequiredErrorNamesEveryTransport(t *testing.T) {
	msg := LiveCommandRequiredError("coder").Error()
	for _, want := range []string{"[coder]", "no command and no profile", InterfaceStream, InterfaceACP, "command=", "profile="} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

// TestLiveBinding_ReviewerGetsDefaultTools — a rework.consult=reviewer (or any
// live open of the [reviewer] section) with no authored tools must still get
// the read-only ceiling ReviewerBinding() gives the one-shot gate path, not a
// bare empty grant just because it went through the live seam instead.
func TestLiveBinding_ReviewerGetsDefaultTools(t *testing.T) {
	ac, err := loadAgentsBody("[reviewer]\nrole = \"reviewer\"\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b, ok := ac.LiveBinding("reviewer")
	if !ok {
		t.Fatal("reviewer binding not found")
	}
	if b.Tools != DefaultReviewerTools {
		t.Errorf("tools = %q, want %q (the same ceiling ReviewerBinding() applies)", b.Tools, DefaultReviewerTools)
	}
	// An authored grant is never overridden.
	ac2, err := loadAgentsBody("[reviewer]\nrole = \"reviewer\"\ntools = \"Read\"\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b2, ok := ac2.LiveBinding("reviewer")
	if !ok {
		t.Fatal("reviewer binding not found")
	}
	if b2.Tools != "Read" {
		t.Errorf("tools = %q, want the authored grant Read", b2.Tools)
	}
}

// TestLiveBinding_ExecutorEmptyCommandStaysInLoop — an unset [executor]
// command has an established meaning ALREADY (ExecutorBinding: the driving
// agent itself). LiveBinding must resolve it to "in-loop", not read the empty
// command as "no shape yet, pick me a live default" and quietly turn the
// driving session into a spawned Claude stream session.
func TestLiveBinding_ExecutorEmptyCommandStaysInLoop(t *testing.T) {
	var ac AgentsConfig
	b, ok := ac.LiveBinding("executor")
	if !ok {
		t.Fatal("executor binding not found")
	}
	if !IsInLoopCommand(b.CommandTemplate()) {
		t.Errorf("executor command = %q, want the in-loop default", b.CommandTemplate())
	}
}

// OneShotBinding is how the headless drive reads [executor]: an unset command is
// still the driving session (never a reviewer default), a headless command is a
// one-shot run on the command transport — the live transport a stream-capable
// command would pick for a session is not what a single run uses — and an
// authored interface is never overridden.
func TestOneShotBindingResolvesTheExecutorForASingleRun(t *testing.T) {
	var unset AgentsConfig
	b, ok := unset.OneShotBinding("executor")
	if !ok || !IsInLoopCommand(b.CommandTemplate()) {
		t.Errorf("an unset [executor] must stay in-loop, got %+v (found %v)", b, ok)
	}

	ac := AgentsConfig{Executor: AgentBinding{Command: "claude --output-format stream-json --input-format stream-json"}}
	one, _ := ac.OneShotBinding("executor")
	if got := one.ResolvedInterface(); got != InterfaceCommand {
		t.Errorf("one-shot interface = %q, want %q", got, InterfaceCommand)
	}
	if live, _ := ac.LiveBinding("executor"); live.ResolvedInterface() == InterfaceCommand {
		t.Errorf("fixture must be live-capable for the contrast to mean anything, live = %q", live.ResolvedInterface())
	}

	ac.Executor.Interface = InterfaceACP
	if one, _ := ac.OneShotBinding("executor"); one.ResolvedInterface() != InterfaceACP {
		t.Errorf("an authored interface wins, got %q", one.ResolvedInterface())
	}
	if _, ok := ac.OneShotBinding("nobody"); ok {
		t.Errorf("an unknown binding must report not found")
	}
}

// sty_7bfa980d AC1: model= takes an ordered comma-separated list. A single model
// resolves exactly as before (no fallbacks); a list keeps its first entry in
// Model and the rest, trimmed and in order, in ModelFallbacks. The decoded layer
// is never rewritten.
func TestResolveAgentsSplitsAModelList(t *testing.T) {
	repo := repoAgents(t, "[reviewer]\nmodel = \"m1\"\n\n[lister]\nrole = \"reviewer\"\nmodel = \"m1, m2 ,m3\"\n")
	agents, _, err := ResolveAgentsLayered(repo, AgentsConfig{}, GlobalAgentsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	single := agents.Reviewer
	if single.Model != "m1" || single.ModelFallbacks != nil {
		t.Fatalf("single model: Model=%q ModelFallbacks=%v, want m1 and nil", single.Model, single.ModelFallbacks)
	}
	if got := single.Models(); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("single model: Models()=%v, want [m1]", got)
	}
	list := agents.Agents["lister"]
	if list.Model != "m1" {
		t.Fatalf("list: Model=%q, want the first entry m1", list.Model)
	}
	if got := strings.Join(list.Models(), "|"); got != "m1|m2|m3" {
		t.Fatalf("list: Models()=%q, want m1|m2|m3", got)
	}
	if raw := repo.Agents["lister"]; raw.Model != "m1, m2 ,m3" || raw.ModelFallbacks != nil {
		t.Fatalf("the decoded layer must be unchanged: %+v", raw)
	}
	if got := (AgentBinding{}).Models(); got != nil {
		t.Fatalf("no model: Models()=%v, want nil", got)
	}
}
