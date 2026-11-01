package agentcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func usageFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "usage", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// claude command transport: --output-format json envelope. Cache split comes
// from Anthropic's own field names and is available.
func TestUsageAdapter_ClaudeCommand(t *testing.T) {
	_, u := UnwrapUsage(usageFixture(t, "claude_result.json"))
	if !u.Available || u.UnavailableReason != "" {
		t.Fatalf("claude usage = %+v, want available", u)
	}
	if u.InputTokens != 22+11000+2500 || u.OutputTokens != 300 {
		t.Errorf("tokens in/out = %d/%d", u.InputTokens, u.OutputTokens)
	}
	if !u.CacheSplitAvailable || u.FreshInputTokens != 22 || u.CacheCreationInputTokens != 11000 || u.CacheReadInputTokens != 2500 {
		t.Errorf("split = %+v", u)
	}
	// No top-level total_cost_usd on this fixture: cost falls back to the sum
	// of modelUsage's own costUSD (claude-opus-5-5: 0.1).
	if u.CostUSD == nil || !almostEqual(*u.CostUSD, 0.1) || u.CostUnavailableReason != "" {
		t.Errorf("cost = %+v, want 0.1 from modelUsage costUSD", u.CostUSD)
	}
}

// claude stream transport: the same shape arrives on a result event.
func TestUsageAdapter_ClaudeStreamEvent(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(usageFixture(t, "claude_result.json"), &v); err != nil {
		t.Fatal(err)
	}
	evs := commandAdapter{}.Adapt(usageFixture(t, "claude_result.json"), false)
	if len(evs) != 1 || evs[0].Kind != EventUsage || evs[0].Usage == nil {
		t.Fatalf("events = %+v, want one usage event", evs)
	}
	u := evs[0].Usage
	if !u.CacheSplitAvailable || u.CacheReadInputTokens != 2500 || u.ModelResolved != "claude-opus-5-5" {
		t.Errorf("usage = %+v", u)
	}
	if u.CostUSD == nil || !almostEqual(*u.CostUSD, 0.1) || u.CostUnavailableReason != "" {
		t.Errorf("cost = %+v, want 0.1 from modelUsage costUSD", u.CostUSD)
	}
}

// grok command transport, --output-format json (REAL capture): usage is
// snake_case with disjoint counts, so input = fresh + read + write. Pinned to
// the real numbers so a mapper that reads the wrong names (zeros) fails.
func TestUsageAdapter_GrokCommandJSON(t *testing.T) {
	text, u := UnwrapUsage(usageFixture(t, "grok_json.json"))
	if string(text) != "ok" {
		t.Errorf("text = %q", text)
	}
	if !u.Available || u.UnavailableReason != "" {
		t.Fatalf("usage = %+v, want available", u)
	}
	if u.InputTokens != 6435+512 || u.OutputTokens != 24 || u.TotalTokens != 6971 {
		t.Fatalf("tokens in/out/total = %d/%d/%d, want 6947/24/6971", u.InputTokens, u.OutputTokens, u.TotalTokens)
	}
	if !u.CacheSplitAvailable || u.FreshInputTokens != 6435 || u.CacheReadInputTokens != 512 || u.CacheCreationInputTokens != 0 {
		t.Errorf("split = %+v, want fresh=6435 read=512 write=0", u)
	}
	if u.ModelResolved != "grok-4.5-build" || len(u.Models) != 1 || u.Models[0].CacheReadInputTokens != 512 {
		t.Errorf("model = %q %+v", u.ModelResolved, u.Models)
	}
	if u.CostUSD == nil || !almostEqual(*u.CostUSD, 0.004476984) || u.CostUnavailableReason != "" {
		t.Errorf("cost = %+v, want 0.004476984", u.CostUSD)
	}
}

// grokRealCaptureFormat is the --output-format the REAL grok fixture
// (grok_json.json, README provenance line) was captured with. The shipped grok
// presets must use it, or the fixture proves nothing about what the shipped
// command prints (sty_5ebcd6f9).
const grokRealCaptureFormat = "json"

