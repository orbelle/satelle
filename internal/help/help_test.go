package help

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

func TestTopicsShipWithTitles(t *testing.T) {
	want := map[string]bool{"satelle": false, "workflows": false, "edit-gate": false, "configuration": false, "ledger-store": false}
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
