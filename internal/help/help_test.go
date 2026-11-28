package help

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

func TestTopicsShipWithTitles(t *testing.T) {
	want := map[string]bool{"satelle": false, "workflows": false, "edit-gate": false, "configuration": false, "ledger-store": false, "agents": false}
	for _, top := range List() {
		if top.Title == "" || top.Body == "" {
			t.Errorf("topic %q has no title or body", top.Name)
		}
		if _, ok := want[top.Name]; ok {
			want[top.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("topic %q is missing", name)
		}
	}
}

func TestProjectAndWorkspaceDefinedOnce(t *testing.T) {
	top, ok := Get("projects")
	if !ok {
		t.Fatal("no projects topic")
	}
	for _, want := range []string{"A **project** is a repo or directory", "bound to at most one satelle.dev project", "A **workspace** is a collection of projects on satelle.dev"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the projects topic lacks %q", want)
		}
	}
	for _, other := range List() {
		if other.Name == "projects" {
			continue
		}
		if strings.Contains(other.Body, "is a repo or directory") || strings.Contains(other.Body, "is a collection of projects") {
			t.Errorf("topic %s defines a word again", other.Name)
		}
	}
	if s, _ := Get("satelle"); !strings.Contains(s.Body, "satelle help projects") {
		t.Error("the satelle topic does not point to satelle help projects")
	}
}

func TestQueueFilesDocumented(t *testing.T) {
	for topic, wants := range map[string][]string{
		"queues":    {".satelle/queues/<name>.toml", `enqueue = ["release"]`, "satelle queue set` refuses"},
		"workflows": {`enqueue = ["<name>"]`, ".satelle/queues/<name>.toml"},
	} {
		top, ok := Get(topic)
		if !ok {
			t.Fatalf("no %s topic", topic)
		}
		for _, want := range wants {
			if !strings.Contains(top.Body, want) {
				t.Errorf("the %s topic lacks %q", topic, want)
			}
		}
	}
}

func TestTriggerExitConventionDocumented(t *testing.T) {
	top, ok := Get("workflows")
	if !ok {
		t.Fatal("no workflows topic")
	}
	for _, want := range []string{"exit 0 runs the gate, exit 1 skips it", "trigger <skill> failed (exit <code>); the gate ran"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the workflows topic lacks %q", want)
		}
	}
}

func TestLedgerStoreContractDocumented(t *testing.T) {
	top, ok := Get("ledger-store")
	if !ok {
		t.Fatal("no ledger-store topic")
	}
	for _, want := range []string{
		"satelle runs the command once per operation, with the configured args, then the operation and its operands; credentials come from the command's own environment (for Azure, the managed identity), never from satelle.",
		"- `put-if-absent <key>`: reads the object body on stdin. Exit 0 = created. Exit 3 = an object with that key already exists, and it prints that object's body to stdout so satelle can compare. Anything else is an error.",
		"- `get <key>`: writes the body to stdout. Exit 0 = found. Exit 4 = no such key.",
		"- `list <prefix> [<after-key>]`: prints the keys under the prefix, one per line, sorted, starting after `<after-key>` when given. Exit 0, including when there are none.",
		"`error: <code>: <message>`, where `<code>` is one of `auth`, `not-found`, `conflict`, `unavailable`, `invalid`.",
		"- Keys are ASCII `[A-Za-z0-9._/-]`, at most 512 bytes; bodies are at most 64 MiB.",
		"exit 2 with\n  code `invalid`",
		"`LC_ALL=C sort`",
		"`conformance/<run>/`",
		"`SATELLE_TOKEN` and `SATELLE_NODE_TOKEN`",
		"adapters/satelle-store-fs",
		ledgerstore.CheckListEmpty, ledgerstore.CheckGetMissing, ledgerstore.CheckPutNew, ledgerstore.CheckPutExisting,
		ledgerstore.CheckGetPresent, ledgerstore.CheckListSorted, ledgerstore.CheckListAfter, ledgerstore.CheckErrorLine,
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the ledger-store topic lacks %q", want)
		}
	}
	if c, _ := Get("configuration"); !strings.Contains(c.Body, "[ledger]") || !strings.Contains(c.Body, "satelle help ledger-store") {
		t.Error("the configuration topic does not document [ledger]")
	}
}

