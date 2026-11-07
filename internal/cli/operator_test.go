package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// writeOperatorWorkflow adds a workflow st-one -> deploy (the operator's,
// recording deploy-record) -> st-three, beside the gated one.
func writeOperatorWorkflow(t *testing.T, dir string) {
	t.Helper()
	writeGatedWorkflow(t, dir)
	p := filepath.Join(dir, ".satelle", "workflows", "ops.toml")
	body := `name = "ops"
[states.st-one]
start = true
[states.deploy]
performer = { operator = "deploy-record" }
[states.st-three]
terminal = true
[[transition]]
from = ["st-one"]
to = "deploy"
[[transition]]
from = ["deploy"]
to = "st-three"
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.md"), []byte("deployed v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeTerminal answers the confirmation with answer and keeps the prompt.
type fakeTerminal struct {
	io.Reader
	prompt strings.Builder
}

func (f *fakeTerminal) Write(p []byte) (int, error) { return f.prompt.Write(p) }
func (f *fakeTerminal) Close() error                { return nil }

// useTerminal stands in a controlling terminal that types answer; a nil
// terminal is none.
func useTerminal(t *testing.T, answer *string) *fakeTerminal {
	t.Helper()
	prev := openTerminal
	t.Cleanup(func() { openTerminal = prev })
	if answer == nil {
		openTerminal = func() (io.ReadWriteCloser, error) { return nil, errors.New("open /dev/tty: no such device") }
		return nil
	}
	ft := &fakeTerminal{Reader: strings.NewReader(*answer + "\n")}
	openTerminal = func() (io.ReadWriteCloser, error) { return ft, nil }
	return ft
}

func chainKinds(t *testing.T, id string) []ledger.Event {
	t.Helper()
	var events []ledger.Event
	if err := json.Unmarshal([]byte(must(t, "ledger", "list", id, "--json")), &events); err != nil {
		t.Fatal(err)
	}
	return events
}

func countEvents(t *testing.T, id, kind string) int {
	t.Helper()
	n := 0
	for _, e := range chainKinds(t, id) {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// AC1: a seat on an item in an operator state allows no edits.
func TestEditGateRefusesOperatorState(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeOperatorWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "ship", "--workflow", "ops")
	must(t, "story", "set", id, "--status", "deploy")
	must(t, "story", "seat", id, "--session", "s-1")
	if deny, _ := hookGate(t, dir, "s-1", "Edit", "main.go", ""); !deny {
		t.Error("a seated edit in an operator state was allowed")
	}
	if deny, _ := hookGate(t, dir, "s-1", "Bash", "", "echo hi > out.txt"); !deny {
		t.Error("a seated shell write in an operator state was allowed")
	}
	t.Setenv("SATELLE_SESSION", "s-1")
	out := must(t, "status")
	if !strings.Contains(out, id) || strings.Contains(out, "performing") {
		t.Errorf("status should list the seat without performing:\n%s", out)
	}
	if route := must(t, "story", "route", id); !strings.Contains(route, "performer operator (records deploy-record)") {
		t.Errorf("route does not show the operator:\n%s", route)
	}
}

// AC2: leaving is refused until the operator records; the record verb lets it
// leave; raw appends cannot forge the record or the move.
func TestOperatorRecordLetsTheItemLeave(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeOperatorWorkflow(t, dir)
	must(t, "validate")
	id := createID(t, "story", "create", "--title", "ship", "--workflow", "ops")
	must(t, "story", "set", id, "--status", "deploy")

	_, err := run(t, "", "story", "set", id, "--status", "st-three")
	if err == nil || !strings.Contains(err.Error(), "satelle story record "+id) {
		t.Fatalf("leaving without a record: %v", err)
	}
	ft := useTerminal(t, &id)
	out := must(t, "story", "record", id, "--file", "evidence.md")
	if !strings.Contains(out, "recorded the operator's deploy-record on "+id) || !strings.Contains(ft.prompt.String(), "Type the item id to confirm") {
		t.Fatalf("record: %s / prompt %q", out, ft.prompt.String())
	}
	var rec struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
		Operator  bool   `json:"operator"`
	}
	for _, e := range chainKinds(t, id) {
		if e.Kind == "attachment.added" {
			if err := e.Decode(&rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !rec.Operator || rec.Name != "deploy-record" || !strings.Contains(rec.MediaType, "type=deploy-record") {
		t.Fatalf("recorded %+v", rec)
	}
	if atts := must(t, "story", "attachments", id, "--json"); !strings.Contains(atts, "type=deploy-record") {
		t.Errorf("attachments: %s", atts)
	}
	must(t, "story", "set", id, "--status", "st-three")
}

// AC2: satelle ledger append refuses the kinds satelle reads, so it can neither
// forge the operator's record nor move the item out.
func TestLedgerAppendRefusesFoldedKinds(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeOperatorWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "ship", "--workflow", "ops")
	must(t, "story", "set", id, "--status", "deploy")

	forged := `{"name":"deploy-record","sha256":"` + strings.Repeat("a", 64) + `","size":1,"media_type":"text/markdown; type=deploy-record","operator":true}`
	if _, err := run(t, "", "ledger", "append", id, "--kind", "attachment.added", "--payload", forged); err == nil || !strings.Contains(err.Error(), "written only by satelle's own verbs") {
		t.Fatalf("forged attachment.added: %v", err)
	}
	if _, err := run(t, "", "ledger", "append", id, "--kind", "item.transitioned", "--payload", `{"from":"deploy","to":"st-three"}`); err == nil {
		t.Fatal("forged item.transitioned was appended")
	}
	if n := countEvents(t, id, "attachment.added"); n != 0 {
		t.Fatalf("attachment.added on the chain: %d", n)
	}
	if n := countEvents(t, id, "item.transitioned"); n != 1 {
		t.Fatalf("item.transitioned on the chain: %d", n)
	}
	if out := must(t, "story", "get", id); !strings.Contains(out, "deploy") || strings.Contains(out, "st-three") {
		t.Fatalf("item moved:\n%s", out)
	}
	if _, err := run(t, "", "story", "set", id, "--status", "st-three"); err == nil {
		t.Fatal("left the operator state after forged appends")
	}
	must(t, "ledger", "append", id, "--kind", "github.review", "--payload", "{}")
}

// AC3: an agent session cannot record the operator's word: the kernel refuses
// a session, the verb refuses without the id retyped on a terminal, and the
// hook refuses the verb in any agent tool call.
func TestOperatorRecordRefusesAnAgent(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeOperatorWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "ship", "--workflow", "ops")
	must(t, "story", "set", id, "--status", "deploy")
	must(t, "story", "seat", id, "--session", "s-1")

	refused := func(want string) {
		t.Helper()
		_, err := run(t, "", "story", "record", id, "--file", "evidence.md")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("record: want %q, got %v", want, err)
		}
		if n := countEvents(t, id, "attachment.added"); n != 0 {
			t.Fatalf("attachment.added on the chain: %d", n)
		}
		if _, err := run(t, "", "story", "set", id, "--status", "st-three"); err == nil {
			t.Fatal("left the operator state")
		}
	}

	// A session, even with the right id typed on a terminal.
	useTerminal(t, &id)
	t.Setenv("SATELLE_SESSION", "s-1")
	refused("an agent session (s-1) may not record the operator's word")
	t.Setenv("SATELLE_SESSION", "")

	// env -u SATELLE_SESSION: no controlling terminal.
	useTerminal(t, nil)
	refused("no controlling terminal")

	// A terminal that answers another id.
	wrong := "sto_00000000"
	useTerminal(t, &wrong)
	refused("is not " + id)

	// The hook, for the session holding a seat on the item.
	for _, c := range []string{
		"satelle story record " + id + " --file f",
		"env -u SATELLE_SESSION satelle story record " + id + " --file f",
	} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); !deny || !strings.Contains(why, "the operator's alone") {
			t.Errorf("hook allowed %q (%s)", c, why)
		}
	}
}
