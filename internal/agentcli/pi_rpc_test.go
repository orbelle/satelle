package agentcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// piRPCRecordedRun is a pi RPC run in the record shapes pi's docs define (json.md,
// rpc-commands.md) and the numbers sty_79ce4858's 2026-10-28 proof run returned:
// one grep tool call over two turns, then the stats.
var piRPCRecordedRun = []string{
	`{"id":"satelle-prompt","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`,
	`{"type":"agent_start"}`,
	`{"type":"turn_start"}`,
	`{"type":"tool_execution_start","toolCallId":"c1","toolName":"grep","args":{"pattern":"x"}}`,
	`{"type":"tool_execution_end","toolCallId":"c1","toolName":"grep","result":{},"isError":false}`,
	`{"type":"turn_start"}`,
	`{"type":"message_end","message":{"role":"assistant","provider":"cursor","model":"claude-opus-5-5-fast","content":[{"type":"text","text":"the answer"}]}}`,
	`{"type":"agent_end","messages":[],"willRetry":false}`,
	`{"type":"agent_settled","aborted":false}`,
	`{"id":"satelle-stats","type":"response","command":"get_session_stats","success":true,"data":{"tokens":{"input":9926,"output":450,"cacheRead":10446,"cacheWrite":74,"total":20896},"cost":0.399,"toolCalls":1}}`,
}

