package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// satelle <kind> seat stamps this machine's id and host name on the seat, as
// read back from the item's chain (sto_e127b2e6 AC4).
func TestSeatCommandStampsThisMachine(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "seated", "--criteria", "c")
	must(t, "story", "seat", id, "--session", "s1")
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ledger.OpenExisting(r.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	seats := item.FoldSeats(evs)
	want, ok := config.ReadMachineID(config.GlobalDir())
	host, _ := os.Hostname()
	if !ok || len(seats) != 1 || seats[0].Machine != want || seats[0].Host != host {
		t.Fatalf("seats %+v, want machine %q (%v) host %q", seats, want, ok, host)
	}
}

// The SessionStart hook records the session's hook processes and a later hook
// call settles its owner: the harness both calls share, here the test's
// parent (sto_e127b2e6 AC5). Every hook call settles a missing owner: a prompt,
// a second session start, or a seat command (sto_35c85724 AC2).
func TestHooksRecordAndResolveTheSessionOwner(t *testing.T) {
	dir := isolate(t)
	anc := procgroup.Ancestors(os.Getpid())
	if len(anc) == 0 {
		t.Skip("no process ancestry on this platform")
	}
	must(t, "init")
	payload := fmt.Sprintf(`{"session_id":"s1","cwd":%q}`, dir)
	if out, err := run(t, payload, "hook", "context"); err != nil {
		t.Fatalf("hook context: %v\n%s", err, out)
	}
	home := config.GlobalDir()
	rec, ok := seatlife.Load(home, "s1")
	if !ok || len(rec.Hooks) == 0 || rec.Hooks[0] != anc[0] {
		t.Fatalf("record after session start %+v (%v), want the chain from %+v", rec, ok, anc[0])
	}
	if out, err := run(t, payload, "hook", "prompt"); err != nil {
		t.Fatalf("hook prompt: %v\n%s", err, out)
	}
	rec, _ = seatlife.Load(home, "s1")
	if rec.Owner == nil || *rec.Owner != anc[0] {
		t.Fatalf("owner %+v, want %+v", rec.Owner, anc[0])
	}

	payload = fmt.Sprintf(`{"session_id":"s2","cwd":%q}`, dir)
	for range 2 {
		if out, err := run(t, payload, "hook", "context"); err != nil {
			t.Fatalf("hook context: %v\n%s", err, out)
		}
	}
	rec, _ = seatlife.Load(home, "s2")
	if rec.Owner == nil || *rec.Owner != anc[0] {
		t.Fatalf("owner after two session starts %+v, want %+v", rec.Owner, anc[0])
	}

	payload = fmt.Sprintf(`{"session_id":"s3","cwd":%q}`, dir)
	if out, err := run(t, payload, "hook", "context"); err != nil {
		t.Fatalf("hook context: %v\n%s", err, out)
	}
	id := createID(t, "story", "create", "--title", "seated", "--criteria", "c")
	must(t, "story", "seat", id, "--session", "s3")
	rec, _ = seatlife.Load(home, "s3")
	if rec.Owner == nil || *rec.Owner != anc[0] {
		t.Fatalf("owner after the seat command %+v, want %+v", rec.Owner, anc[0])
	}
}
