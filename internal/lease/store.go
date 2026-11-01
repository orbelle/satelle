// Package lease persists an EXPLICIT, OWNED engagement seat for satelle work
// items (sty_8426b9c0). Engagement is no longer only derived from committed
// status: a lease is acquired at the START of an engaging transition (before
// gate+dispatch) so concurrent engages and edit/commit gates see the seat
// immediately. The DOT shape still defines WHICH states are engaging; the lease
// records WHO holds the seat NOW.
//
// Atomicity: store.Open uses MaxOpenConns(1) + _txlock=immediate, so a
// check-and-insert inside one BEGIN IMMEDIATE transaction is single-flight
// across concurrent satelle processes.
package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// HeartbeatTTL is how long a lease may sit without a heartbeat before a
// colliding acquire treats it as stale (process death). Must exceed a long
// synchronous dispatch (reviewer + planner can run many minutes).
const HeartbeatTTL = 30 * time.Minute

// InFlightTTL is a backstop for in_flight aging when the transitioning pid is
// still alive (or unknown). The primary self-heal for a dead mid-transition
// process is InFlightPid + pidAlive (sty_bf797fa9 AC3) — not this TTL. Must
// exceed a long multi-gate dispatch (same order as HeartbeatTTL).
const InFlightTTL = HeartbeatTTL

// ErrNotFound is returned when Get misses.
var ErrNotFound = errors.New("lease: not found")

// ErrNotOwner is returned when Release is attempted by a non-owner.
var ErrNotOwner = errors.New("lease: not owner")

const schema = `
CREATE TABLE IF NOT EXISTS engagement_lease (
    item_id            TEXT PRIMARY KEY,
    kind               TEXT NOT NULL DEFAULT '',
    story_seat         INTEGER NOT NULL DEFAULT 0,
    seat_key           TEXT NOT NULL DEFAULT '',
    worktree           TEXT NOT NULL DEFAULT '',
    owner              TEXT NOT NULL,
    state              TEXT NOT NULL DEFAULT '',
    acquired_at        TEXT NOT NULL,
    heartbeat_at       TEXT NOT NULL,
    stop_requested_by  TEXT NOT NULL DEFAULT '',
    stop_reason        TEXT NOT NULL DEFAULT '',
    in_flight          INTEGER NOT NULL DEFAULT 0,
    in_flight_at       TEXT NOT NULL DEFAULT '',
    in_flight_pid      INTEGER NOT NULL DEFAULT 0,
    activity_label     TEXT NOT NULL DEFAULT '',
    activity_index     INTEGER NOT NULL DEFAULT 0,
    activity_total     INTEGER NOT NULL DEFAULT 0,
    activity_at        TEXT NOT NULL DEFAULT '',
    session_id         TEXT NOT NULL DEFAULT '',
    tree_scoped        INTEGER NOT NULL DEFAULT 0,
    exclusive          INTEGER NOT NULL DEFAULT 0,
    activity_agent       TEXT NOT NULL DEFAULT '',
    activity_model       TEXT NOT NULL DEFAULT '',
    activity_pid         INTEGER NOT NULL DEFAULT 0,
    activity_event_label TEXT NOT NULL DEFAULT '',
    activity_event_at    TEXT NOT NULL DEFAULT '',
    activity_event_count INTEGER NOT NULL DEFAULT 0,
    activity_gates       INTEGER NOT NULL DEFAULT 0,
    activity_passed      INTEGER NOT NULL DEFAULT 0,
    activity_failed      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_engagement_lease_seat ON engagement_lease(story_seat);
`

// leaseCols is the column list every SELECT shares — one source so a new column
// cannot reach some read paths and not others.
const leaseCols = `item_id, kind, story_seat, seat_key, worktree, owner, state, acquired_at, heartbeat_at, stop_requested_by, stop_reason, in_flight, in_flight_at, in_flight_pid, activity_label, activity_index, activity_total, activity_at, session_id, tree_scoped, exclusive, activity_agent, activity_model, activity_pid, activity_event_label, activity_event_at, activity_event_count, activity_gates, activity_passed, activity_failed`

// Lease is one engagement seat row.
type Lease struct {
	ItemID    string
	Kind      string
	StorySeat bool
	// SeatKey is the ARBITRATION key this lease claimed the story seat under
	// (sty_c098dc2d). Two live seat holders coexist only when their keys are
	// equal and non-empty. The caller chooses the key policy — the item's own id
	// (single occupancy) or its parent id (siblings of one epic). Empty on
	// pre-migration rows, which therefore behave as singletons.
	SeatKey string
	// Worktree is the git working tree this engagement was anchored to (git
	// toplevel at acquire). Two live seat leases never share a non-empty tree —
	// they would attribute each other's edits. Empty when git was unavailable or
	// on pre-migration rows; empty never participates in tree arbitration.
	Worktree string
	// TreeScoped and Exclusive are the claim's co-hold policy (sty_69ec7fbc),
	// recorded so a later claimant can judge this holder without knowing the
	// mode: see CoHolds. False on pre-migration rows, which therefore keep
	// today's equal-key rule.
	TreeScoped      bool
	Exclusive       bool
	Owner           string
	State           string
	AcquiredAt      time.Time
	HeartbeatAt     time.Time
	StopRequestedBy string
	StopReason      string
	InFlight        bool // transition in progress (gate+dispatch not yet committed)
	// InFlightAt is when in_flight was last set to 1. Zero when not in flight
	// or on pre-migration rows. Used by EffectiveInFlight (not HeartbeatAt —
	// hooks refresh heartbeat while the seat is held).
	InFlightAt time.Time
	// InFlightPid is the OS pid of the process that marked in_flight (the
	// transitioning CLI). 0 when unknown / not in flight. Distinct from Owner
	// (which is deliberately pid-less local@host so sequential CLI invocations
	// share the seat). When non-zero and dead on this host, EffectiveInFlight
	// is false immediately — the process that owned the transition is gone.
	InFlightPid int
	// SessionID is the acquiring session (SATELLE_SESSION or a published
	// harness session_id). Empty when Acquire could not establish one.
	// Stamped only at Acquire.
	SessionID string
	// Activity* describe the current gate/phase while in_flight (sty_598a8e1b).
	// Empty when not in flight or when progress has not been stamped yet.
	ActivityLabel string
	ActivityIndex int
	ActivityTotal int
	ActivityAt    time.Time
	// ActivityAgent/Model/Pid/EventLabel/EventAt/EventCount extend the SAME
	// activity record with in-flight DISPATCH metadata (sty_752c4ef2) — which
	// agent binding and model are running, the OS pid, and the last REAL event
	// (tool start/end, message, usage) seen. Refreshed via SetActivityDetail
	// on real events, throttled, not only once per phase like ActivityLabel.
	// Empty/zero when no dispatch has stamped detail yet. No second table.
	ActivityAgent      string
	ActivityModel      string
	ActivityPid        int
	ActivityEventLabel string
	ActivityEventAt    time.Time
	ActivityEventCount int
	// ActivityGates marks an activity stamped by a gate round, whose
	// ActivityPassed and ActivityFailed count the gates of that round that have
	// returned an accept or a reject so far (sty_216d5205). False and zero for
	// every other phase (a performer dispatch, a summariser).
	ActivityGates  bool
	ActivityPassed int
	ActivityFailed int
}

