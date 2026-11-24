package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestPrintGateResultUnknownModel(t *testing.T) {
	var b strings.Builder
	_ = printGateResult(&b, kernel.GateDryResult{From: "a", To: "b", Result: &gate.Result{Gate: "g", Result: gate.Accept, Binding: "x"}})
	if !strings.Contains(b.String(), "  judge x  "+kernel.UnknownModel+"\n") {
		t.Fatalf("a single judge with no recorded model:\n%s", b.String())
	}
	b.Reset()
	_ = printGateResult(&b, kernel.GateDryResult{From: "a", To: "b", Result: &gate.Result{Gate: "g", Result: gate.Error,
		Members: []gate.Member{{Binding: "y", Result: gate.Error}}}})
	if !strings.Contains(b.String(), "  y  "+kernel.UnknownModel+"  "+gate.Error+"\n") {
		t.Fatalf("a panel member with no recorded model:\n%s", b.String())
	}
}
