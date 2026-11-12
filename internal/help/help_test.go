package help

import (
	"strings"
	"testing"

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

// The two words are defined once, in the projects topic.
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

// Queue files are documented where queues and workflows are.
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

// The workflows topic names a trigger's exit convention.
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

// The ledger-store topic carries the adapter contract and names the checks
// store-test runs, as the code names them (sto_eb93dfd8).
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

// The ledger-store topic says how the store is chosen and that push, pull and
// status are the one replication surface for every store (sto_9b98696a).
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

// The agents topic documents the cursor-agent adapter, and workflows points to
// it (sto_0f41dda9 AC5).
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

// The edit-gate topic says what a Cursor IDE session gets, from the IDE run's
// evidence (sto_bf491f0d AC6, AC7), and the agents topic names the IDE.
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
		"Cursor's prompt event has no context channel, so the seats are in the session-start context and in `satelle status`",
		"A repo wired only for claude (`satelle agents install claude`, no `.cursor/hooks.json`) is gated in the Cursor IDE too",
		"attempt to write a readonly database",
		"`satelle story release <id>`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edit-gate topic lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(agents.Body), " "), "the Cursor IDE's agent") {
		t.Error("agents topic does not name the Cursor IDE's agent")
	}
}

// The automatic sync section says installing or updating satelle starts the
// timer and names the opt-out (sto_1b6a7ed3 AC3).
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
