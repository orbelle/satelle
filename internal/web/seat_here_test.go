package web

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// seatsHere makes seats engaged here (sto_e127b2e6): home is a fresh satelle
// home with a machine id, each seat's session is owned by this live test
// process, and each seat row records this machine. Set SATELLE_HOME to home
// after mirrorWith, which isolates a home of its own.
func seatsHere(t *testing.T, seats []json.RawMessage) (out []json.RawMessage, home string) {
	t.Helper()
	home = t.TempDir()
	machine, err := config.MachineID(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range seats {
		var s mirror.Seat
		if err := json.Unmarshal(r, &s); err != nil {
			t.Fatal(err)
		}
		if err := seatlife.Own(home, s.Session, procgroup.Self()); err != nil {
			t.Fatal(err)
		}
		s.Machine = machine
		out = append(out, raw(s))
	}
	return out, home
}