func TestLedgerStoreChoiceDocumented(t *testing.T) {
	top, _ := Get("ledger-store")
	for _, want := range []string{
		"## Choosing the store", `store = "satelle.dev"`, `store = "local"`,
		"is `satelle.dev` while signed in", "`local` otherwise",
		"## Push, pull and status", "satelle ledger status", "performer-output-<event id>",
		"No hook, gate, story or ledger read",
		"`satelle remote fire`, a remote placement", "store-test checks only a configured store\ncommand",
		"and `args` may be omitted",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the ledger-store topic lacks %q", want)
		}
	}
	if strings.Contains(top.Body, "satelle sync") {
		t.Error("the ledger-store topic names the removed satelle sync")
	}
}

func TestAgentsTopicDocumentsAgy(t *testing.T) {
	var agents string
	for _, top := range List() {
		if top.Name == "agents" {
			agents = top.Body
		}
	}
	for _, want := range []string{"agy", "--input-format stream-json", "--settings", "--add-dir", "PreToolUse", "command(*)", ".agents/hooks.json", ".antigravity/settings.json", "AGY_CLI_DISABLE_AUTO_UPDATE"} {
		if !strings.Contains(agents, want) {
			t.Errorf("agents topic lacks %q", want)
		}
	}
}

func TestAgentsTopicDocumentsTargetItems(t *testing.T) {
	var agents string
	for _, top := range List() {
		if top.Name == "agents" {
			agents = top.Body
		}
	}
	for _, want := range []string{"## Target items", "--setting-sources user --restricted", "grok under its `--sandbox`", "allowUnsandboxedCommands: false", "scratch folder", "protected-tier", "harness-confinement.md"} {
		if !strings.Contains(agents, want) {
			t.Errorf("agents topic lacks %q", want)
		}
	}
}

func TestAgentsTopicDocumentsCursor(t *testing.T) {
	var agents, workflows string
	for _, top := range List() {
		switch top.Name {
		case "agents":
			agents = top.Body
		case "workflows":
			workflows = top.Body
		}
	}
	for _, want := range []string{"cursor-agent", "--mode ask", "--force", "CURSOR_CONFIG_DIR", "Shell(**)", "heredoc"} {
		if !strings.Contains(agents, want) {
			t.Errorf("agents topic lacks %q", want)
		}
	}
	if strings.Contains(agents, "Shell(**satelle**)") {
		t.Error("agents topic still names the per-command Shell(**satelle**) rule")
	}
	if !strings.Contains(workflows, "satelle help agents") {
		t.Error("workflows does not point to satelle help agents")
	}
}

func TestEditGateTopicDocumentsCursorIDE(t *testing.T) {
	gate, ok := Get("edit-gate")
	if !ok {
		t.Fatal("no edit-gate topic")
	}
	agents, ok := Get("agents")
	if !ok {
		t.Fatal("no agents topic")
	}
	body := strings.Join(strings.Fields(gate.Body), " ")
	if !strings.Contains(gate.Body, "\n## Cursor IDE sessions\n") {
		t.Error("edit-gate lacks the ## Cursor IDE sessions heading")
	}
	for _, want := range []string{
		"Cursor IDE 3.28.7",
		"(`session_id` equals `conversation_id`), which is the session satelle uses",
		"its sessionStart `generation_id` is empty where cursor-agent's equals the session id; satelle reads `model_id` as a fallback for `model` and ignores the rest",
		"Cursor's prompt event has no context channel, so the seats are in the session-start context and in `satelle status --session <session>`",
		"A repo wired only for claude (`satelle agents install claude --session <session>`, no `.cursor/hooks.json`) is gated in the Cursor IDE too",
		"attempt to write a readonly database",
		"`satelle story release <id> --session <session>`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edit-gate topic lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(agents.Body), " "), "the Cursor IDE's agent") {
		t.Error("agents topic does not name the Cursor IDE's agent")
	}
}

func TestLedgerStoreSyncInstalledByInstallAndUpdate(t *testing.T) {
	top, ok := Get("ledger-store")
	if !ok {
		t.Fatal("no ledger-store topic")
	}
	for _, want := range []string{"satelle update", "install.sh", "--no-service", "SATELLE_NO_SERVICE=1"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("ledger-store lacks %q", want)
		}
	}
}

func helpSection(t *testing.T, topic, heading string) string {
	t.Helper()
	top, ok := Get(topic)
	if !ok {
		t.Fatalf("no %s topic", topic)
	}
	_, rest, ok := strings.Cut(top.Body, "\n"+heading+"\n")
	if !ok {
		t.Fatalf("the %s topic has no %s section", topic, heading)
	}
	rest, _, _ = strings.Cut(rest, "\n## ")
	return rest
}

