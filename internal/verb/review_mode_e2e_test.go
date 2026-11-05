package verb_test

import (
	"encoding/json"
	"testing"
)

// sty_93359e0f end to end, with the real engine as the gater and the real
// verb.PriorVerdicts as its resolver: a fail-fast gate's reject is stored with
// its mode and coverage, and the next presentation is told the prior was partial.

// claudeResult wraps a verdict in the envelope `claude -p --output-format json`
// prints, so the reviewer path reports num_turns the way it does in production.
func claudeResult(t *testing.T, verdict string, turns int) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"result": verdict, "num_turns": turns,
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const stoppedAt = "AC2 has no plan entry"

func failFastReject(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"decision": "reject", "notes": stoppedAt, "reviewed": "AC2",
		"coverage": map[string]any{"complete": false, "stopped_at": stoppedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