// A grok seat run by the shipped default commands records its turns and tokens: the
// output form they ask for is the one the REAL capture carries them in, and
// UnwrapUsage reads them from that capture. Reverting a preset to `plain` fails.
func TestGrokPresets_MeasureTurnsAndTokensFromRealCapture(t *testing.T) {
	for name, command := range map[string]string{
		"reviewer": DefaultGrokCommand,
		"agent":    DefaultGrokCommand,
	} {
		t.Run(name, func(t *testing.T) {
			formats := flagValues(strings.Fields(command)[1:], "--output-format")
			if len(formats) != 1 || formats[0] != grokRealCaptureFormat {
				t.Fatalf("--output-format = %v, want exactly [%s], the form the real capture carries usage and num_turns in", formats, grokRealCaptureFormat)
			}
			text, u := UnwrapUsage(usageFixture(t, "grok_json.json"))
			if string(text) != "ok" {
				t.Errorf("text = %q, want the envelope's text unwrapped for verdict parsing", text)
			}
			if !u.Available || u.InputTokens != 6435+512 || u.OutputTokens != 24 {
				t.Errorf("tokens = %+v, want the capture's usage", u)
			}
			if !u.TurnsAvailable || u.Turns != 1 {
				t.Errorf("turns = %d available=%v, want the capture's num_turns 1", u.Turns, u.TurnsAvailable)
			}
		})
	}
}

// A run cut off at its turn cap can leave `text` empty while still reporting
// usage and turns: they are kept, and stdout comes back verbatim so the caller
// can see why it ended. The envelope is built here — NOT a capture (see the
// README: no real turn-capped json run is recorded).
func TestUnwrapUsage_GrokEmptyTextKeepsUsageAndTurns(t *testing.T) {
	raw := []byte(`{"text":"","stopReason":"max_turns","usage":{"input_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":7,"total_tokens":107},"num_turns":4}`)
	text, u := UnwrapUsage(raw)
	if string(text) != string(raw) {
		t.Errorf("an empty text returns stdout verbatim, got %q", text)
	}
	if !u.Available || u.InputTokens != 100 || u.OutputTokens != 7 {
		t.Errorf("usage = %+v, want the envelope's tokens", u)
	}
	if !u.TurnsAvailable || u.Turns != 4 {
		t.Errorf("turns = %d available=%v, want 4 true", u.Turns, u.TurnsAvailable)
	}
}

// almostEqual compares dollar figures within float64 rounding noise.
func almostEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// A grok envelope with no usage object (synthetic fixture) records unavailable
// with a reason naming the grok adapter — not zeros.
func TestUsageAdapter_GrokCommandJSONNoUsage(t *testing.T) {
	_, u := UnwrapUsage(usageFixture(t, "grok_json_nousage.json"))
	if u.Available || u.CacheSplitAvailable {
		t.Errorf("usage = %+v, want unavailable", u)
	}
	if !strings.HasPrefix(u.UnavailableReason, "grok adapter:") {
		t.Errorf("reason = %q, want grok adapter reason", u.UnavailableReason)
	}
	if u.ModelResolved != "unavailable: grok command reports no model" {
		t.Errorf("model = %q", u.ModelResolved)
	}
	if u.CostUSD != nil || !strings.Contains(u.CostUnavailableReason, "grok") {
		t.Errorf("cost = %+v, want nil with a grok-named reason", u.CostUSD)
	}
}

// grok streaming-json (REAL capture): the usage line and the final end line
// carry the same snake_case usage; the end line's figure wins.
func TestUsageAdapter_GrokStreamingJSON(t *testing.T) {
	_, u := UnwrapUsage(usageFixture(t, "grok_streaming.jsonl"))
	if !u.Available || u.InputTokens != 6691+256 || u.OutputTokens != 14 || u.TotalTokens != 6961 {
		t.Fatalf("usage = %+v, want in=6947 out=14 total=6961", u)
	}
	if !u.CacheSplitAvailable || u.FreshInputTokens != 6691 || u.CacheReadInputTokens != 256 || u.CacheCreationInputTokens != 0 {
		t.Errorf("split = %+v", u)
	}
	if u.ModelResolved != "grok-4.5-build" {
		t.Errorf("model = %q", u.ModelResolved)
	}
	if u.CostUSD == nil || !almostEqual(*u.CostUSD, 0.004604552) || u.CostUnavailableReason != "" {
		t.Errorf("cost = %+v, want 0.004604552 (last/end line wins)", u.CostUSD)
	}
}