func TestConventionalTagsDocumented(t *testing.T) {
	tags := helpSection(t, "satelle", "## Conventional tags")
	for _, want := range []string{"`priority:high`", "`priority:medium`", "`priority:low`", "Optional", "`surface:", "`reporter:", "`source:", "satelle validate"} {
		if !strings.Contains(tags, want) {
			t.Errorf("Conventional tags lacks %q", want)
		}
	}
	_, surface, ok := strings.Cut(tags, "- `surface:")
	if !ok {
		t.Fatal("Conventional tags has no surface bullet")
	}
	surface, _, _ = strings.Cut(surface, "\n- ")
	for _, banned := range []string{"applies_to", "gate"} {
		if strings.Contains(strings.ToLower(surface), banned) {
			t.Errorf("the surface bullet names %q, a gate key the workflow loader does not read: %q", banned, surface)
		}
	}
	for _, c := range item.Conventions {
		if !strings.Contains(tags, "`"+c.Key+":") {
			t.Errorf("Conventional tags does not name the key %s", c.Key)
		}
		for _, v := range c.Values {
			if !strings.Contains(tags, "`"+c.Key+":"+v+"`") {
				t.Errorf("Conventional tags does not name %s:%s", c.Key, v)
			}
		}
	}
	ui := helpSection(t, "configuration", "## ui.toml")
	if !strings.Contains(ui, "[tag_colours]") {
		t.Error("the ui.toml section does not name [tag_colours]")
	}
	for _, c := range item.ChipColours {
		if !strings.Contains(ui, "`"+c+"`") {
			t.Errorf("the ui.toml section does not name the colour %s", c)
		}
	}
}

func TestSubstrateDirDocumented(t *testing.T) {
	flat := func(name string) string {
		top, ok := Get(name)
		if !ok {
			t.Fatalf("no %s topic", name)
		}
		return strings.Join(strings.Fields(top.Body), " ")
	}
	name := "`" + config.SubstrateDirEnv + "`"
	wf := flat("workflows")
	for _, want := range []string{name, "every dispatched performer and every judge", "doctor check"} {
		if !strings.Contains(wf, want) {
			t.Errorf("the workflows topic lacks %q", want)
		}
	}
	rem := flat("remotes")
	for _, want := range []string{"Setup commands a node runs get " + name, "docker-local node's container-start `[setup]`", "has " + name + " unset"} {
		if !strings.Contains(rem, want) {
			t.Errorf("the remotes topic lacks %q", want)
		}
	}
	var set []string
	for _, kv := range (gate.CheckEnv{Substrate: "x"}).Vars() {
		if strings.HasSuffix(kv, "=x") {
			set = append(set, kv)
		}
	}
	if len(set) != 1 || set[0] != config.SubstrateDirEnv+"=x" {
		t.Errorf("CheckEnv{Substrate} sets %q, want only %s", set, config.SubstrateDirEnv)
	}
}

func TestWorktreeCharsetDocumented(t *testing.T) {
	top, ok := Get("workflows")
	if !ok {
		t.Fatal("no workflows topic")
	}
	start := strings.Index(top.Body, "every\ncheck also gets `SATELLE_WORKTREE`")
	if start < 0 {
		t.Fatal("the workflows topic has no SATELLE_WORKTREE paragraph")
	}
	p := top.Body[start:]
	if end := strings.Index(p, "\n\n"); end >= 0 {
		p = p[:end]
	}
	for _, want := range []string{"[A-Za-z0-9._/+@-]", "2.44", "partial", "`worktree`", "is not passed on"} {
		if !strings.Contains(p, want) {
			t.Errorf("the SATELLE_WORKTREE paragraph does not say %q:\n%s", want, p)
		}
	}
}

