//go:build integration

package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The human's start (sto_33a25a76): this repo's delivery workflow makes
// backlog the operator's state (type go), so a delivery story enters
// development only on the operator's recorded go. These tests read this
// repo's own configuration; the ledger lives in temp dirs, never the repo's
// runtime.

// deliveryKernel opens a kernel on this repo's .satelle with a temp runtime
// and working tree, and creates a story, which the repo's rules route to
// delivery at backlog.
func deliveryKernel(t *testing.T) (*kernel.Kernel, string) {
	t.Helper()
	k, err := kernel.Open(kernel.Options{Root: filepath.Join("..", ".satelle"), Runtime: t.TempDir(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	kind, title := "story", "try the go"
	it, err := k.Create(item.Fields{Kind: &kind, Title: &title}, "perform_test")
	if err != nil {
		t.Fatal(err)
	}
	if it.Workflow != "delivery" || it.Status != "backlog" {
		t.Fatalf("a new story is %s at %s, want delivery at backlog", it.Workflow, it.Status)
	}
	return k, it.ID
}

func chain(t *testing.T, k *kernel.Kernel, id string) []item.Attachment {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return item.FoldAttachments(events)
}

func chainLen(t *testing.T, k *kernel.Kernel, id string) int {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}

// AC1: a delivery story cannot leave backlog, by any exit, without the
// operator's recorded go; the refusal names the state, the type and the
// command, and appends nothing.
func TestDeliveryBacklogNeedsTheGo(t *testing.T) {
	k, id := deliveryKernel(t)
	n := chainLen(t, k, id)
	for _, to := range []string{"plan", "blocked", "cancelled"} {
		_, err := k.Transition(context.Background(), id, to, "perform_test")
		var ore *kernel.OperatorRecordError
		if !errors.As(err, &ore) || ore.State != "backlog" || ore.Type != "go" || !strings.Contains(err.Error(), "satelle story record "+id) {
			t.Fatalf("backlog -> %s without the go: %v", to, err)
		}
	}
	if got := chainLen(t, k, id); got != n {
		t.Fatalf("a refused exit appended %d events", got-n)
	}
}

// AC2: an agent session cannot give the go: its record is refused, and a
// go-typed attachment it adds is not the operator's word.
func TestDeliveryGoIsNeverAnAgents(t *testing.T) {
	k, id := deliveryKernel(t)
	n := chainLen(t, k, id)
	if _, err := k.RecordOperator(id, "", "plan", []byte("go"), "", "perform_test", "perform_test"); err == nil || !strings.Contains(err.Error(), "may not record the operator's word") {
		t.Fatalf("a session's go: %v", err)
	}
	if got := chainLen(t, k, id); got != n {
		t.Fatalf("a refused go appended %d events", got-n)
	}
	if _, err := k.Attach(id, "go", []byte("go"), "text/markdown; type=go", "perform_test"); err != nil {
		t.Fatal(err)
	}
	var ore *kernel.OperatorRecordError
	if _, err := k.Transition(context.Background(), id, "plan", "perform_test"); !errors.As(err, &ore) {
		t.Fatalf("an agent's go-typed attachment let the story start: %v", err)
	}
}

// AC3: the operator's go (the record verb's path, with no session) is one
// attachment.added event naming the operator, and the session may then present
// the move it names.
func TestDeliveryGoLetsTheStoryStart(t *testing.T) {
	k, id := deliveryKernel(t)
	if _, err := k.RecordOperator(id, "", "plan", []byte("go: start it"), "", "bob", ""); err != nil {
		t.Fatal(err)
	}
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != item.KindAttachmentAdd || last.Actor != "bob" {
		t.Fatalf("last event %s by %s", last.Kind, last.Actor)
	}
	var a item.Attachment
	if err := json.Unmarshal(last.Payload, &a); err != nil {
		t.Fatal(err)
	}
	if !a.Operator || a.Name != "go" || a.MediaType != "text/markdown; type=go" || a.To != "plan" {
		t.Fatalf("the go: %+v", a)
	}
	ops := 0
	for _, at := range chain(t, k, id) {
		if at.Operator {
			ops++
		}
	}
	if ops != 1 {
		t.Fatalf("%d operator records, want 1", ops)
	}
	res, err := k.Transition(context.Background(), id, "plan", "perform_test")
	if err != nil || !res.Moved || res.Item.Status != "plan" {
		t.Fatalf("backlog -> plan after the go: %+v %v", res, err)
	}
}

// AC4: after the go nothing asks the operator before the confirmation point:
// on the forward route the headless drive takes (each state's first non-park
// target), backlog is the only operator state before confirm.
func TestDeliveryForwardRouteAfterTheGo(t *testing.T) {
	w, err := workflow.Parse("../.satelle/workflows/delivery.toml")
	if err != nil {
		t.Fatal(err)
	}
	state := ""
	for name, st := range w.States {
		if st.Start {
			state = name
		}
	}
	route := []string{state}
	for range 10 {
		if state == "confirm" {
			break
		}
		next := ""
		for _, to := range w.Targets(state, "") {
			if s, ok := w.States[to]; ok && !s.Park {
				next = to
				break
			}
		}
		if next == "" {
			t.Fatalf("no forward move from %s", state)
		}
		state = next
		route = append(route, state)
	}
	if want := []string{"backlog", "plan", "in_progress", "integration", "confirm"}; !slices.Equal(route, want) {
		t.Fatalf("forward route %v, want %v", route, want)
	}
	if typ, ok := w.States["backlog"].Operated(); !ok || typ != "go" {
		t.Fatalf("backlog operator type %q %v, want go", typ, ok)
	}
	for _, s := range route[1 : len(route)-1] {
		if typ, ok := w.States[s].Operated(); ok {
			t.Errorf("%s asks the operator (%s) before confirm", s, typ)
		}
	}
}

// AC2 (satelled): the operator gives the go on satelled's Awaiting-you page,
// which lists a story in a start state that is the operator's; the record is
// the operator's, names the move, and the session then presents it.
func TestSatelledGivesTheGo(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	base := startSatelled(t, s)
	s.must("init")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
performer = { operator = "go" }
[states.plan]
[states.done]
terminal = true
[states.cancelled]
terminal = true
[[transition]]
from = ["backlog"]
to = "plan"
[[transition]]
from = ["plan"]
to = "done"
[[transition]]
from = ["*"]
to = "cancelled"
`)
	s.must("validate")
	s.must("project", "add")
	lines := strings.Split(strings.TrimSpace(s.must("project", "partitions")), "\n")
	if len(lines) != 2 {
		t.Fatalf("partitions:\n%s", strings.Join(lines, "\n"))
	}
	key := strings.Fields(lines[1])[0]
	id := s.create("story", "--title", "start me").ID
	page := httpGet(t, base+"/r/"+key+"/operator")
	for _, want := range []string{"start me", "awaiting your word", `value="plan"`, `value="cancelled"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Awaiting-you page lacks %q", want)
		}
	}
	s.env["SATELLE_SESSION"] = "driver"
	if r := s.run("story", "set", id, "--status", "plan"); r.code == 0 || !strings.Contains(r.out+r.err, "go record") {
		t.Fatalf("backlog -> plan without the go: exit %d\n%s%s", r.code, r.out, r.err)
	}
	delete(s.env, "SATELLE_SESSION")
	if code, body := postOperatorWord(t, base, key, id, url.Values{"to": {"plan"}, "word": {"go"}, "retype": {id}}); code != http.StatusSeeOther {
		t.Fatalf("POST: %d %s", code, body)
	}
	recs := operatorRecords(s, id)
	if len(recs) != 1 || recs[0]["to"] != "plan" || recs[0]["name"] != "go" {
		t.Fatalf("operator records: %+v", recs)
	}
	if !strings.Contains(httpGet(t, base+"/r/"+key+"/operator"), "tester chose plan") {
		t.Error("the page does not show the go")
	}
	s.env["SATELLE_SESSION"] = "driver"
	s.must("story", "set", id, "--status", "plan")
	if st := s.get(id).Status; st != "plan" {
		t.Fatalf("status %s", st)
	}
}

// AC5: the session principles say the move out of a delivery story's backlog
// waits for the operator's go, which the session never records, and that the
// session reports the missing go and stops.
func TestSessionStopsForTheGo(t *testing.T) {
	goals := substrate(t, "principles/satelle-agent-goals.md")
	para := ""
	for _, p := range strings.Split(goals, "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(p), "**The workflow is the authority.**") {
			para = strings.Join(strings.Fields(p), " ")
		}
	}
	for _, want := range []string{"satelle <kind> record", "which the session never runs", "only on the operator's go", "reports the missing go and stops"} {
		if !strings.Contains(para, want) {
			t.Errorf("satelle-agent-goals.md's authority paragraph lacks %q", want)
		}
	}
	disc := strings.Join(strings.Fields(substrate(t, "principles/satelle-discovery-criteria.md")), " ")
	for _, want := range []string{"satelle story record <id> --to plan --file F", "report the missing go and stop"} {
		if !strings.Contains(disc, want) {
			t.Errorf("satelle-discovery-criteria.md lacks %q", want)
		}
	}
}
