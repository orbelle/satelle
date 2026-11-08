package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// A presentation whose only refusal is an error (a judge that could not be
// asked) is refused; its row is v-error, its timeline dot tl-error, and both
// are drawn in the warning colour, not the reject red.
func TestGateErrorIsRefusedAndShownAsAnError(t *testing.T) {
	payload, _ := json.Marshal(mirror.GateRun{Transition: "plan->build", Gate: "scope", Binding: "r1", Result: mirror.GateError, Notes: "dispatch failed: boom"})
	ev := Entry{ID: "e1", Item: "sto_1", Kind: mirror.EventGateRun, At: time.Now(), Payload: payload}
	ps := buildPresentations([]Entry{ev})
	if len(ps) != 1 || ps[0].Outcome != outcomeRefused || ps[0].Verdicts[0].Result != "error" {
		t.Fatalf("presentations %+v", ps)
	}
	if dot := tmplFuncs["evdot"].(func(eventVM) string)(evs(ev)[0]); dot != "tl-error" {
		t.Fatalf("dot %q", dot)
	}
	if !strings.Contains(templatesSrc, `<tr class="verdict v-{{.Result}}">`) {
		t.Fatal("verdict rows no longer carry v-<result>")
	}
	raw, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tr.verdict.v-error .verdict-result { color: var(--warn); font-weight: 600; }",
		"ol.timeline li.tl-error::before { background: var(--warn); }",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
}
