package cli

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// The node a drive runs on is named on every record of its stop, so the server can
// tell a remote's park from a person's (sty_aaafe4e2).

func rowPayload(t *testing.T, e ledger.Entry) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("%s payload: %v", e.Kind, err)
	}
	return p
}
