package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/gatehandle"
)

// A harness whose stop cannot hold its session (agentcli.HarnessFacts
// .SettleNotifyOnly, sty_7ebeda10) is told a gate's verdict once, and is never
// re-prompted for a gate still running: the Stop hook waits once, blocks only
// with a verdict, and answers a gate still going with an allow that names it.

// settleHarness selects the harness (and --no-wake) the stopcheck flags would.
func settleHarness(t *testing.T, harness string, noWake bool) {
	t.Helper()
	prevH, prevN := hookHarnessFlag, hookNoWakeFlag
	hookHarnessFlag, hookNoWakeFlag = harness, noWake
	t.Cleanup(func() { hookHarnessFlag, hookNoWakeFlag = prevH, prevN })
}

// stopRaw runs the Stop hook once and returns its single output line.
func stopRaw(t *testing.T, raw string) string {
	t.Helper()
	var out strings.Builder
	if err := runHookStopcheck([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

func finishGateID(t *testing.T, store *gatehandle.Store, id, verdict string) {
	t.Helper()
	if err := os.WriteFile(store.VerdictPath(id), []byte(verdict+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(id, gatehandle.Result{}); err != nil {
		t.Fatal(err)
	}
}

// AC2: a gate still running at the end of the one wait is not a block, however
// many times the session settles; the stop is allowed and the gate is named.
func TestSettleStopAllowsAGateStillRunning(t *testing.T) {
	store := stopWakeRepo(t, "50ms")
	settleHarness(t, agentcli.HarnessPi, false)
	m := runningGate(t, store, "sty_slow")

	for settle := 1; settle <= 3; settle++ {
		start := time.Now()
		line := stopRaw(t, `{"stop_hook_active":true}`)
		if settle == 1 && time.Since(start) < 40*time.Millisecond {
			t.Errorf("the hook did not wait out its one bound (%s)", time.Since(start))
		}
		var allow agentcli.StopAllow
		if err := json.Unmarshal([]byte(line), &allow); err != nil || strings.Contains(line, `"decision"`) {
			t.Fatalf("settle %d: a still-running gate must allow the stop, not block it: %q", settle, line)
		}
		if len(allow.Pending) != 1 || allow.Pending[0] != m.ID || !strings.Contains(allow.SystemMessage, "still running") {
			t.Fatalf("settle %d: the allow does not name the pending gate: %+v", settle, allow)
		}
	}
	if store.Delivered(m.ID) {
		t.Error("a gate still running was claimed")
	}
}

// AC1: a finished gate's verdict is the stop's one block; the second settle,
// and a prompt hook after it, have nothing left to deliver.
func TestSettleStopDeliversTheVerdictOnce(t *testing.T) {
	store := stopWakeRepo(t, "0s")
	settleHarness(t, agentcli.HarnessPi, false)
	m := runningGate(t, store, "sty_done")
	finishGateID(t, store, m.ID, "accepted plan→in_progress")

	first, ok := stopOnce(t, "{}")
	if !ok || !strings.Contains(first.Reason, m.ID) || !strings.Contains(first.Reason, "accepted plan→in_progress") {
		t.Fatalf("the finished gate's verdict was not the block: %+v ok=%v", first, ok)
	}
	if line := stopRaw(t, `{"stop_hook_active":true}`); line != "" {
		t.Errorf("a second settle repeated or noted something: %q", line)
	}
	if text := gateDeliveryFor(0); text != "" {
		t.Errorf("the prompt hook delivered a verdict the stop already sent: %q", text)
	}
}

// AC1: a verdict that finished between turns is claimed by the prompt hook's
// catch-up, and then not sent again by the stop.
func TestSettleStopDoesNotRepeatACaughtUpVerdict(t *testing.T) {
	store := stopWakeRepo(t, "0s")
	settleHarness(t, agentcli.HarnessPi, false)
	m := runningGate(t, store, "sty_caught")
	finishGateID(t, store, m.ID, "accepted")

	if text := gateDeliveryFor(0); !strings.Contains(text, m.ID) {
		t.Fatalf("the prompt hook did not catch the verdict up: %q", text)
	}
	if line := stopRaw(t, "{}"); line != "" {
		t.Errorf("the stop sent a verdict the prompt already delivered: %q", line)
	}
}

// AC3: a run that ends at settle waits for nothing, claims nothing, and records
// the adapter-named limitation once on each undelivered handle.
func TestSettleNoWakeRecordsALimitationAndDeliversNothing(t *testing.T) {
	store := stopWakeRepo(t, "30s")
	settleHarness(t, agentcli.HarnessPi, true)
	running := runningGate(t, store, "sty_running")
	done := runningGate(t, store, "sty_finished")
	finishGateID(t, store, done.ID, "accepted")

	start := time.Now()
	line := stopRaw(t, "{}")
	if time.Since(start) > 5*time.Second {
		t.Errorf("a run that ends at settle waited (%s)", time.Since(start))
	}
	var allow agentcli.StopAllow
	if err := json.Unmarshal([]byte(line), &allow); err != nil || strings.Contains(line, `"decision"`) {
		t.Fatalf("a run that ends at settle must not block: %q", line)
	}
	if len(allow.Limited) != 2 || !strings.Contains(allow.SystemMessage, "pi:") || len(allow.Pending) != 0 {
		t.Fatalf("both handles must be named limited with the pi limitation: %+v", allow)
	}
	for _, id := range []string{running.ID, done.ID} {
		if !store.Limited(id) || store.Delivered(id) {
			t.Errorf("%s: limited=%v delivered=%v — must be limited and still undelivered", id, store.Limited(id), store.Delivered(id))
		}
		b, _ := os.ReadFile(store.Dir() + "/" + id + "/delivery-limited")
		if !strings.HasPrefix(string(b), "pi: ") {
			t.Errorf("%s: the recorded limitation is not adapter-named: %q", id, b)
		}
	}
	// Recorded once: the next settle has nothing new to say.
	if line := stopRaw(t, "{}"); line != "" {
		t.Errorf("a second settle re-recorded the limitation: %q", line)
	}
	// The verdict is still owed: the next session's prompt delivers it.
	if text := gateDeliveryFor(0); !strings.Contains(text, done.ID) || !strings.Contains(text, "accepted") {
		t.Errorf("the catch-up lost the verdict of a limited handle: %q", text)
	}
}
