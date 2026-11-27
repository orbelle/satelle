package cli

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/ledgerrepl"
)

func TestPassSyncedRefusesAnyUnsynced(t *testing.T) {
	if !passSynced(ledgerrepl.SyncResult{})("que_release") {
		t.Fatal("a clean pass refused a queue")
	}
	cases := map[string]ledgerrepl.SyncResult{
		"unsynced":      {Unsynced: []ledgerrepl.ItemStatus{{Item: "sto_a"}}},
		"push refused":  {Push: ledgerrepl.Result{Refused: []string{"sto_a refused"}}},
		"pull refused":  {Pull: ledgerrepl.Result{Refused: []string{"sto_a refused"}}},
		"push diverged": {Push: ledgerrepl.Result{Diverged: []ledgerrepl.Divergence{{Item: "sto_a"}}}},
		"pull diverged": {Pull: ledgerrepl.Result{Diverged: []ledgerrepl.Divergence{{Item: "sto_a"}}}},
	}
	for name, res := range cases {
		synced := passSynced(res)
		for _, id := range []string{"sto_a", "que_release", "exe_b"} {
			if synced(id) {
				t.Errorf("%s: %s counted in sync", name, id)
			}
		}
	}
}