// piRPCRecordedUsage feeds recorded pi records through the run's own handler and
// returns the usage it reports.
func piRPCRecordedUsage(t *testing.T, lines []string) UsageResult {
	t.Helper()
	run := &piRPCRun{stdin: &bytes.Buffer{}}
	for _, l := range lines {
		if err := run.handle([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	return run.usage()
}

// AC6: building a pi-rpc runner refuses a template that would not start cold,
// naming every missing flag. RunnerFromBinding is what dispatch and validate
// build with; the refusal returns no runner, so nothing can be started.
func TestNewPiRPCRunnerRefusesAWarmTemplate(t *testing.T) {
	if _, err := RunnerFromBinding(InterfacePiRPC, PiRPCCommand); err != nil {
		t.Fatalf("the canonical cold template is refused: %v", err)
	}
	warm := strings.Replace(PiRPCCommand, " --no-session", "", 1)
	r, err := RunnerFromBinding(InterfacePiRPC, warm)
	if err == nil || r != nil || !strings.Contains(err.Error(), "--no-session") {
		t.Fatalf("runner=%v err=%v, want a refusal naming --no-session", r, err)
	}
	bare := "pi --tools {tools} --model {model}"
	_, err = RunnerFromBinding(InterfacePiRPC, bare)
	if err == nil {
		t.Fatal("a template with no cold flags was accepted")
	}
	for _, f := range append([]string{"--mode rpc", "--system-prompt {system}"}, piRPCColdFlags...) {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("refusal %q does not name %s", err, f)
		}
	}
	if _, err := RunnerFromBinding(InterfacePiRPC, strings.Replace(PiRPCCommand, "pi ", "claude ", 1)); err == nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("a non-pi command must be refused naming its adapter: %v", err)
	}
}

// AC4: tokens, cache split and cost come from get_session_stats, turns from the
// run's turn_start events and the model from pi's own message_end.
func TestPiRPCUsageFromStatsAndEvents(t *testing.T) {
	u := piRPCRecordedUsage(t, piRPCRecordedRun)
	if !u.Available || !u.CacheSplitAvailable || u.Adapter != HarnessPi {
		t.Fatalf("usage = %+v, want available pi usage with a cache split", u)
	}
	if u.FreshInputTokens != 9926 || u.OutputTokens != 450 || u.CacheReadInputTokens != 10446 || u.CacheCreationInputTokens != 74 {
		t.Errorf("tokens = %+v", u)
	}
	if u.InputTokens != 9926+10446+74 || u.TotalTokens != 9926+10446+74+450 {
		t.Errorf("input/total = %d/%d", u.InputTokens, u.TotalTokens)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.399 {
		t.Errorf("cost = %v, want 0.399", u.CostUSD)
	}
	if !u.TurnsAvailable || u.Turns != 2 {
		t.Errorf("turns = %d (available %v), want 2", u.Turns, u.TurnsAvailable)
	}
	if u.ModelResolved != "cursor/claude-opus-5-5-fast" {
		t.Errorf("model = %q, want pi's own provider/model", u.ModelResolved)
	}
}

// A run whose messages name no model records the adapter-named marker, never the
// configured alias.
func TestPiRPCUsageWithoutAModelNamesTheAdapter(t *testing.T) {
	var lines []string
	for _, l := range piRPCRecordedRun {
		if !strings.Contains(l, `"message_end"`) {
			lines = append(lines, l)
		}
	}
	u := piRPCRecordedUsage(t, lines)
	if !IsModelUnavailable(u.ModelResolved) || !strings.Contains(u.ModelResolved, piRPCLabel) {
		t.Errorf("model = %q, want the %s no-model marker", u.ModelResolved, piRPCLabel)
	}
}

// Stats that never arrive are a pi-named unavailable, not a zero.
func TestPiRPCUsageWithoutStatsIsUnavailable(t *testing.T) {
	u := piRPCRecordedUsage(t, piRPCRecordedRun[:len(piRPCRecordedRun)-1])
	if u.Available || !strings.HasPrefix(u.UnavailableReason, "pi adapter:") || u.InputTokens != 0 {
		t.Errorf("usage = %+v, want a pi-named unavailable", u)
	}
}

// AC7: a dialog request is answered with cancelled under its own id; a
// fire-and-forget one gets no reply.
func TestPiRPCDialogRequestIsCancelled(t *testing.T) {
	var sent bytes.Buffer
	var events []Event
	run := &piRPCRun{stdin: &sent, onEvent: func(ev Event) { events = append(events, ev) }}
	for _, l := range []string{
		`{"type":"extension_ui_request","id":"ui-1","method":"notify","message":"hi"}`,
		`{"type":"extension_ui_request","id":"ui-2","method":"confirm","title":"Allow?"}`,
	} {
		if err := run.handle([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(sent.Bytes()), &got); err != nil {
		t.Fatalf("want exactly one reply, got %q: %v", sent.String(), err)
	}
	if got["type"] != "extension_ui_response" || got["id"] != "ui-2" || got["cancelled"] != true {
		t.Errorf("reply = %v", got)
	}
	if len(events) != 1 || events[0].Kind != EventInteractiveDenied {
		t.Errorf("events = %+v, want one interactive-denied", events)
	}
}

// A prompt pi refuses ends the run with pi's own error.
func TestPiRPCRefusedPromptIsAnError(t *testing.T) {
	run := &piRPCRun{stdin: &bytes.Buffer{}}
	err := run.handle([]byte(`{"id":"satelle-prompt","type":"response","command":"prompt","success":false,"error":"no model"}`))
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("err = %v", err)
	}
}

// The capability output counts pi-rpc tool calls; a live session is refused.
func TestPiRPCCapabilities(t *testing.T) {
	if !ReportsToolEvents(InterfacePiRPC, PiRPCCommand) {
		t.Error("pi-rpc reports tool events")
	}
	var row *AdapterCapabilities
	for _, r := range CapabilityTable() {
		if r.Adapter == piRPCLabel {
			row = &r
		}
	}
	if row == nil {
		t.Fatal("no pi pi-rpc capability row")
	}
	if !strings.Contains(CapabilityTableMarkdown(), "| pi pi-rpc |") {
		t.Error("the rendered capability table has no pi pi-rpc row")
	}
	if _, err := OpenerFromBinding(InterfacePiRPC, PiRPCCommand); !errors.Is(err, ErrNotLiveCapable) {
		t.Errorf("opener err = %v, want ErrNotLiveCapable", err)
	}
}

// The reviewer isolation path knows a pi-rpc runner: it is a recognised adapter,
// and its offered tools are the --tools list it spawns with.
func TestPiRPCReviewerIsolation(t *testing.T) {
	r, err := RunnerFromBinding(InterfacePiRPC, PiRPCCommand)
	if err != nil {
		t.Fatal(err)
	}
	iso := DescribeReviewer(r, Request{ReadOnly: true, AllowedTools: "read,grep,find,ls"})
	if iso.OfferedSource != OfferedSourceFlag || strings.Join(iso.OfferedTools, ",") != "read,grep,find,ls" {
		t.Errorf("isolation = %+v, want the --tools list", iso)
	}
	if gaps := PreflightRunner(r, "read,grep,find,ls"); len(gaps) != 0 {
		t.Errorf("gaps = %+v, want none for --tools equal to the grant", gaps)
	}
}