func TestCheckStdinKeysDocumented(t *testing.T) {
	top, ok := Get("workflows")
	if !ok {
		t.Fatal("no workflows topic")
	}
	start := strings.Index(top.Body, "A skill whose frontmatter says `type: check`")
	if start < 0 {
		t.Fatal("the workflows topic has no check-skill paragraph")
	}
	p := top.Body[start:]
	if end := strings.Index(p, "\n\n"); end >= 0 {
		p = p[:end]
	}
	p = strings.Join(strings.Fields(p), " ")
	if !strings.Contains(p, "JSON object on stdin") {
		t.Errorf("the check-skill paragraph does not say a check gets a JSON object on stdin: %q", p)
	}
	documented := map[string]string{}
	for _, m := range regexp.MustCompile("`([a-z_]+)` \\(`(SATELLE_[A-Z_]+)`\\)").FindAllStringSubmatch(p, -1) {
		documented[m[1]] = m[2]
	}
	env := gate.CheckEnv{Item: "i", From: "f", To: "t", Base: "b"}
	var payload map[string]any
	if err := json.Unmarshal(env.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for _, kv := range env.Vars() {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	for key := range documented {
		if _, ok := payload[key]; !ok {
			t.Errorf("the help names stdin key %q, which CheckEnv.Payload does not write", key)
		}
	}
	for key := range payload {
		if _, ok := documented[key]; !ok {
			t.Errorf("CheckEnv.Payload writes stdin key %q, which the help does not name", key)
		}
	}
	for key, variable := range documented {
		got, ok := payload[key]
		if !ok {
			continue
		}
		want, ok := vars[variable]
		if !ok {
			t.Errorf("the help pairs stdin key %q with %s, which CheckEnv.Vars does not set", key, variable)
			continue
		}
		if got != want {
			t.Errorf("the help pairs stdin key %q with %s, but the payload holds %v and the variable %q", key, variable, got, want)
		}
	}
}

func TestAgentsTopicDocumentsAutoMode(t *testing.T) {
	agents, ok := Get("agents")
	if !ok {
		t.Fatal("no agents topic")
	}
	for _, want := range []string{
		"satelle agents auto-mode <harness> --session <session>",
		".satelle/auto-mode.md",
		"~/.claude/settings.json",
		`"$defaults"`,
		"unavailable",
		"`pi`, `grok`, `cursor`, `agy`",
	} {
		if !strings.Contains(agents.Body, want) {
			t.Errorf("agents topic lacks %q", want)
		}
	}
	cfg, ok := Get("configuration")
	if !ok {
		t.Fatal("no configuration topic")
	}
	for _, want := range []string{"`auto-mode.md`", "satelle agents auto-mode <harness> --session <session>"} {
		if !strings.Contains(cfg.Body, want) {
			t.Errorf("configuration topic lacks %q", want)
		}
	}
}

func TestDiffMaxBytesDocumented(t *testing.T) {
	top, ok := Get("workflows")
	if !ok {
		t.Fatal("no workflows topic")
	}
	body := strings.Join(strings.Fields(top.Body), " ")
	for _, want := range []string{"diff_max_bytes", "64,000", "cut files:", "deleted file:"} {
		if !strings.Contains(body, want) {
			t.Errorf("the workflows topic lacks %q", want)
		}
	}
}

func TestPackTomlDocumented(t *testing.T) {
	top, ok := Get("configuration")
	if !ok {
		t.Fatal("no configuration topic")
	}
	if !strings.Contains(top.Body, "`skills/<name>/pack.toml`") {
		t.Error("configuration topic does not list skills/<name>/pack.toml")
	}
	if !strings.Contains(top.Body, "`skills/satelle-pack.md`") {
		t.Error("configuration topic does not list skills/satelle-pack.md")
	}
	pack := helpSection(t, "configuration", "## pack.toml")
	for _, want := range []string{
		".satelle/skills/<entry>/pack.toml",
		"SKILL.md",
		`name = "<pack-name>"`,
		`version = "<version>"`,
		`skills = ["<member-skill>"]`,
		"[needs]",
		`scripts = ["<script>"]`,
		`agents = ["<agent>"]`,
		`network = ["<host>"]`,
		`secrets = ["<secret>"]`,
		"unknown_keys",
		"unknown-keys",
		".satelle/packs.toml",
		"satelle pack install",
		"satelle pack update",
		"satelle pack status",
		"satelle-pack-version-check",
		"satelle pack remove",
		"satelle pack publish",
		"satelle pack visibility",
		"--force",
		"pack-merge",
		"[[pack]]",
		"[[pack.file]]",
		"sha256",
		"locked_paths",
	} {
		if !strings.Contains(pack, want) {
			t.Errorf("pack.toml section lacks %q", want)
		}
	}
}

func TestAgentsTopicDocumentsHerdrPanel(t *testing.T) {
	sec := helpSection(t, "agents", "## herdr panel")
	for _, want := range []string{"herdr-app", "stories panel", "satelle herdr install --session <session>", "mods/satelle/main.js",
		"layouts/satelle.json", "~/.config/herdr-app", "Linux", "macOS", "Windows", "HERDR_APP_HOME", "herdr.home", "satelle update"} {
		if !strings.Contains(sec, want) {
			t.Errorf("the herdr panel section lacks %q", want)
		}
	}
}

func TestRemotesTopicDocumentsAPlainImage(t *testing.T) {
	top, ok := Get("remotes")
	if !ok {
		t.Fatal("no remotes topic")
	}
	for _, want := range []string{"may start from a plain image that [setup] prepares", "satelle is copied in at the fire", "docker-plain-probe", "is refused before setup, naming the image\nand the reason"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the remotes topic lacks %q", want)
		}
	}
}
