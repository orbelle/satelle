package cli

import (
	"encoding/json"
	"time"
)

// Work-state push holds a story's activity until the code it describes can be
// checked out from the git remote (sty_7361569a), so another machine never sees
// a status it cannot act on. The decision is made at push time from live git
// state, so nothing is dropped: a held row stays behind the cursor and the next
// push re-checks. The hold is mechanism; whether it runs is [sync] hold_unpushed.
// It is local to the push and unrelated to the hosted story hold, which no longer
// exists (sty_40853d02).

// ledgerRowMeta is what the hold needs to know about one collected ledger row,
// parallel to the batch's raw rows: whose it is and where it sits.
type ledgerRowMeta struct {
	storyID   string
	id        string
	seq       int64
	createdAt time.Time
}

// withoutHeld drops the held stories' item rows and ledger rows. It returns the
// rows left, the ledger metadata for what is left, and the earliest updated_at
// among the held item rows (zero when none), which is how far the items cursor
// may safely advance.
func withoutHeld(held map[string]string, items, ledgerRaw []json.RawMessage, rows []ledgerRowMeta) (keepItems, keepLedger []json.RawMessage, earliestHeld time.Time) {
	keepItems = make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		id := rawItemID(raw)
		if _, h := held[id]; !h {
			keepItems = append(keepItems, raw)
			continue
		}
		var w struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		_ = json.Unmarshal(raw, &w)
		if earliestHeld.IsZero() || w.UpdatedAt.Before(earliestHeld) {
			earliestHeld = w.UpdatedAt
		}
	}
	keepLedger = make([]json.RawMessage, 0, len(ledgerRaw))
	for i, raw := range ledgerRaw {
		if i < len(rows) {
			if _, h := held[rows[i].storyID]; h {
				continue
			}
		}
		keepLedger = append(keepLedger, raw)
	}
	return keepItems, keepLedger, earliestHeld
}

// itemsCursorBefore is the items cursor to save when rows were held: the newest
// kept item that predates every held one, else the old cursor. Anything newer is
// re-collected next push; ingest is idempotent by id.
func itemsCursorBefore(kept []json.RawMessage, earliestHeld, old time.Time) time.Time {
	best := old
	for _, raw := range kept {
		var w struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		if json.Unmarshal(raw, &w) == nil && w.UpdatedAt.Before(earliestHeld) && w.UpdatedAt.After(best) {
			best = w.UpdatedAt
		}
	}
	return best
}

// ledgerMarkBeforeHold is the ledger position to save when rows were held: the
// row just before the first held one, so that row and everything after it is
// collected again next push. start is the position the collection began from.
func ledgerMarkBeforeHold(start ledgerMark, held map[string]string, rows []ledgerRowMeta) ledgerMark {
	m := start
	for _, r := range rows {
		if _, h := held[r.storyID]; h {
			break
		}
		if r.createdAt.After(m.createdAt) {
			m.createdAt = r.createdAt
		}
		m.seq = r.seq
		m.anchorID = r.id
	}
	return m
}
