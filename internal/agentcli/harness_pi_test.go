package agentcli

import (
	"testing"
	"time"
)

// AC1 + AC2: pi is a governed harness with a MEASURED row, not the floor.
// Before this row, FactsFor("pi") fell through to the HarnessUnknown branch:
// a 10s cutoff and "no completion-notification path is wired for this harness".
// That is what stranded twelve gates on 2026-09-29 — the reviewer emitted a
// correct verdict and nothing ever claimed the finished handle.
func TestPiHasAMeasuredHarnessFactsRow(t *testing.T) {
	f := FactsFor(HarnessPi)
	if f.Harness != HarnessPi {
		t.Fatalf("FactsFor(%q).Harness = %q, want %q", HarnessPi, f.Harness, HarnessPi)
	}
	if f.BackgroundCutoff <= DefaultBackgroundCutoff {
		t.Errorf("pi cutoff = %s, want above the unknown-harness floor %s",
			f.BackgroundCutoff, DefaultBackgroundCutoff)
	}
	if f.BackgroundCutoff < GateForegroundBudget {
		t.Errorf("pi cutoff = %s, want at or above GateForegroundBudget %s or the gate still detaches",
			f.BackgroundCutoff, GateForegroundBudget)
	}
	if f.CompletionNotification != yes() {
		t.Errorf("pi CompletionNotification = %v, want wired (%v)", f.CompletionNotification, yes())
	}
	// The basis must name the measurement, not the conservative fallback.
	if got := FactsFor(HarnessUnknown).CutoffBasis; f.CutoffBasis == got {
		t.Errorf("pi CutoffBasis is the unknown-harness fallback %q", got)
	}
}

// AC3: THE REGRESSION. A pi-only session must not need a hand-off, or the
// default `auto` mode detaches the gate at the cutoff and the driver waits
// forever. Assert both directions so this cannot silently revert.
func TestPiOnlySessionDoesNotNeedAHandOff(t *testing.T) {
	if HandoffNeeded([]string{HarnessPi}) {
		t.Errorf("HandoffNeeded([pi]) = true; a pi-only driver would detach every gate and never claim it")
	}
	// Before the row existed, "pi" had no entry and took the floor.
	if got := DefaultBackgroundCutoff; got >= GateForegroundBudget {
		t.Fatalf("floor %s is no longer the regression premise; update this test", got)
	}
	// A mixed set naming grok still hands off — grok's 15s cutoff wins, and the
	// shortest-cutoff rule is unchanged.
	if !HandoffNeeded([]string{HarnessClaude, HarnessPi, HarnessGrok}) {
		t.Error("HandoffNeeded([claude,pi,grok]) = false; grok's 15s cutoff must still force a hand-off")
	}
	if HandoffNeeded([]string{HarnessClaude, HarnessPi}) {
		t.Error("HandoffNeeded([claude,pi]) = true; neither cutoff is below the budget")
	}
}

// AC6: detection. PI_CODING_AGENT is the primary marker; the PI_SESSION_ prefix
// catches a session exported without it, and a plain shell stays unknown.
func TestPiSessionMarkers(t *testing.T) {
	piEnviron := []string{"PI_CODING_AGENT=true", "PI_SESSION_ID=01a0ea12", "PATH=/usr/bin"}
	if !DetectSessionHarnesses(piEnviron)[HarnessPi] {
		t.Error("PI_CODING_AGENT=true was not detected as pi")
	}
	prefixOnly := []string{"PI_SESSION_FILE=/home/x/.pi/s.jsonl"}
	if !DetectSessionHarnesses(prefixOnly)[HarnessPi] {
		t.Error("PI_SESSION_ prefix alone was not detected as pi")
	}
	// The explicit marker off, prefix still on.
	off := []string{"PI_CODING_AGENT=0", "PI_SESSION_ID=x"}
	if !DetectSessionHarnesses(off)[HarnessPi] {
		t.Error("PI_CODING_AGENT=0 with a PI_SESSION_ id should still detect pi via the prefix")
	}
	if got := DetectSessionHarnesses([]string{"PATH=/usr/bin", "HOME=/root"}); got[HarnessPi] {
		t.Errorf("a plain shell detected as pi: %v", got)
	}
}

// InLoopHarnessFromEnv must name pi, and must not let pi shadow a real claude
// or grok session that happens to carry pi's variables in its environment.
func TestInLoopHarnessFromEnvNamesPi(t *testing.T) {
	h, ok := InLoopHarnessFromEnv([]string{"PI_CODING_AGENT=true"})
	if !ok || h != HarnessPi {
		t.Errorf("InLoopHarnessFromEnv = (%q, %v), want (%q, true)", h, ok, HarnessPi)
	}
	// claude and grok keep precedence; pi is checked last.
	h, ok = InLoopHarnessFromEnv([]string{"CLAUDECODE=1", "PI_CODING_AGENT=true"})
	if !ok || h != HarnessClaude {
		t.Errorf("claude+pi = (%q, %v), want claude to win", h, ok)
	}
	h, ok = InLoopHarnessFromEnv([]string{"GROK_AGENT=1", "PI_CODING_AGENT=true"})
	if !ok || h != HarnessGrok {
		t.Errorf("grok+pi = (%q, %v), want grok to win", h, ok)
	}
}

// AC5: nothing about the existing harnesses moved.
func TestClaudeAndGrokRowsAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		harness string
		cutoff  time.Duration
	}{
		{HarnessClaude, 2 * time.Minute},
		{HarnessGrok, 15 * time.Second},
	} {
		if got := FactsFor(tc.harness).BackgroundCutoff; got != tc.cutoff {
			t.Errorf("%s cutoff = %s, want %s", tc.harness, got, tc.cutoff)
		}
		if got := FactsFor(tc.harness).CompletionNotification; got != yes() {
			t.Errorf("%s CompletionNotification = %v, want wired", tc.harness, got)
		}
	}
	if n := len(HarnessFactsTable()); n != 3 {
		t.Errorf("HarnessFactsTable has %d rows, want 3 (claude, grok, pi)", n)
	}
}