// Activity is the phase stamp SetActivity writes and EffectiveActivity reads:
// the phase label, its 1-based index in a set of Total, and — on a gate round's
// stamp (Gates) — how many of the round's gates have passed and failed so far.
type Activity struct {
	Label  string
	Index  int
	Total  int
	Gates  bool
	Passed int
	Failed int
}

// ActivityDetail is the in-flight dispatch metadata SetActivityDetail writes
// and EffectiveActivityDetail reads (sty_752c4ef2).
type ActivityDetail struct {
	Agent      string
	Model      string
	Pid        int
	EventLabel string
	EventAt    time.Time
	EventCount int
}

// Outcome of Acquire.
type Outcome int

const (
	// OutcomeAcquired — this call inserted a new lease.
	OutcomeAcquired Outcome = iota
	// OutcomeAlreadyHeld — same owner holds a settled lease; sequential step may continue.
	OutcomeAlreadyHeld
	// OutcomeInFlight — same owner already has a transition in flight (AC3 no-op).
	OutcomeInFlight
	// OutcomeStolen — a stale holder was replaced by this call.
	OutcomeStolen
	// OutcomeConflict — another live owner holds this item or the story seat.
	OutcomeConflict
	// OutcomeTreeConflict — another live seat lease is anchored to the same
	// git working tree. Refused regardless of seat key: two engagements sharing
	// one tree would attribute each other's edits (sty_c098dc2d).
	OutcomeTreeConflict
)

// ErrTreeConflict is the sentinel a caller wraps when it turns
// OutcomeTreeConflict into a refusal, so the reason survives as errors.Is
// rather than as message text.
var ErrTreeConflict = errors.New("one engagement per working tree")

// ErrTreeRequired is returned for a TreeScoped story-seat claim that carries no
// working tree: tree identity is its whole arbitration, so it cannot be guessed.
var ErrTreeRequired = errors.New("lease: a tree-scoped seat claim requires a git working tree")

// AcquireOpts are the arbitration inputs for one seat claim. The lease package
// is MECHANISM: it applies the key and tree rules it is handed and never learns
// the name of the concurrency mode that chose them (configuration over code).
type AcquireOpts struct {
	ItemID string
	Kind   string
	Owner  string
	State  string
	// StorySeat gates the story-seat rule. Tasks take a lease (so "is anything
	// engaged" sees them) but never occupy the story seat.
	StorySeat bool
	// SeatKey is the arbitration key. Callers pass the item's own id for single
	// occupancy, or a shared key (e.g. a parent id) to admit co-holders. Empty
	// is treated as a singleton keyed by ItemID.
	SeatKey string
	// Worktree is the git toplevel this engagement is anchored to. Empty opts
	// out of tree arbitration (git unavailable / non-repo caller).
	Worktree string
	// TreeScoped makes the working tree the arbitration: this claim co-holds
	// with every other tree-scoped holder whatever the keys, so long as the
	// trees differ (the tree rule below). Exclusive narrows that to "not with a
	// holder under my own key" — the siblings of a sequential epic. Both are
	// policy flags the caller derives from the repo's mode (sty_69ec7fbc); the
	// lease package still never learns the mode's name.
	TreeScoped bool
	Exclusive  bool
	// SessionID is the acquiring session identity. Empty if the caller
	// could not establish one. The lease package does not read the env.
	SessionID string
	// StampLive says whether a seat's recorded session id still names a live
	// session (sty_36d6f155). A same-owner re-acquire re-stamps a seat whose
	// stamp is not live to SessionID, so the seat's holder can edit again; a
	// live stamp is never overwritten. Nil treats every non-empty stamp as live.
	StampLive func(sessionID string) bool
}

// restamp returns the session id a same-owner re-acquire leaves on a seat that
// carries cur: opts.SessionID when it is set, differs, and cur is empty or no
// longer live; otherwise cur.
func (o AcquireOpts) restamp(cur string) string {
	next := strings.TrimSpace(o.SessionID)
	if next == "" || next == cur {
		return cur
	}
	if cur == "" || (o.StampLive != nil && !o.StampLive(cur)) {
		return next
	}
	return cur
}

// seatKey returns the effective arbitration key (empty falls back to ItemID so
// a caller that does not care gets today's single-occupancy behaviour).
func (o AcquireOpts) seatKey() string {
	if strings.TrimSpace(o.SeatKey) != "" {
		return o.SeatKey
	}
	return o.ItemID
}

