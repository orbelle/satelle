//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentstep"
)

// AC1 end to end: the seed the relay hands the consult seat names the coder
// binding and its authored grant verbatim.
func TestSlowReworkConsultSeedIncludesCoderGrant(t *testing.T) {
	_, id := reworkRepoWith(t, "2", "READY", "unused", seedEchoPeer, "")
	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("rework: %v\n%s", err, out)
	}
	rows := reworkMessages(t, id)
	if len(rows) == 0 || rows[0].From != "consultant" {
		t.Fatalf("rows = %+v", rows)
	}
	seed := strings.TrimPrefix(strings.SplitN(rows[0].Body, "\n", 2)[0], "SEED-ECHO ")
	var got struct {
		Rework *agentstep.ReworkContext `json:"rework"`
	}
	if err := json.Unmarshal([]byte(seed), &got); err != nil {
		t.Fatalf("the consult seed is not a payload: %v\n%.300s", err, seed)
	}
	if got.Rework == nil || got.Rework.CoderBinding != "coder" || got.Rework.CoderTools != "Read,Grep,Glob,Edit,Write,Bash(satelle:*)" {
		t.Errorf("rework = %+v; want the coder binding and its grant verbatim", got.Rework)
	}
	if !strings.Contains(rows[0].Body, "DEFERRED <owner>:") {
		t.Error("the seed text must state the DEFERRED contract")
	}
}

// AC2 end to end: the result line and the ledger row carry the deferrals, and
// the row is a ReworkRelayRecord.
func TestSlowReworkLedgerRowCarriesDeferrals(t *testing.T) {
	_, id := reworkRepo(t, "2", "DEFERRED grant: needs docker\nREADY", "unused")
	out, err := runRoot(t, "story", "rework", id)
	if err != nil {
		t.Fatalf("rework: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"converged":true`) || !strings.Contains(out, `"outcome":"converged_with_deferrals"`) || !strings.Contains(out, `"deferred":[{"owner":"grant","objection":"needs docker"}]`) {
		t.Fatalf("result line missing the deferral:\n%s", out)
	}
	led, err := runRoot(t, "ledger", "list", "--story", id, "--kind", "agent_invocation", "--json")
	if err != nil {
		t.Fatalf("ledger: %v\n%s", err, led)
	}
	var rows []struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(led), &rows); err != nil {
		t.Fatalf("parse ledger: %v\n%s", err, led)
	}
	var found *agentstep.ReworkRelayRecord
	for _, r := range rows {
		var rec agentstep.ReworkRelayRecord
		if json.Unmarshal(r.Payload, &rec) == nil && rec.Relay == agentstep.ReworkRelayMarker {
			found = &rec
		}
	}
	if found == nil {
		t.Fatalf("no relay row among %s", led)
	}
	if !found.Converged || found.Outcome != "converged_with_deferrals" || len(found.Deferred) != 1 || found.Deferred[0].Owner != "grant" {
		t.Errorf("relay row = %+v", found)
	}
}