// Plain text / JSONL without any usage line: unavailable, never a measured zero.
// A foreign per-turn carrier no adapter here knows is not read as usage either.
func TestUsageAdapter_NoUsageIsUnavailable(t *testing.T) {
	for _, raw := range []string{
		"plain text verdict",
		"{\"type\":\"turn.started\"}\n{\"type\":\"item.completed\",\"item\":{\"text\":\"x\"}}\n",
		"{\"type\":\"turn.started\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":100,\"cached_input_tokens\":4,\"output_tokens\":5}}\n",
	} {
		_, u := UnwrapUsage([]byte(raw))
		if u.Available || u.InputTokens != 0 || u.CacheSplitAvailable {
			t.Errorf("usage(%q) = %+v, want unavailable", raw, u)
		}
	}
}

// acpFixtureResult returns the session/prompt response (the line with an id)
// from the REAL grok agent stdio capture.
func acpFixtureResult(t *testing.T) json.RawMessage {
	t.Helper()
	for _, l := range strings.Split(string(usageFixture(t, "grok_acp.jsonl")), "\n") {
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(l), &m) == nil && m.ID != nil {
			return m.Result
		}
	}
	t.Fatal("no session/prompt response in grok_acp.jsonl")
	return nil
}

// grok ACP (REAL capture): usage lives at result._meta.usage in camelCase and
// inputTokens INCLUDES cachedReadTokens (19663 = 11855 fresh + 7808 read).
func TestUsageAdapter_ACPResultUsage(t *testing.T) {
	u := acpUsageFromResult(acpFixtureResult(t), "", "grok acp")
	if u == nil || !u.Available || u.InputTokens != 19663 || u.OutputTokens != 32 || u.TotalTokens != 19695 {
		t.Fatalf("usage = %+v, want in=19663 out=32 total=19695", u)
	}
	// The prompt result's _meta.modelId is the id the peer states it ran; the
	// modelUsage key (grok-4.7-build) is only the fallback.
	if u.ModelResolved != "grok-4.7" {
		t.Errorf("ModelResolved = %q, want grok-4.7", u.ModelResolved)
	}
	if !u.CacheSplitAvailable || u.CacheReadInputTokens != 7808 || u.CacheCreationInputTokens != 0 || u.FreshInputTokens != 11855 {
		t.Errorf("split = %+v, want fresh=11855 read=7808 write=0", u)
	}
	if acpUsageFromResult(json.RawMessage(`{"stopReason":"end_turn"}`), "", "grok acp") != nil {
		t.Error("a response without _meta.usage or a model must map to nil, not zeros")
	}
	// costUsdTicks (94540400) maps to cost_usd at 1 tick = 1e-10 USD.
	if u.CostUSD == nil || !almostEqual(*u.CostUSD, 0.00945404) || u.CostUnavailableReason != "" {
		t.Errorf("cost = %+v, want 0.00945404 from costUsdTicks", u.CostUSD)
	}
}

// A grok ACP peer that reports usage but no costUsdTicks records unavailable
// with an adapter-named reason — never a zero.
func TestUsageAdapter_ACPResultCostUnavailable(t *testing.T) {
	u := acpUsageFromResult(json.RawMessage(`{"_meta":{"usage":{"inputTokens":5,"outputTokens":2}}}`), "", "grok acp")
	if u == nil || !u.Available {
		t.Fatalf("usage = %+v, want available", u)
	}
	if u.CostUSD != nil || !strings.Contains(u.CostUnavailableReason, "costUsdTicks") {
		t.Errorf("cost = %+v, want nil with a costUsdTicks reason", u.CostUSD)
	}
}