// SeatClaim is the part of a seat claim that decides whether two claims may
// hold the story seat together (sty_69ec7fbc): its arbitration key and its
// co-hold policy flags.
type SeatClaim struct {
	Key        string
	TreeScoped bool
	Exclusive  bool
}

// CoHolds reports whether two seat claims may hold the story seat at once. It
// is the ONE co-hold rule: the lease store applies it to rows, and the derived
// status scan applies it to committed stories, so the two cannot disagree.
//
//   - if either claim is not tree-scoped, they co-hold only on equal,
//     non-empty keys (the rule before sty_69ec7fbc, byte for byte);
//   - if both are tree-scoped, they co-hold when their keys differ, or when the
//     keys are equal and neither claim is exclusive. The working tree, not the
//     key, then keeps them apart — that rule lives in AcquireWith.
//
// A pre-migration holder is not tree-scoped, so a mixed store never widens
// concurrency by accident.
func CoHolds(a, b SeatClaim) bool {
	ka, kb := strings.TrimSpace(a.Key), strings.TrimSpace(b.Key)
	if !a.TreeScoped || !b.TreeScoped {
		return ka != "" && kb != "" && ka == kb
	}
	if ka == "" || kb == "" || ka != kb {
		return true
	}
	return !a.Exclusive && !b.Exclusive
}

// claim is the SeatClaim an acquire carries.
func (o AcquireOpts) claim() SeatClaim {
	return SeatClaim{Key: o.seatKey(), TreeScoped: o.TreeScoped, Exclusive: o.Exclusive}
}

// Claim is the SeatClaim a held lease carries.
func (l Lease) Claim() SeatClaim {
	return SeatClaim{Key: l.SeatKey, TreeScoped: l.TreeScoped, Exclusive: l.Exclusive}
}

// Store wraps engagement_lease operations against a shared sqlite handle.
type Store struct{ db *sql.DB }

// New returns a Store bound to db.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Migrate creates the engagement_lease table. Idempotent.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("lease: migrate: %w", err)
	}
	// Idempotent column add for DBs created before in_flight existed.
	if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN in_flight INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("lease: migrate in_flight: %w", err)
	}
	// When in_flight was set (sty_bf797fa9). Empty string when not in flight.
	if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN in_flight_at TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("lease: migrate in_flight_at: %w", err)
	}
	// Pid of the process that marked in_flight (sty_bf797fa9 AC3). 0 = unknown.
	if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN in_flight_pid INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("lease: migrate in_flight_pid: %w", err)
	}
	// Transition activity (which gate / index / total) for queryable progress (sty_598a8e1b).
	for _, col := range []string{
		"activity_label TEXT NOT NULL DEFAULT ''",
		"activity_index INTEGER NOT NULL DEFAULT 0",
		"activity_total INTEGER NOT NULL DEFAULT 0",
		"activity_at TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("lease: migrate activity: %w", err)
		}
	}
	// Seat arbitration key + working-tree anchor (sty_c098dc2d). Empty on
	// pre-upgrade rows: an empty key is a singleton (today's refusal) and an
	// empty tree does not participate in tree arbitration, so a lease held
	// across the upgrade keeps behaving exactly as it did.
	for _, col := range []string{
		"seat_key TEXT NOT NULL DEFAULT ''",
		"worktree TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("lease: migrate seat arbitration: %w", err)
		}
	}
	// After the column exists on upgraded DBs, not in schema above.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_engagement_lease_seat_key ON engagement_lease(seat_key)`); err != nil {
		return fmt.Errorf("lease: migrate seat_key index: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("lease: migrate session_id: %w", err)
	}
	// Co-hold policy flags (sty_69ec7fbc). 0 on pre-upgrade rows: such a holder
	// is not tree-scoped, so it keeps the equal-key rule it was taken under.
	for _, col := range []string{
		"tree_scoped INTEGER NOT NULL DEFAULT 0",
		"exclusive INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("lease: migrate co-hold policy: %w", err)
		}
	}
	// In-flight dispatch metadata, extending the existing activity record
	// (sty_752c4ef2): additive and nullable-by-default, so a binary reading an
	// upgraded row before this column existed is unaffected, and a lease held
	// across the upgrade keeps behaving exactly as it did.
	for _, col := range []string{
		"activity_agent TEXT NOT NULL DEFAULT ''",
		"activity_model TEXT NOT NULL DEFAULT ''",
		"activity_pid INTEGER NOT NULL DEFAULT 0",
		"activity_event_label TEXT NOT NULL DEFAULT ''",
		"activity_event_at TEXT NOT NULL DEFAULT ''",
		"activity_event_count INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("lease: migrate activity detail: %w", err)
		}
	}
	// Gate-round progress on the same activity record (sty_216d5205): whether the
	// stamp is a gate round's, and its passed and failed counts so far.
	for _, col := range []string{
		"activity_gates INTEGER NOT NULL DEFAULT 0",
		"activity_passed INTEGER NOT NULL DEFAULT 0",
		"activity_failed INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := db.Exec(`ALTER TABLE engagement_lease ADD COLUMN ` + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("lease: migrate activity gates: %w", err)
		}
	}
	// Backfill so pre-upgrade stuck in_flight rows age from a real clock, not
	// from a hook-refreshed heartbeat. Without this, zero-InFlightAt residue
	// cannot self-heal via the TTL backstop.
	if _, err := db.Exec(`UPDATE engagement_lease SET in_flight_at = heartbeat_at
		WHERE in_flight = 1 AND (in_flight_at = '' OR in_flight_at IS NULL)`); err != nil {
		return fmt.Errorf("lease: backfill in_flight_at: %w", err)
	}
	return nil
}

