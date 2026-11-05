package agentcli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/help"
)

// capabilityProbe runs one adapter's real (captured) output, or the output it
// gives when the provider reports nothing, through the code that reads it.
func capabilityProbe(t *testing.T, adapter string) UsageResult {
	t.Helper()
	switch adapter {
	case "claude command":
		_, u := UnwrapUsage(usageFixture(t, "claude_result.json"))
		return u
	case "claude stream":
		var raw map[string]any
		if err := json.Unmarshal(usageFixture(t, "claude_result.json"), &raw); err != nil {
			t.Fatal(err)
		}
		return *usageFromMap(raw)
	case "grok command":
		_, u := UnwrapUsage(usageFixture(t, "grok_json.json"))
		return u
	case "grok acp":
		return *acpUsageFromResult(acpFixtureResult(t), "", "grok acp")
	case "pi pi-rpc":
		return piRPCRecordedUsage(t, piRPCRecordedRun)
	}
	t.Fatalf("no probe for adapter %q", adapter)
	return UsageResult{}
}

func liveOpenable(adapter string) bool {
	commands := map[string][2]string{
		"claude command": {InterfaceCommand, DefaultClaudeCommand},
		"claude stream":  {InterfaceStream, DefaultClaudeStreamCommand},
		"grok command":   {InterfaceCommand, DefaultGrokCommand},
		"grok acp":       {InterfaceACP, "grok agent stdio"},
		"pi pi-rpc":      {InterfacePiRPC, PiRPCCommand},
	}
	c := commands[adapter]
	_, err := OpenerFromBinding(c[0], c[1])
	return err == nil
}

// The table names the adapters satelle supports, each once, and every cell is
// either available (no reason) or unavailable with a reason.
func TestCapabilityTable_ShapeIsExplicit(t *testing.T) {
	want := []string{"claude command", "claude stream", "grok command", "pi pi-rpc"}
	table := CapabilityTable()
	if len(table) != len(want) {
		t.Fatalf("table has %d rows, want %d", len(table), len(want))
	}
	for i, row := range table {
		if row.Adapter != want[i] {
			t.Errorf("row %d = %q, want %q", i, row.Adapter, want[i])
		}
		for j, c := range row.cells() {
			if c.Available && c.Reason != "" {
				t.Errorf("%s / %s: available but carries a reason %q", row.Adapter, capabilityColumns[j], c.Reason)
			}
			if !c.Available && strings.TrimSpace(c.Reason) == "" {
				t.Errorf("%s / %s: unavailable with no reason (a silent zero)", row.Adapter, capabilityColumns[j])
			}
		}
	}
}

// Every usage, cache-split, resolved-model and live-session cell agrees with
// what the code does on that adapter's output. Fails both ways: a table that
// says available where the code records unavailable, and an unavailable that
// the code reports as a bare zero instead of an explicit adapter-named reason.
func TestCapabilityTable_MatchesCode(t *testing.T) {
	for _, row := range CapabilityTable() {
		t.Run(row.Adapter, func(t *testing.T) {
			u := capabilityProbe(t, row.Adapter)

			if got := u.Available; got != row.Usage.Available {
				t.Errorf("usage: table available=%v, code available=%v (%+v)", row.Usage.Available, got, u)
			}
			if !u.Available {
				if !strings.Contains(u.UnavailableReason, "adapter:") {
					t.Errorf("usage unavailable without an adapter-named reason: %q", u.UnavailableReason)
				}
				if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheSplitAvailable {
					t.Errorf("unavailable usage carries numbers: %+v", u)
				}
			}

			if got := u.CacheSplitAvailable; got != row.CacheSplit.Available {
				t.Errorf("cache split: table available=%v, code available=%v (%+v)", row.CacheSplit.Available, got, u)
			}

			modelKnown := !IsModelUnavailable(u.ModelResolved)
			if modelKnown != row.ResolvedModel.Available {
				t.Errorf("resolved model: table available=%v, code recorded %q", row.ResolvedModel.Available, u.ModelResolved)
			}
			if !modelKnown && !strings.HasPrefix(u.ModelResolved, modelUnavailablePrefix+" "+row.Adapter) {
				t.Errorf("resolved model unavailable without the adapter-named reason %q: %q", row.Adapter, u.ModelResolved)
			}

			if got := liveOpenable(row.Adapter); got != row.LiveSession.Available {
				t.Errorf("live session: table available=%v, OpenerFromBinding ok=%v", row.LiveSession.Available, got)
			}
		})
	}
}

// The help topic carries exactly the table the code renders, so the docs cannot
// mark a capability available that the code records unavailable.
func TestCapabilityTable_HelpTopicMatchesCode(t *testing.T) {
	top, ok := help.Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch help topic missing")
	}
	if want := CapabilityTableMarkdown(); !strings.Contains(top.Body, want) {
		t.Errorf("help agent-dispatch does not contain the capability table the code renders; want:\n%s", want)
	}
}
