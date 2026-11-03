//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// The shapes every other harness gets are unchanged: a gate still running still
// blocks, and its answer carries no pending list.
func TestSlowSettleModeIsOnlyForAHarnessThatCannotHoldItsStop(t *testing.T) {
	for _, harness := range []string{agentcli.HarnessClaude, agentcli.HarnessGrok, "", "mystery"} {
		t.Run("harness="+harness, func(t *testing.T) {
			store := stopWakeRepo(t, "50ms")
			settleHarness(t, harness, false)
			m := runningGate(t, store, "sty_held")
			blk, ok := stopOnce(t, "{}")
			if !ok || !strings.Contains(blk.Reason, m.ID) || !strings.Contains(blk.Reason, "still running") {
				t.Fatalf("a stop that can hold must still block on a running gate: %+v ok=%v", blk, ok)
			}
		})
	}
	var note strings.Builder
	if err := emitStopNote(&note, "n"); err != nil || strings.TrimSpace(note.String()) != `{"systemMessage":"n"}` {
		t.Errorf("the allow-with-note shape changed for every other harness: %q", note.String())
	}
}