// EffectiveInFlight reports whether the lease is mid-transition for gate
// purposes. Consumers of "is a transition running right now" must use this
// rather than raw InFlight (architecture: domain calculation lives in lease).
//
// Order of checks:
//  1. raw InFlight false → not live
//  2. InFlightPid > 0 and dead on this host → not live (primary AC3 self-heal:
//     the process that owned the transition is gone; Owner stays pid-less)
//  3. zero InFlightAt → not live (residue after upgrade without backfill)
//  4. InFlightAt older than InFlightTTL → not live (backstop when pid unknown
//     or still alive but stuck)
func EffectiveInFlight(l Lease, now time.Time) bool {
	if !l.InFlight {
		return false
	}
	if l.InFlightPid > 0 && !pidAlive(l.InFlightPid) {
		return false
	}
	if l.InFlightAt.IsZero() {
		return false
	}
	return now.Sub(l.InFlightAt) <= InFlightTTL
}

// currentInFlightPid is the OS pid stamped onto a new in_flight mark.
// Overridable in tests.
var currentInFlightPid = func() int { return os.Getpid() }

// ResolveOwner returns the process owner identity for acquire/release.
// SATELLE_OWNER (trimmed) wins for multi-agent isolation. The default is
// local@hostname (no pid) so sequential CLI invocations on one machine share
// ownership — a pid-scoped default would break park/done release after the
// engage process exits (sty_8426b9c0). Process death is detected via
// HeartbeatTTL (and optional local@host:pid forms if an operator sets that).
func ResolveOwner() string {
	if v := strings.TrimSpace(os.Getenv("SATELLE_OWNER")); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return "local@" + host
}

// Acquire claims the seat for itemID under single occupancy — the seat key is
// the item's own id, so any other live seat holder conflicts. This is the
// unkeyed, tree-unanchored form; AcquireWith is the full surface.
func (s *Store) Acquire(ctx context.Context, itemID, kind, owner, state string, occupiesStorySeat bool) (Lease, Outcome, *Lease, error) {
	return s.AcquireWith(ctx, AcquireOpts{
		ItemID: itemID, Kind: kind, Owner: owner, State: state,
		StorySeat: occupiesStorySeat, SeatKey: itemID,
	})
}

