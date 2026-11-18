package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

// A transition on an item with no base names the item and prints no git
// error, moved or refused (sto_f98743d0).
func TestReportTransitionNamesABaselessItem(t *testing.T) {
	it := item.Item{ID: "sto_00000001", Status: "x"}
	var out bytes.Buffer
	moved := kernel.TransitionResult{Item: it, From: "r", To: "x", Moved: true,
		Gates: []gate.Result{{Gate: "final", Result: gate.Accept, Notes: "ok"}}}
	if err := reportTransition(&out, moved, false); err != nil {
		t.Fatalf("moved: %v", err)
	}
	if !strings.Contains(out.String(), "sto_00000001: r -> x") {
		t.Fatalf("moved output %q, want the item named", out.String())
	}
	out.Reset()
	refused := kernel.TransitionResult{Item: it, From: "w", To: "r",
		Gates: []gate.Result{{Gate: "code-ac", Result: gate.Reject, Notes: "no"}}}
	err := reportTransition(&out, refused, false)
	if err == nil || !strings.HasPrefix(err.Error(), "sto_00000001 did not move") {
		t.Fatalf("refused: err %v, want it to name the item", err)
	}
	for _, s := range []string{out.String(), err.Error()} {
		if strings.Contains(s, "fatal:") || strings.Contains(s, "bad revision") {
			t.Fatalf("output %q holds a git error", s)
		}
	}
}
