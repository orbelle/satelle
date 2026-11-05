package cli

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// workingOnRows is the working_on rows `ledger list` shows for id on the current
// machine, oldest first.
func workingOnRows(t *testing.T, id string) []verb.WorkingOnPayload {
	t.Helper()
	out, err := runRoot(t, "ledger", "list", "--story", id, "--kind", ledger.KindWorkingOn)
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, out)
	}
	var entries []ledger.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ledger list is not JSON: %v\n%s", err, out)
	}
	var rows []verb.WorkingOnPayload
	for _, e := range entries {
		var p verb.WorkingOnPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("working_on payload: %v\n%s", err, e.Payload)
		}
		rows = append(rows, p)
	}
	return rows
}

func locations(rows []verb.WorkingOnPayload) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Location)
	}
	return out
}