// AcquireWith claims the seat for opts.ItemID. Returns the resulting lease, the
// outcome, and — on OutcomeConflict / OutcomeTreeConflict — the blocking holder.
// check-and-insert runs in ONE immediate transaction.
//
// Arbitration for a NEW story-seat claim, in order:
//  1. KEY — a live seat lease conflicts unless its key equals this claim's key
//     and both are non-empty. Single occupancy is simply "every key is the
//     item's own id"; there is no mode branch here.
//  2. TREE — among claims the key ADMITS, a live seat lease on the same
//     non-empty worktree refuses (OutcomeTreeConflict): two engagements sharing
//     one tree would attribute each other's edits.
//
// Key before tree because the two refusals give different advice, and only the
// key answer survives moving: a claim the key rejects is not helped by a fresh
// working tree, so telling it to make one would be wrong. It also means single
// occupancy never reaches the tree branch, leaving its refusal exactly as it was.
//
// Stale holders are stolen (deleted) as they are encountered, exactly as before.
func (s *Store) AcquireWith(ctx context.Context, opts AcquireOpts) (Lease, Outcome, *Lease, error) {
	if s == nil || s.db == nil {
		return Lease{}, 0, nil, errors.New("lease: store not configured")
	}
	itemID, kind, owner := opts.ItemID, opts.Kind, opts.Owner
	occupiesStorySeat := opts.StorySeat
	if strings.TrimSpace(itemID) == "" || strings.TrimSpace(owner) == "" {
		return Lease{}, 0, nil, fmt.Errorf("lease: item_id and owner required")
	}
	now := time.Now().UTC()
	nowS := now.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, 0, nil, fmt.Errorf("lease: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Existing row for this item?
	cur, err := scanLease(tx.QueryRowContext(ctx,
		`SELECT `+leaseCols+` FROM engagement_lease WHERE item_id = ?`, itemID))
	if err == nil {
		if cur.Owner == owner {
			// Transition already in flight: AC3 no-op (never two concurrent dispatches).
			// Stuck flag self-heal: dead transitioning pid or aged in_flight_at →
			// treat as settled so the same owner can re-enter (sty_bf797fa9).
			if cur.InFlight && EffectiveInFlight(cur, now) {
				if _, err := tx.ExecContext(ctx,
					`UPDATE engagement_lease SET heartbeat_at = ? WHERE item_id = ?`, nowS, itemID); err != nil {
					return Lease{}, 0, nil, fmt.Errorf("lease: heartbeat: %w", err)
				}
				if err := tx.Commit(); err != nil {
					return Lease{}, 0, nil, err
				}
				cur.HeartbeatAt = now
				return cur, OutcomeInFlight, nil, nil
			}
			// Settled lease (or expired stuck in_flight), same owner: mark in_flight
			// for the new step. Do NOT advance state yet — state tracks last
			// COMMITTED engaging status; Confirm advances it after store.Update
			// (avoids gate-reject poison).
			pid := currentInFlightPid()
			// The anchor is recorded ONCE, at first claim: a sequential step
			// must not silently re-anchor the lease to wherever the next CLI
			// invocation happened to run. Empty values are backfilled so a
			// lease held across the upgrade gains its key/tree on the next step.
			seatKey, worktree := cur.SeatKey, cur.Worktree
			if strings.TrimSpace(seatKey) == "" {
				seatKey = opts.seatKey()
			}
			if strings.TrimSpace(worktree) == "" {
				worktree = opts.Worktree
			}
			sessionID := opts.restamp(cur.SessionID)
			if _, err := tx.ExecContext(ctx,
				`UPDATE engagement_lease SET in_flight = 1, in_flight_at = ?, in_flight_pid = ?, heartbeat_at = ?, kind = ?, story_seat = ?, seat_key = ?, worktree = ?, session_id = ?
				 WHERE item_id = ?`, nowS, pid, nowS, kind, boolInt(occupiesStorySeat), seatKey, worktree, sessionID, itemID); err != nil {
				return Lease{}, 0, nil, fmt.Errorf("lease: mark in_flight: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return Lease{}, 0, nil, err
			}
			cur.InFlight = true
			cur.InFlightAt = now
			cur.InFlightPid = pid
			cur.HeartbeatAt = now
			cur.Kind = kind
			cur.StorySeat = occupiesStorySeat
			cur.SeatKey = seatKey
			cur.Worktree = worktree
			cur.SessionID = sessionID
			return cur, OutcomeAlreadyHeld, nil, nil
		}
		// Different owner: stale → steal; live → conflict.
		if Alive(cur, now) {
			h := cur
			return Lease{}, OutcomeConflict, &h, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM engagement_lease WHERE item_id = ?`, itemID); err != nil {
			return Lease{}, 0, nil, fmt.Errorf("lease: steal delete: %w", err)
		}
		// fall through to insert; mark stolen
		stolen := cur
		l, err := insertLease(ctx, tx, opts, nowS)
		if err != nil {
			return Lease{}, 0, nil, err
		}
		if err := tx.Commit(); err != nil {
			return Lease{}, 0, nil, err
		}
		return l, OutcomeStolen, &stolen, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Lease{}, 0, nil, err
	}

	// Story-seat arbitration against EVERY live seat holder — one lease or five,
	// each is examined; a single LIMIT 1 probe would drop co-holders.
	if occupiesStorySeat {
		// A tree-scoped claim is arbitrated by its working tree alone, so it
		// cannot be admitted on a guess: with no tree it is refused outright.
		if opts.TreeScoped && strings.TrimSpace(opts.Worktree) == "" {
			return Lease{}, 0, nil, ErrTreeRequired
		}
		holders, herr := scanLeases(tx.QueryContext(ctx,
			`SELECT `+leaseCols+` FROM engagement_lease WHERE story_seat = 1`))
		if herr != nil {
			return Lease{}, 0, nil, herr
		}
		var live []Lease
		for _, h := range holders {
			if h.ItemID == itemID {
				continue
			}
			if !Alive(h, now) {
				// Steal stale seat holder (same rule as before, now per row).
				if _, err := tx.ExecContext(ctx, `DELETE FROM engagement_lease WHERE item_id = ?`, h.ItemID); err != nil {
					return Lease{}, 0, nil, fmt.Errorf("lease: seat steal: %w", err)
				}
				continue
			}
			live = append(live, h)
		}
		// 1. Key — co-holding is CoHolds: equal, non-empty keys, unless both
		//    claims are tree-scoped (then only a shared exclusive key refuses).
		claim := opts.claim()
		for _, h := range live {
			if !CoHolds(claim, h.Claim()) {
				blocker := h
				return Lease{}, OutcomeConflict, &blocker, nil
			}
		}
		// 2. Tree — only reachable once the key ADMITS, which is what makes it
		//    the accurate reason: this claim may legitimately co-hold the seat,
		//    it just cannot share a tree. Under single occupancy every key is
		//    unique, so the loop above already refused and this never fires —
		//    that is why the default mode's refusal text is untouched.
		if tree := strings.TrimSpace(opts.Worktree); tree != "" {
			for _, h := range live {
				if h.Worktree == tree {
					blocker := h
					return Lease{}, OutcomeTreeConflict, &blocker, nil
				}
			}
		}
	}

	l, err := insertLease(ctx, tx, opts, nowS)
	if err != nil {
		return Lease{}, 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, 0, nil, err
	}
	return l, OutcomeAcquired, nil, nil
}

func insertLease(ctx context.Context, tx *sql.Tx, opts AcquireOpts, nowS string) (Lease, error) {
	// New lease starts in_flight=1; Confirm clears it after status commits.
	pid := currentInFlightPid()
	key := opts.seatKey()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO engagement_lease (item_id, kind, story_seat, seat_key, worktree, owner, state, acquired_at, heartbeat_at, stop_requested_by, stop_reason, in_flight, in_flight_at, in_flight_pid, session_id, tree_scoped, exclusive)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 1, ?, ?, ?, ?, ?)`,
		opts.ItemID, opts.Kind, boolInt(opts.StorySeat), key, opts.Worktree,
		opts.Owner, opts.State, nowS, nowS, nowS, pid, strings.TrimSpace(opts.SessionID),
		boolInt(opts.TreeScoped), boolInt(opts.Exclusive)); err != nil {
		return Lease{}, fmt.Errorf("lease: insert: %w", err)
	}
	now, _ := time.Parse(time.RFC3339Nano, nowS)
	return Lease{
		ItemID: opts.ItemID, Kind: opts.Kind, StorySeat: opts.StorySeat,
		SeatKey: key, Worktree: opts.Worktree, TreeScoped: opts.TreeScoped, Exclusive: opts.Exclusive,
		Owner: opts.Owner, State: opts.State,
		AcquiredAt: now, HeartbeatAt: now, InFlight: true, InFlightAt: now, InFlightPid: pid,
		SessionID: strings.TrimSpace(opts.SessionID),
	}, nil
}

// InsertIfAbsent inserts a new in-flight lease for opts.ItemID and reports whether
// it did. A row already there — live or stale, any owner — is left byte for byte
// as it was: this never updates, steals or deletes, which is what AcquireWith's
// same-owner and stale branches do. It is for a caller that only needs the page to
// see its work in flight (a readiness round, sty_a42f4add) and must never
// take over a seat another step holds. The caller releases only a row it inserted.
func (s *Store) InsertIfAbsent(ctx context.Context, opts AcquireOpts) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("lease: store not configured")
	}
	if strings.TrimSpace(opts.ItemID) == "" || strings.TrimSpace(opts.Owner) == "" {
		return false, fmt.Errorf("lease: item_id and owner required")
	}
	nowS := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO engagement_lease (item_id, kind, story_seat, seat_key, worktree, owner, state, acquired_at, heartbeat_at, stop_requested_by, stop_reason, in_flight, in_flight_at, in_flight_pid, session_id, tree_scoped, exclusive)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 1, ?, ?, ?, ?, ?)
		 ON CONFLICT(item_id) DO NOTHING`,
		opts.ItemID, opts.Kind, boolInt(opts.StorySeat), opts.seatKey(), opts.Worktree,
		opts.Owner, opts.State, nowS, nowS, nowS, currentInFlightPid(), strings.TrimSpace(opts.SessionID),
		boolInt(opts.TreeScoped), boolInt(opts.Exclusive))
	if err != nil {
		return false, fmt.Errorf("lease: insert if absent: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// MarkInFlightIfSettled flags the existing lease for itemID in flight, and reports
// whether it did, only when EffectiveInFlight judges it settled. A row already
// effectively in flight, or no row, is left alone. It writes the in-flight columns
// alone — never owner, story_seat, seat_key, state, worktree or heartbeat — and
// only if they still hold the values just read, so a concurrent mark is not
// overwritten. The caller that gets true settles the row again with ClearInFlight
// (sty_a42f4add).
func (s *Store) MarkInFlightIfSettled(ctx context.Context, itemID string, now time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("lease: store not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("lease: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var inFlight, pid int
	var at string
	err = tx.QueryRowContext(ctx,
		`SELECT in_flight, in_flight_at, in_flight_pid FROM engagement_lease WHERE item_id = ?`, itemID).
		Scan(&inFlight, &at, &pid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lease: read in_flight: %w", err)
	}
	if EffectiveInFlight(Lease{InFlight: inFlight != 0, InFlightAt: parseLeaseTime(at), InFlightPid: pid}, now) {
		return false, nil
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE engagement_lease SET in_flight = 1, in_flight_at = ?, in_flight_pid = ?
		 WHERE item_id = ? AND in_flight = ? AND in_flight_at = ? AND in_flight_pid = ?`,
		now.UTC().Format(time.RFC3339Nano), currentInFlightPid(), itemID, inFlight, at, pid)
	if err != nil {
		return false, fmt.Errorf("lease: mark in_flight: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

// Confirm settles an in-flight transition: records committed engaging state and
// clears in_flight. Called after store.Update succeeds for an engaging target.
func (s *Store) Confirm(ctx context.Context, itemID, committedState string) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	nowS := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET state = ?, in_flight = 0, in_flight_at = '', in_flight_pid = 0, activity_label = '', activity_index = 0, activity_total = 0, activity_at = '', heartbeat_at = ?, activity_agent = '', activity_model = '', activity_pid = 0, activity_event_label = '', activity_event_at = '', activity_event_count = 0, activity_gates = 0, activity_passed = 0, activity_failed = 0 WHERE item_id = ?`,
		committedState, nowS, itemID)
	if err != nil {
		return fmt.Errorf("lease: confirm: %w", err)
	}
	return nil
}

// ClearInFlight drops the in_flight flag without changing state — used when a
// sequential step aborts (gate reject) so a retry can re-acquire the step.
func (s *Store) ClearInFlight(ctx context.Context, itemID string) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET in_flight = 0, in_flight_at = '', in_flight_pid = 0, activity_label = '', activity_index = 0, activity_total = 0, activity_at = '', activity_agent = '', activity_model = '', activity_pid = 0, activity_event_label = '', activity_event_at = '', activity_event_count = 0, activity_gates = 0, activity_passed = 0, activity_failed = 0 WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("lease: clear in_flight: %w", err)
	}
	return nil
}

// Release frees the seat for itemID. Only the owner may release mid-flight
// (AC4/AC5). For workflow exit (terminal/park) use ForceRelease.
func (s *Store) Release(ctx context.Context, itemID, owner string) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lease: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := scanLease(tx.QueryRowContext(ctx,
		`SELECT `+leaseCols+` FROM engagement_lease WHERE item_id = ?`, itemID))
	if errors.Is(err, ErrNotFound) {
		return tx.Commit() // already free
	}
	if err != nil {
		return err
	}
	if cur.Owner != owner {
		return fmt.Errorf("%w: lease for %s held by %q (requester %q)", ErrNotOwner, itemID, cur.Owner, owner)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM engagement_lease WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("lease: delete: %w", err)
	}
	return tx.Commit()
}

// ForceRelease deletes the lease for itemID regardless of owner. Used when a
// workflow transition into terminal/park commits — the DOT exit is the
// authority that the seat is free (sequential CLI pids must not leave a stuck
// seat because the engage process already exited).
func (s *Store) ForceRelease(ctx context.Context, itemID string) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM engagement_lease WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("lease: force release: %w", err)
	}
	return nil
}

// RequestStop annotates the holder row with a stop request. Never deletes.
// Any principal may request; arbitration happens at the next step edge (AC5).
func (s *Store) RequestStop(ctx context.Context, itemID, requester, reason string) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	if strings.TrimSpace(requester) == "" {
		return fmt.Errorf("lease: requester required")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET stop_requested_by = ?, stop_reason = ? WHERE item_id = ?`,
		requester, reason, itemID)
	if err != nil {
		return fmt.Errorf("lease: request stop: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lease: no active lease for %s — nothing to stop", itemID)
	}
	return nil
}