// A `usage` object with none of the provider's token fields is not a
// measurement: it records unavailable with an adapter-named reason.
func TestUsageAdapter_EmptyUsageObjectIsUnavailable(t *testing.T) {
	// grok headless envelope
	_, u := UnwrapUsage([]byte(`{"text":"ok","usage":{}}`))
	if u.Available || u.CacheSplitAvailable || !strings.HasPrefix(u.UnavailableReason, "grok adapter:") {
		t.Errorf("grok headless usage = %+v, want unavailable with grok reason", u)
	}
	// grok headless envelope whose usage carries only foreign fields
	_, u = UnwrapUsage([]byte(`{"text":"ok","usage":{"reasoning_tokens":5}}`))
	if u.Available || !strings.HasPrefix(u.UnavailableReason, "grok adapter:") {
		t.Errorf("grok headless foreign-only usage = %+v", u)
	}
	// grok ACP: _meta.usage without token fields maps to nil (-> acp reason)
	if acpUsageFromResult(json.RawMessage(`{"_meta":{"usage":{"modelCalls":1}}}`), "", "grok acp") != nil {
		t.Error("ACP usage without token fields must map to nil, not zeros")
	}
	if g := grokUsageFromMap(map[string]any{"modelCalls": 1.0}); g.Available || !strings.HasPrefix(g.UnavailableReason, "acp adapter:") {
		t.Errorf("grokUsageFromMap = %+v, want unavailable with acp reason", g)
	}
	// grok streaming: an empty usage line must not clobber a real one
	_, u = UnwrapUsage([]byte("{\"type\":\"usage\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}\n{\"type\":\"end\",\"usage\":{}}\n"))
	if !u.Available || u.InputTokens != 10 {
		t.Errorf("streaming usage = %+v, want earlier real usage kept", u)
	}
}

func writeUsageACPPeer(t *testing.T, result string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage-acp-peer")
	script := `#!/usr/bin/env python3
import json, sys
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    msg = json.loads(line)
    mid, method = msg.get("id"), msg.get("method")
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[{"id":"cached_token"}]}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"s"}})
    elif method == "session/prompt":
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"decision\":\"accept\"}"}}}})
        send({"jsonrpc":"2.0","id":mid,"result":` + result + `})
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// End to end over the ACP transport: usage on the prompt response reaches the
// caller through RunUsage; a peer without it records the ACP adapter's reason.
func TestUsageAdapter_ACPRunUsage(t *testing.T) {
	cases := []struct {
		name, result string
		wantAvail    bool
	}{
		{"with usage", `{"stopReason":"end_turn","_meta":{"usage":{"inputTokens":500,"outputTokens":20,"cachedReadTokens":100}}}`, true},
		{"without usage", `{"stopReason":"end_turn"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := RunnerFromBinding(InterfaceACP, writeUsageACPPeer(t, c.result)+" stdio")
			if err != nil {
				t.Fatal(err)
			}
			ur, ok := r.(UsageRunner)
			if !ok {
				t.Fatalf("%T does not implement UsageRunner", r)
			}
			out, u, err := ur.RunUsage(context.Background(), Request{Payload: "{}", Model: "grok-4.5"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "accept") {
				t.Errorf("out = %q", out)
			}
			if u.Available != c.wantAvail {
				t.Fatalf("usage = %+v, want available=%v", u, c.wantAvail)
			}
			if c.wantAvail && (u.InputTokens != 500 || u.CacheReadInputTokens != 100 || !u.CacheSplitAvailable) {
				t.Errorf("usage = %+v", u)
			}
			if !c.wantAvail && !strings.HasPrefix(u.UnavailableReason, "acp adapter:") {
				t.Errorf("reason = %q, want acp adapter reason", u.UnavailableReason)
			}
			if !IsModelUnavailable(u.ModelResolved) || !strings.HasPrefix(u.ModelResolved, "unavailable: acp ") {
				t.Errorf("ModelResolved = %q, want an adapter-named reason", u.ModelResolved)
			}
		})
	}
}

// A peer that names its model on the prompt result has it recorded end to end.
func TestUsageAdapter_ACPRunUsageRecordsModelID(t *testing.T) {
	r, err := RunnerFromBinding(InterfaceACP, writeUsageACPPeer(t, `{"stopReason":"end_turn","_meta":{"modelId":"grok-4.7","usage":{"inputTokens":5,"outputTokens":2}}}`)+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	_, u, err := r.(UsageRunner).RunUsage(context.Background(), Request{Payload: "{}", Model: "grok-4.5"})
	if err != nil {
		t.Fatal(err)
	}
	if u.ModelResolved != "grok-4.7" {
		t.Errorf("ModelResolved = %q, want grok-4.7", u.ModelResolved)
	}
}
