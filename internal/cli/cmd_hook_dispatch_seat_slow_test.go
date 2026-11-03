//go:build clislow

package cli

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestSlowDispatchedPerformerAttributedToItsOwnLease (sty_8d7d1c45 AC1/AC2): an epic
// still committed at `ready` with its own transition to `in_progress` in flight
// is dispatched a coder. The performer is attributed to its own in-flight lease
// and may edit — whether the epic seat is alone or shares the store with sibling
// seats, whichever of them was acquired first (List orders by seat_key, then
// acquired_at, so acquisition order IS store order), and whatever session id the
// performer presents against the one the seat was stamped with.
func TestSlowDispatchedPerformerAttributedToItsOwnLease(t *testing.T) {
	for _, tc := range []struct {
		name      string
		siblings  int    // committed sibling seats co-held under the epic key
		epicFirst bool   // the epic's seat is acquired before the siblings'
		leaseSID  string // session the seats were stamped with
		perfSID   string // session the performer's hook presents
		seatless  bool   // a performing sibling that holds no seat at all
	}{
		// The reported failure: the epic seat alone, the performer's own session
		// differs from the one the driver stamped on the seat.
		{"epic alone, performer session differs from the seat's", 0, true, "sess-driver", "sess-performer", false},
		{"epic alone, performer unstamped, seat stamped", 0, true, "sess-driver", "", false},
		{"epic acquired first, performer session differs", 2, true, "sess-driver", "sess-performer", false},
		{"epic acquired last, performer session differs", 2, false, "sess-driver", "sess-performer", false},
		{"epic acquired first, session shared", 2, true, "sess-driver", "sess-driver", false},
		{"epic acquired last, session shared", 2, false, "sess-driver", "sess-driver", false},
		{"unstamped, ambiguous by a seatless sibling", 1, false, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, create := attributionRepo(t)
			stubWorktree(t, "/w/driver")
			epic := create("epic", "ready")
			var kids []workitem.Item
			for i := 0; i < tc.siblings; i++ {
				kids = append(kids, create("kid", "in_progress"))
			}
			if tc.seatless {
				create("seatless kid", "in_progress")
			}
			seatKids := func() {
				for i, k := range kids {
					settledEpicSeat(t, db, k, "in_progress", fmt.Sprintf("/w/kid%d", i), tc.leaseSID)
					time.Sleep(2 * time.Millisecond) // time-subject: distinct acquired_at, so the store's acquisition order is the order the case sets up
				}
			}
			if tc.epicFirst {
				inFlightSeat(t, db, epic, "in_progress", "/w/epic", tc.leaseSID)
				time.Sleep(2 * time.Millisecond) // time-subject: distinct acquired_at (see above)
				seatKids()
			} else {
				seatKids()
				inFlightSeat(t, db, epic, "in_progress", "/w/epic", tc.leaseSID)
			}
			setDispatch(t, "coder", "in_progress", epic.ID)

			// The case is only what it claims if the store lists the epic where the
			// case says it is.
			ls, err := db.Leases.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			pos := -1
			for i, l := range ls {
				if l.ItemID == epic.ID {
					pos = i
				}
			}
			wantPos := len(ls) - 1
			if tc.epicFirst {
				wantPos = 0
			}
			if pos != wantPos {
				t.Fatalf("epic listed at %d of %d, want %d (epicFirst=%v)", pos, len(ls), wantPos, tc.epicFirst)
			}

			var before lease.Lease
			if len(kids) > 0 {
				if before, err = db.Leases.Get(context.Background(), kids[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(10 * time.Millisecond) // time-subject: heartbeat separation, so a heartbeat bump by resolveSeats would show as a moved timestamp

			info, engaged, live, err := resolveSeats(true, tc.perfSID)
			if err != nil {
				t.Fatal(err)
			}
			if !engaged || info.ItemID != epic.ID {
				t.Fatalf("performer resolved seat %q (engaged=%v), want its own %s; live=%+v", info.ItemID, engaged, epic.ID, live)
			}
			if !info.InFlight || info.StoryStatus != "ready" {
				t.Errorf("seat must be the in-flight lease over the source status: InFlight=%v StoryStatus=%q", info.InFlight, info.StoryStatus)
			}
			if !hookEditPermitted(info, currentDispatchMarker(), currentRelayMarker()) {
				t.Errorf("a dispatched coder must edit under its own in-flight lease: %+v", info)
			}

			// Attribution never borrows a sibling's lease: its heartbeat is untouched.
			if len(kids) > 0 {
				after, err := db.Leases.Get(context.Background(), kids[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if !after.HeartbeatAt.Equal(before.HeartbeatAt) {
					t.Errorf("sibling heartbeat moved %v -> %v", before.HeartbeatAt, after.HeartbeatAt)
				}
			}
		})
	}
}
