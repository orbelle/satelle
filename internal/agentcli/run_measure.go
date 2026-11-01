package agentcli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// What a run measured about its own work: its turn count and tool calls, or the
// adapter-named reason it reported none. These are measured actuals recorded on
// the ledger, never a bound — satelle hands no harness a turn limit
// (sty_58a7bdfe); a harness may still stop a run at a cap of its own.

// AdapterLabel names the adapter behind a binding's transport and command as
// "<provider> <transport>" — the wording capability reasons and recorded
// unavailables use. A harness satelle has no adapter for is "unknown", never
// assumed to be Claude.
func AdapterLabel(iface, command string) string {
	harness := HarnessUnknown
	if fields := strings.Fields(command); len(fields) > 0 {
		harness = adapterOf(fields[0], fields[1:])
	}
	transport := strings.ToLower(strings.TrimSpace(iface))
	if transport == "" {
		transport = InterfaceCommand
	}
	return harness + " " + transport
}

// applyTurns records the run's own turn count when the output carried one; an
// absent count leaves TurnsAvailable false, never a zero.
func applyTurns(u *UsageResult, n *int) {
	if n == nil {
		return
	}
	u.Turns, u.TurnsAvailable = *n, true
}

// TurnsUnavailableReason is the adapter-named reason a run reported no turn
// count, so a recorded absence is never read as zero turns. A transport that
// never carries one names that, instead of blaming the run's output.
func TurnsUnavailableReason(iface, command string) string {
	label := AdapterLabel(iface, command)
	switch strings.ToLower(strings.TrimSpace(iface)) {
	case "cloud":
		return "cloud: a cloud session is not spawned from a command template, so no turn count is read"
	case InterfaceACP:
		return label + ": the session protocol reports no turn count"
	case InterfacePiRPC:
		return label + ": the run ended before satelle counted its turns"
	}
	return fmt.Sprintf("%s: the run's output carried no turn count (num_turns)", label)
}

// ReportsToolEvents says whether a dispatch with this transport and command
// template emits a tool-start event per tool call, so satelle can count them.
// stream, acp and pi-rpc always do; a command transport does only when its output format
// is a JSONL event stream (stream-json, streaming-json) — its one-shot json
// envelope and plain output carry the answer, never the tool calls. A zero count
// from a transport that reports events is a measured zero; every other transport
// has no count at all (ToolCallsUnavailableReason).
func ReportsToolEvents(iface, command string) bool {
	switch strings.ToLower(strings.TrimSpace(iface)) {
	case InterfaceStream, InterfaceACP, InterfacePiRPC:
		return true
	case "cloud":
		return false
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || strings.EqualFold(fields[0], "in-loop") {
		return false
	}
	for _, format := range flagValues(fields[1:], "--output-format") {
		if f := strings.ToLower(format); f == "stream-json" || f == "streaming-json" {
			return true
		}
	}
	return false
}

// ToolCallsUnavailableReason is the adapter-named reason a dispatch with this
// transport and command template has no tool-call count (ReportsToolEvents
// false), so a recorded absence is never read as zero calls.
func ToolCallsUnavailableReason(iface, command string) string {
	if strings.EqualFold(strings.TrimSpace(iface), "cloud") {
		return "cloud: a cloud session is not spawned from a command template, so no tool events are seen"
	}
	return AdapterLabel(iface, command) + ": the transport emits no tool events (its output format is not stream-json or streaming-json), so tool calls are not counted"
}

// ErrMaxTurns marks a run the harness ended because it reached a turn cap of its
// own. It is its own outcome: the run was cut short, it did not reach a verdict,
// and it is not a reject.
var ErrMaxTurns = errors.New("the harness ended the run at its turn cap (max turns reached)")

// maxTurnsMarkers are the provider spellings of a turn-cap stop, matched
// case-insensitively: grok prints "Max turns reached", and claude's result
// envelope carries subtype "error_max_turns". Nothing outside this file knows
// them.
var maxTurnsMarkers = [][]byte{[]byte("max turns reached"), []byte("error_max_turns")}

// DetectMaxTurns reports whether a run's output says the harness stopped it at
// its turn cap. Callers consult it only for a run that produced no verdict.
func DetectMaxTurns(out []byte) bool {
	lower := bytes.ToLower(out)
	for _, m := range maxTurnsMarkers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}
