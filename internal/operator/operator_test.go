package operator

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// opsWorkflow is a -> check (the operator's, recording confirmation) with
// moves to done and a, and a plain state b.
func opsWorkflow() map[string]*workflow.Workflow {
	return map[string]*workflow.Workflow{"ops": {
		Name: "ops",
		States: map[string]workflow.State{
			"a":     {Start: true},
			"check": {Performer: &workflow.Performer{Operator: "confirmation"}},
			"b":     {},
			"done":  {Terminal: true},
		},
		Transitions: []workflow.Transition{
			{From: []string{"a"}, To: "check"},
			{From: []string{"a"}, To: "b"},
			{From: []string{"check"}, To: "done"},
			{From: []string{"check"}, To: "a"},
		},
	}}
}

func ptr(s string) *string { return &s }

// newItem creates an item of workflow ops at status on a fresh store.
func newItem(t *testing.T, status string) (*ledger.Store, string) {
	t.Helper()
	l, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "th_00000001"
	if _, err := l.Append(id, item.KindCreated, "op", item.Fields{Kind: ptr("thing"), Title: ptr("t"), Workflow: ptr("ops"), Status: ptr("a")}); err != nil {
		t.Fatal(err)
	}
	if status != "a" {
		move(t, l, id, "a", status)
	}
	return l, id
}

func move(t *testing.T, l *ledger.Store, id, from, to string) {
	t.Helper()
	if _, err := l.Append(id, item.KindTransitioned, "op", item.Move{From: from, To: to}); err != nil {
		t.Fatal(err)
	}
}

func chainLen(t *testing.T, l *ledger.Store, id string) int {
	t.Helper()
	events, err := l.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}

// AC3: a record needs an operator state, a declared move and a non-empty
// word; each refusal leaves the chain unchanged.
func TestRecordNeedsMoveAndWord(t *testing.T) {
	wf := opsWorkflow()
	l, id := newItem(t, "check")
	n := chainLen(t, l, id)
	for _, c := range []struct {
		to, word, want string
	}{
		{"", "ok", "names no move"},
		{"b", "ok", "does not declare"},
		{"nowhere", "ok", "does not declare"},
		{"done", "", "is empty"},
		{"done", " \n\t", "is empty"},
	} {
		if _, _, err := Record(l, wf, id, "", c.to, []byte(c.word), "", "bob"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("to %q word %q: want %q, got %v", c.to, c.word, c.want, err)
		}
	}
	if got := chainLen(t, l, id); got != n {
		t.Fatalf("a refused record changed the chain: %d -> %d", n, got)
	}
	lb, idb := newItem(t, "b")
	if _, _, err := Record(lb, wf, idb, "", "done", []byte("ok"), "", "bob"); err == nil || !strings.Contains(err.Error(), "not the operator's") {
		t.Fatalf("record outside an operator state: %v", err)
	}
	a, _, err := Record(l, wf, id, "", "done", []byte("tried it, works"), "", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Operator || a.To != "done" || a.Name != "confirmation" || a.MediaType != "text/markdown; type=confirmation" || a.Actor != "bob" {
		t.Fatalf("record %+v", a)
	}
	if err := Session("s-1"); err == nil {
		t.Fatal("a session may record")
	}
}

// Latest reads only this visit's records, and the newest wins.
func TestLatestSinceEntry(t *testing.T) {
	wf := opsWorkflow()
	l, id := newItem(t, "check")
	if _, _, err := Record(l, wf, id, "", "a", []byte("back"), "", "bob"); err != nil {
		t.Fatal(err)
	}
	move(t, l, id, "check", "a")
	move(t, l, id, "a", "check")
	events, _ := l.Read(id)
	if _, ok := Latest(events, "confirmation"); ok {
		t.Fatal("an earlier visit's record counted")
	}
	// An attachment of the type without the operator mark is not the word.
	if _, _, err := item.AppendAttachment(l, id, item.Attachment{Name: "confirmation", MediaType: "text/markdown; type=confirmation", To: "done"}, []byte("x"), "agent"); err != nil {
		t.Fatal(err)
	}
	events, _ = l.Read(id)
	if _, ok := Latest(events, "confirmation"); ok {
		t.Fatal("an unmarked attachment counted")
	}
	for _, to := range []string{"a", "done"} {
		if _, _, err := Record(l, wf, id, "", to, []byte("word "+to), "", "bob"); err != nil {
			t.Fatal(err)
		}
	}
	events, _ = l.Read(id)
	rec, ok := Latest(events, "confirmation")
	if !ok || rec.To != "done" {
		t.Fatalf("newest record: %+v %v", rec, ok)
	}
}
