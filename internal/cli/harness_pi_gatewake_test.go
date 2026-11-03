package cli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gatehandle"
)

// The pi extension's gate-verdict delivery (sty_7ebeda10), traced end to end:
// the RENDERED extension runs under node against a stub of pi's ExtensionAPI and
// calls the REAL hook handlers — this test binary, re-executed as `satelle` —
// over a real handle store. Nothing about the hook is faked, so what is asserted
// is the path a pi session takes: agent_settled -> `satelle hook stopcheck
// --harness pi` -> the handle's exclusive Claim -> pi.sendUserMessage.
//
// This is a stand-in for a live pi run, not one: the pi binary itself is not
// driven. The pi side is the stub in testdata/pi_driver.mjs.

// useRealSatelle governs the rig's repo and puts the real hook on the extension's
// PATH. It returns the handle store the hook resolves, so a test can create and
// finish gates behind it.
func (r *piRig) useRealSatelle(stopWait string) *gatehandle.Store {
	t := r.t
	clearHarnessEnv(t)
	home := t.TempDir()
	cfgPath := filepath.Join(r.repo, ".satelle", "satelle.toml")
	for p, body := range map[string]string{
		cfgPath: "[review]\ngate_create = false\n",
		filepath.Join(r.repo, ".satelle", "agents.toml"): "[executor]\nharness = \"in-loop\"\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nSATELLE_CLI_REEXEC=1 exec '" + exe + "' \"$@\"\n"
	for _, p := range []string{filepath.Join(r.repo, ".satelle", "satelle"), filepath.Join(r.binDir, "satelle")} {
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		"SATELLE_HOME":            home,
		"SATELLE_CONFIG":          cfgPath,
		"SATELLE_SERVER_ENDPOINT": "none",
		stopGateWaitEnv:           stopWait,
	}
	for k, v := range env {
		r.env = append(r.env, k+"="+v)
		if k != stopGateWaitEnv {
			t.Setenv(k, v)
		}
	}
	return gateStoreForTest(t)
}

// finishGateOnConnect returns the address the driver's connect step dials, and
// finishes the gate when it does. The goroutine blocks in Accept, so the finish
// is ordered behind whatever step connects, never behind a timer or a poll and
// so not behind the machine's speed. The listener deadline only bounds a driver
// that never connects: the gate then stays running and the test fails on its
// own assertions.
func (r *piRig) finishGateOnConnect(store *gatehandle.Store, id, verdict string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		r.t.Fatal(err)
	}
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(60 * time.Second))
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
		_ = os.WriteFile(store.VerdictPath(id), []byte(verdict+"\n"), 0o644)
		_ = store.Finish(id, gatehandle.Result{})
	}()
	return ln.Addr().String()
}

func (r *piRig) trace(out piOut) {
	b, _ := json.MarshalIndent(map[string]any{"messages": out.Messages, "notices": out.Notices}, "", "  ")
	r.t.Logf("pi extension trace:\n%s", b)
}

func one(n int) *int { return &n }

// AC1: a verdict that finishes between turns reaches the model as the next
// prompt's additionalContext, and the stop that follows does not send it again.
func TestPiTrace_FinishedBetweenTurnsRidesTheNextPrompt(t *testing.T) {
	r := newPiRig(t)
	store := r.useRealSatelle("1s")
	g := runningGate(t, store, "sty_between")
	finishGateID(t, store, g.ID, "accepted")

	out := r.drive(true,
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "next", "systemPrompt": "SYS"}},
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": true, "hasUI": true}},
	)
	r.trace(out)

	prompt, _ := out.Results[0].Result["systemPrompt"].(string)
	if !strings.Contains(prompt, g.ID) || !strings.Contains(prompt, "accepted") {
		t.Errorf("the verdict did not ride the prompt:\n%s", prompt)
	}
	if len(out.Messages) != 0 {
		t.Errorf("the stop re-sent a verdict the prompt delivered: %+v", out.Messages)
	}
}

// AC3: a non-interactive run that exits on settle sends nothing and waits for
// nothing; the undelivered gate gets an adapter-named limitation, is not marked
// delivered, and the next session still receives its verdict.
func TestPiTrace_NonInteractiveSettleRecordsALimitation(t *testing.T) {
	r := newPiRig(t)
	store := r.useRealSatelle("30s")
	g := runningGate(t, store, "sty_headless")

	start := time.Now()
	out := r.drive(true,
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": true, "hasUI": false}},
	)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("a non-interactive settle waited for its gate (%s)", elapsed)
	}
	r.trace(out)

	if len(out.Messages) != 0 {
		t.Errorf("a non-interactive settle sent a message nothing can receive: %+v", out.Messages)
	}
	if !store.Limited(g.ID) || store.Delivered(g.ID) {
		t.Fatalf("limited=%v delivered=%v — the gate must carry a limitation and stay undelivered", store.Limited(g.ID), store.Delivered(g.ID))
	}
	b, _ := os.ReadFile(filepath.Join(store.Dir(), g.ID, "delivery-limited"))
	if !strings.HasPrefix(string(b), "pi: ") || !strings.Contains(string(b), "non-interactive") {
		t.Errorf("the limitation is not adapter-named: %q", b)
	}

	// The verdict was not lost: the next session's prompt delivers it.
	finishGateID(t, store, g.ID, "accepted")
	out = r.drive(true,
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "later", "systemPrompt": "SYS"}},
	)
	if prompt, _ := out.Results[0].Result["systemPrompt"].(string); !strings.Contains(prompt, g.ID) {
		t.Errorf("the next session did not receive the undelivered verdict:\n%s", prompt)
	}
	if !store.Delivered(g.ID) {
		t.Error("the catch-up did not claim the handle")
	}
}

// sty_79ce4858 AC7: an RPC-mode run reports a UI (pi's ctx.hasUI is true in RPC
// mode) but ctx.mode is "rpc", and the client ends it once it settles, so the
// settle takes the non-interactive path: stopcheck --no-wake, which waits for
// nothing and records the limitation, and no re-prompt.
func TestPiTrace_RPCModeSettleIsNonInteractive(t *testing.T) {
	r := newPiRig(t)
	store := r.useRealSatelle("30s")
	g := runningGate(t, store, "sty_rpc")

	start := time.Now()
	out := r.drive(true,
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": true, "hasUI": true, "mode": "rpc"}},
	)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("an RPC-mode settle waited for its gate (%s)", elapsed)
	}
	r.trace(out)

	if len(out.Messages) != 0 {
		t.Errorf("an RPC-mode settle re-prompted the seat: %+v", out.Messages)
	}
	if !store.Limited(g.ID) || store.Delivered(g.ID) {
		t.Fatalf("limited=%v delivered=%v — stopcheck must have run with --no-wake", store.Limited(g.ID), store.Delivered(g.ID))
	}
}