// Get returns the lease for itemID or ErrNotFound.
func (s *Store) Get(ctx context.Context, itemID string) (Lease, error) {
	if s == nil || s.db == nil {
		return Lease{}, errors.New("lease: store not configured")
	}
	return scanLease(s.db.QueryRowContext(ctx,
		`SELECT `+leaseCols+` FROM engagement_lease WHERE item_id = ?`, itemID))
}

// AnyActive reports whether any engagement lease row exists. Callers that gate
// edit/commit must prefer List + a performing-state predicate (see evaluateSeat
// in the hook package): a bare row is not necessarily live engagement
// (sty_1738f973 — orphaned / stale / non-performing rows must not open the gate).
func (s *Store) AnyActive(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("lease: store not configured")
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM engagement_lease`).Scan(&n); err != nil {
		return false, fmt.Errorf("lease: any: %w", err)
	}
	return n > 0, nil
}

// List returns every engagement lease row (including stale / in-flight). The
// lease package is pure mechanism — callers decide which rows count as live
// engagement (staleness via Alive; performing-state via workflow shape).
func (s *Store) List(ctx context.Context) ([]Lease, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("lease: store not configured")
	}
	// Ordered by seat_key first so co-holders of one key list together (the
	// `story seat` surface groups on this); acquired_at keeps it deterministic.
	return scanLeases(s.db.QueryContext(ctx,
		`SELECT `+leaseCols+` FROM engagement_lease ORDER BY seat_key, acquired_at`))
}

// PickForWorktree selects the lease anchored to worktree. This is the domain
// answer to "which of these leases is THIS session's" — needed because the
// default owner is pid-less local@host (see ResolveOwner), so two sibling
// leases held from one host are indistinguishable by owner alone. A heartbeat
// routed by owner would refresh whichever row came first and let the actually
// worked sibling go stale at TTL (sty_c098dc2d).
//
// ok is false for an empty worktree or no match — callers then fall back to
// their own rule rather than guessing.
func PickForWorktree(leases []Lease, worktree string) (Lease, bool) {
	tree := strings.TrimSpace(worktree)
	if tree == "" {
		return Lease{}, false
	}
	for _, l := range leases {
		if l.Worktree == tree {
			return l, true
		}
	}
	return Lease{}, false
}

// scanLeases drains a lease query. Takes (rows, err) so callers can inline the
// query call.
func scanLeases(rows *sql.Rows, qerr error) ([]Lease, error) {
	if qerr != nil {
		return nil, fmt.Errorf("lease: list: %w", qerr)
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var l Lease
		var seat, inflight, inflightPid, actIdx, actTotal, actPid, actEventCount, treeScoped, exclusive, actGates int
		var acq, beat, inflightAt, actAt, actEventAt string
		if err := rows.Scan(&l.ItemID, &l.Kind, &seat, &l.SeatKey, &l.Worktree, &l.Owner, &l.State, &acq, &beat, &l.StopRequestedBy, &l.StopReason,
			&inflight, &inflightAt, &inflightPid, &l.ActivityLabel, &actIdx, &actTotal, &actAt, &l.SessionID, &treeScoped, &exclusive,
			&l.ActivityAgent, &l.ActivityModel, &actPid, &l.ActivityEventLabel, &actEventAt, &actEventCount,
			&actGates, &l.ActivityPassed, &l.ActivityFailed); err != nil {
			return nil, fmt.Errorf("lease: list scan: %w", err)
		}
		l.StorySeat = seat != 0
		l.TreeScoped = treeScoped != 0
		l.Exclusive = exclusive != 0
		l.InFlight = inflight != 0
		l.InFlightPid = inflightPid
		l.ActivityIndex = actIdx
		l.ActivityTotal = actTotal
		l.ActivityPid = actPid
		l.ActivityEventCount = actEventCount
		l.ActivityGates = actGates != 0
		l.AcquiredAt = parseLeaseTime(acq)
		l.HeartbeatAt = parseLeaseTime(beat)
		l.InFlightAt = parseLeaseTime(inflightAt)
		l.ActivityAt = parseLeaseTime(actAt)
		l.ActivityEventAt = parseLeaseTime(actEventAt)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lease: list: %w", err)
	}
	return out, nil
}

// Reap deletes rows that are not Alive. Returns the reaped leases.
// Opportunistic housekeeping for seat-list honesty; gate correctness does not
// depend on Reap — a non-Alive row is already excluded from engagement.
func (s *Store) Reap(ctx context.Context) ([]Lease, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("lease: store not configured")
	}
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var reaped []Lease
	for _, l := range all {
		if Alive(l, now) {
			continue
		}
		if err := s.ForceRelease(ctx, l.ItemID); err != nil {
			return reaped, err
		}
		reaped = append(reaped, l)
	}
	return reaped, nil
}

// Heartbeat refreshes heartbeat_at for the owner's lease (optional long-dispatch).
func (s *Store) Heartbeat(ctx context.Context, itemID, owner string) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	nowS := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET heartbeat_at = ? WHERE item_id = ? AND owner = ?`,
		nowS, itemID, owner)
	if err != nil {
		return fmt.Errorf("lease: heartbeat: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetHeartbeat overwrites heartbeat_at for itemID. Used by regression tests and
// seat diagnostics to simulate a frozen heartbeat after process death
// (sty_1738f973 AC5 kill-simulation). Not an operator happy-path verb.
func (s *Store) SetHeartbeat(ctx context.Context, itemID string, at time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET heartbeat_at = ? WHERE item_id = ?`,
		at.UTC().Format(time.RFC3339Nano), itemID)
	if err != nil {
		return fmt.Errorf("lease: set heartbeat: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetInFlightAt overwrites in_flight_at for itemID (and forces in_flight=1).
// Test/diagnostic seam for stuck-flag aging (sty_bf797fa9). Not an operator path.
func (s *Store) SetInFlightAt(ctx context.Context, itemID string, at time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET in_flight = 1, in_flight_at = ? WHERE item_id = ?`,
		at.UTC().Format(time.RFC3339Nano), itemID)
	if err != nil {
		return fmt.Errorf("lease: set in_flight_at: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanLease(row *sql.Row) (Lease, error) {
	var l Lease
	var seat, inflight, inflightPid, actIdx, actTotal, actPid, actEventCount, treeScoped, exclusive, actGates int
	var acq, beat, inflightAt, actAt, actEventAt string
	err := row.Scan(&l.ItemID, &l.Kind, &seat, &l.SeatKey, &l.Worktree, &l.Owner, &l.State, &acq, &beat, &l.StopRequestedBy, &l.StopReason,
		&inflight, &inflightAt, &inflightPid, &l.ActivityLabel, &actIdx, &actTotal, &actAt, &l.SessionID, &treeScoped, &exclusive,
		&l.ActivityAgent, &l.ActivityModel, &actPid, &l.ActivityEventLabel, &actEventAt, &actEventCount,
		&actGates, &l.ActivityPassed, &l.ActivityFailed)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, fmt.Errorf("lease: scan: %w", err)
	}
	l.StorySeat = seat != 0
	l.TreeScoped = treeScoped != 0
	l.Exclusive = exclusive != 0
	l.InFlight = inflight != 0
	l.InFlightPid = inflightPid
	l.ActivityIndex = actIdx
	l.ActivityTotal = actTotal
	l.ActivityPid = actPid
	l.ActivityEventCount = actEventCount
	l.ActivityGates = actGates != 0
	l.AcquiredAt = parseLeaseTime(acq)
	l.HeartbeatAt = parseLeaseTime(beat)
	l.InFlightAt = parseLeaseTime(inflightAt)
	l.ActivityAt = parseLeaseTime(actAt)
	l.ActivityEventAt = parseLeaseTime(actEventAt)
	return l, nil
}

// SetActivity stamps the current gate/phase on an in-flight lease so seat can
// report progress without the dispatching terminal (sty_598a8e1b). Best-effort:
// callers may ignore the error — observability must not fail a transition.
func (s *Store) SetActivity(ctx context.Context, itemID string, a Activity) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	nowS := time.Now().UTC().Format(time.RFC3339Nano)
	gates := 0
	if a.Gates {
		gates = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET activity_label = ?, activity_index = ?, activity_total = ?, activity_at = ?, activity_gates = ?, activity_passed = ?, activity_failed = ? WHERE item_id = ?`,
		a.Label, a.Index, a.Total, nowS, gates, a.Passed, a.Failed, itemID)
	if err != nil {
		return fmt.Errorf("lease: set activity: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveActivity returns the queryable progress for a live in-flight lease.
// ok is false when the transition is not effectively in flight or no activity
// has been stamped (sty_598a8e1b). Domain calculation lives in lease — same
// rule as EffectiveInFlight.
func EffectiveActivity(l Lease, now time.Time) (a Activity, elapsed time.Duration, ok bool) {
	if !EffectiveInFlight(l, now) {
		return Activity{}, 0, false
	}
	if l.ActivityAt.IsZero() || l.ActivityLabel == "" {
		return Activity{}, 0, false
	}
	return Activity{
		Label: l.ActivityLabel, Index: l.ActivityIndex, Total: l.ActivityTotal,
		Gates: l.ActivityGates, Passed: l.ActivityPassed, Failed: l.ActivityFailed,
	}, now.Sub(l.ActivityAt), true
}

// SetActivityDetail extends the EXISTING activity record with in-flight
// dispatch metadata (sty_752c4ef2) — agent binding, model, pid, and the last
// real event's label/count. It does not touch the phase label/index/total
// SetActivity owns, so a caller may refresh detail far more often (throttled,
// on every real event) without disturbing the once-per-phase stamp. No second
// table. Best-effort: observability must not fail a dispatch.
func (s *Store) SetActivityDetail(ctx context.Context, itemID string, d ActivityDetail) error {
	if s == nil || s.db == nil {
		return errors.New("lease: store not configured")
	}
	eventAt := d.EventAt
	if eventAt.IsZero() {
		eventAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engagement_lease SET activity_agent = ?, activity_model = ?, activity_pid = ?, activity_event_label = ?, activity_event_at = ?, activity_event_count = ? WHERE item_id = ?`,
		d.Agent, d.Model, d.Pid, d.EventLabel, eventAt.UTC().Format(time.RFC3339Nano), d.EventCount, itemID)
	if err != nil {
		return fmt.Errorf("lease: set activity detail: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveActivityDetail returns the in-flight dispatch metadata alongside
// the phase activity (sty_752c4ef2). ok is false under the same rule
// EffectiveActivity applies: not effectively in flight, or no detail has been
// stamped yet.
func EffectiveActivityDetail(l Lease, now time.Time) (d ActivityDetail, ok bool) {
	if !EffectiveInFlight(l, now) {
		return ActivityDetail{}, false
	}
	if l.ActivityEventAt.IsZero() && l.ActivityAgent == "" {
		return ActivityDetail{}, false
	}
	return ActivityDetail{
		Agent: l.ActivityAgent, Model: l.ActivityModel, Pid: l.ActivityPid,
		EventLabel: l.ActivityEventLabel, EventAt: l.ActivityEventAt, EventCount: l.ActivityEventCount,
	}, true
}

func parseLeaseTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// parseLocalOwner parses "local@hostname:pid".
func parseLocalOwner(owner string) (host string, pid int, ok bool) {
	if !strings.HasPrefix(owner, "local@") {
		return "", 0, false
	}
	rest := strings.TrimPrefix(owner, "local@")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", 0, false
	}
	host = rest[:i]
	p, err := strconv.Atoi(rest[i+1:])
	if err != nil || p <= 0 {
		return "", 0, false
	}
	return host, p, true
}

// pidAlive reports whether pid is a live process on this host (best-effort).
// Overridable in tests via PidAlive.
var PidAlive = defaultPidAlive

func pidAlive(pid int) bool { return PidAlive(pid) }

func defaultPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Linux /proc is the reliable existence check; FindProcess always succeeds
	// on Unix and does not prove the process is alive.
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		return true
	}
	// Non-Linux: treat as alive so we never steal a live lease via false death.
	// HeartbeatTTL is the cross-platform stale path.
	if _, err := os.Stat("/proc"); errors.Is(err, os.ErrNotExist) {
		return true
	}
	return false
}
