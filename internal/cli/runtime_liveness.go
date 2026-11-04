// runtime_liveness.go — refuse relocating a live runtime (sty_5308eb60).
//
// migrate / runtime migrate take a VACUUM INTO snapshot of the legacy DB and
// then remove it as residue. Without a liveness guard, every write the working
// session commits AFTER the snapshot is stranded (deleted inode / removed
// file) while new processes read the home-keyed copy. This package is pure
// mechanism: detect live holders, refuse (or warn under --allow-live).
package cli

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	_ "modernc.org/sqlite"
)

// liveHolder is one reason migrate should refuse (or warn under --allow-live).
type liveHolder struct {
	Kind   string // "lease" | "serve"
	Detail string // one-line description naming the holder
}

// detectLiveRuntime reports live engagement leases in the legacy DB and a
// responding satelle serve on this repo's resolved web port. The legacy DB is
// opened read-only with the same DSN shape vacuumInto uses — never store.Open,
// which would create wal/shm and fight VACUUM INTO.
//
// An in-flight dispatch is covered by freshness: a live dispatch heartbeats, so
// lease.Alive already refuses it. in_flight only decorates the message. Refusing
// on a stale in_flight row would permanently wedge migrate after process death.
func detectLiveRuntime(cfg config.Config, repoRoot, legacyDB string) ([]liveHolder, error) {
	var out []liveHolder
	if fileExists(legacyDB) {
		leases, err := scanLiveLeases(legacyDB)
		if err != nil {
			return nil, err
		}
		out = append(out, leases...)
	}
	if h, ok := probeServe(cfg, repoRoot); ok {
		out = append(out, h)
	}
	return out, nil
}

// scanLiveLeases opens legacyDB read-only and returns non-stale engagement
// leases. A missing engagement_lease table (pre-lease DB) yields no holders.
func scanLiveLeases(legacyDB string) ([]liveHolder, error) {
	db, err := sql.Open("sqlite", store.ReadOnlyDSN(legacyDB))
	if err != nil {
		return nil, fmt.Errorf("live-runtime: open legacy db: %w", err)
	}
	defer db.Close()

	// Prefer the full column set (post sty_bf797fa9). Fall back for pre-upgrade DBs.
	rows, err := db.Query(
		`SELECT item_id, kind, owner, state, acquired_at, heartbeat_at, in_flight, in_flight_at, in_flight_pid
		 FROM engagement_lease`)
	if err != nil && strings.Contains(err.Error(), "no such column") {
		rows, err = db.Query(
			`SELECT item_id, kind, owner, state, acquired_at, heartbeat_at, in_flight
			 FROM engagement_lease`)
	}
	if err != nil {
		// Missing table on pre-lease DBs is not an error — nothing to refuse on.
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("live-runtime: list leases: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	var out []liveHolder
	for rows.Next() {
		var itemID, kind, owner, state, acq, beat, inflightAt string
		var inflight, inflightPid int
		// Try full scan first via column count from query shape.
		cols, _ := rows.Columns()
		var scanErr error
		if len(cols) >= 9 {
			scanErr = rows.Scan(&itemID, &kind, &owner, &state, &acq, &beat, &inflight, &inflightAt, &inflightPid)
		} else {
			scanErr = rows.Scan(&itemID, &kind, &owner, &state, &acq, &beat, &inflight)
		}
		if scanErr != nil {
			return nil, fmt.Errorf("live-runtime: scan lease: %w", scanErr)
		}
		l := lease.Lease{
			ItemID:      itemID,
			Kind:        kind,
			Owner:       owner,
			State:       state,
			InFlight:    inflight != 0,
			InFlightPid: inflightPid,
		}
		l.AcquiredAt, _ = time.Parse(time.RFC3339Nano, acq)
		if l.AcquiredAt.IsZero() {
			l.AcquiredAt, _ = time.Parse(time.RFC3339, acq)
		}
		l.HeartbeatAt, _ = time.Parse(time.RFC3339Nano, beat)
		if l.HeartbeatAt.IsZero() {
			l.HeartbeatAt, _ = time.Parse(time.RFC3339, beat)
		}
		if inflightAt != "" {
			if t, perr := time.Parse(time.RFC3339Nano, inflightAt); perr == nil {
				l.InFlightAt = t
			} else if t, perr := time.Parse(time.RFC3339, inflightAt); perr == nil {
				l.InFlightAt = t
			}
		}
		if !lease.Alive(l, now) {
			continue
		}
		age := "unknown"
		if !l.HeartbeatAt.IsZero() {
			age = formatAge(now.Sub(l.HeartbeatAt))
		}
		detail := fmt.Sprintf("engagement lease  %s  owner %s  state %s  heartbeat %s ago",
			itemID, owner, state, age)
		// EffectiveInFlight: decorate only when the flag is live for gate purposes.
		if lease.EffectiveInFlight(l, now) {
			detail += " (dispatch in flight)"
		}
		out = append(out, liveHolder{Kind: "lease", Detail: detail})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("live-runtime: list leases: %w", err)
	}
	return out, nil
}

// serveProbeEnabled gates the HTTP serve probe. Unit tests flip it off so a
// developer's live satelle serve on :8787 does not refuse every migrate fixture.
var serveProbeEnabled = true

// probeServe dials resolved ports and checks /healthz. Returns a holder on
// an ok body. A foreign process on the same port is a false positive — the
// refusal message names --allow-live for that case.
func probeServe(cfg config.Config, repoRoot string) (liveHolder, bool) {
	if !serveProbeEnabled {
		return liveHolder{}, false
	}
	for _, port := range livenessPorts(cfg, repoRoot) {
		if port <= 0 {
			continue
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
		if !healthzOK(url) {
			continue
		}
		return liveHolder{
			Kind:   "serve",
			Detail: fmt.Sprintf("satelled          http://127.0.0.1:%d/ responding", port),
		}, true
	}
	return liveHolder{}, false
}

// livenessPorts returns the one machine service port to probe.
func livenessPorts(cfg config.Config, repoRoot string) []int {
	_ = cfg
	_ = repoRoot
	if gc, err := config.LoadGlobal(); err == nil {
		return []int{gc.Service.ResolvePort()}
	}
	return []int{config.DefaultWebPort}
}

// healthzOK is the dial+GET used by the serve probe; tests swap it.
var healthzOK = defaultHealthzOK

// defaultHealthzOK answers from the machine-wide /healthz cache
// (cachedHealthz), probing only when no other process has within its TTL.
func defaultHealthzOK(url string) bool {
	return cachedHealthz(url).OK
}

func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// refuseLiveRuntimeError is the non-zero exit for a live runtime without --allow-live.
func refuseLiveRuntimeError(holders []liveHolder) error {
	var b strings.Builder
	b.WriteString("migrate: refusing — this repo runtime is LIVE. Relocating now would strand every write made after the snapshot.\n")
	for _, h := range holders {
		b.WriteString("  ")
		b.WriteString(h.Detail)
		b.WriteByte('\n')
	}
	b.WriteString("Wait for that session to reach a terminal/park state (inspect: satelle story seat), stop satelle serve, then re-run. If the holder is not really satelle, override with --allow-live (UNSAFE — see satelle migrate --help).")
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// warnAllowLive prints the AC3 risk statement when overriding a live runtime.
func warnAllowLive(out io.Writer, holders []liveHolder) {
	fmt.Fprintln(out, "WARNING: --allow-live — migrating with a LIVE runtime.")
	fmt.Fprintln(out, "  Every write the live session commits AFTER this snapshot is STRANDED: it")
	fmt.Fprintln(out, "  lands in the legacy database this migration then removes (or on a deleted")
	fmt.Fprintln(out, "  inode), while new processes read the home-keyed copy immediately. Status")
	fmt.Fprintln(out, "  transitions, ledger rows, lease heartbeats, and attachments written in")
	fmt.Fprintln(out, "  that window will silently vanish. Holders:")
	for _, h := range holders {
		fmt.Fprintf(out, "    %s\n", h.Detail)
	}
}

// printLivePlanLine adds a plan summary line for dry-run / plan print.
func printLivePlanLine(out io.Writer, holders []liveHolder) {
	if len(holders) == 0 {
		fmt.Fprintln(out, "  runtime:          idle")
		return
	}
	fmt.Fprintf(out, "  runtime:          LIVE — %d holder(s)\n", len(holders))
	for _, h := range holders {
		fmt.Fprintf(out, "    - %s\n", h.Detail)
	}
}

// migrateSplitBrainHelp is the AC4 field guidance for a repo already split by a
// mid-story migration. Embedded in `satelle migrate` Long help so it travels
// with the binary (repo-agnostic mechanism guidance, not dogfood docs).
const migrateSplitBrainHelp = `
If a migration already raced a live session (split brain):

  1. satelle runtime path — confirm the home-keyed DB is now authoritative.
  2. satelle story seat — the snapshot froze the seat table. A seat whose owner
     process is gone is a PHANTOM: release it (or wait for the heartbeat TTL,
     30m, to reap it). Inspect with satelle story seat.
  3. satelle story get <id> and satelle ledger list --story <id> — compare against
     what the working session believes it did. Anything missing was written into
     the removed legacy DB and is gone.
  4. RE-DRIVE lost transitions through the workflow (satelle story set <id>
     --status <state>) so every gate runs again — never hand-patch status forward
     to catch up; the reviews that vanished did not happen on record.
  5. Re-attach documents written in the window (satelle story attach).
  6. If residue removal failed and the legacy DB survives, it is the ONLY record
     of the lost window — read it before removing anything.
`

// ensureLiveOK is the shared gate used by runMigrate and runRuntimeMigrate.
// dryRun=true only reports; apply paths refuse unless allowLive.
// Returns holders (for plan printing) and a non-nil error when refused.
func ensureLiveOK(out io.Writer, cfg config.Config, repoRoot, legacyDB string, allowLive, applying bool) ([]liveHolder, error) {
	if strings.TrimSpace(legacyDB) == "" || !fileExists(legacyDB) {
		return nil, nil
	}
	holders, err := detectLiveRuntime(cfg, repoRoot, legacyDB)
	if err != nil {
		if allowLive {
			fmt.Fprintf(out, "WARNING: --allow-live — could not inspect live runtime (%v); proceeding\n", err)
			return nil, nil
		}
		return nil, fmt.Errorf("migrate: cannot inspect live runtime: %w", err)
	}
	if len(holders) == 0 {
		return nil, nil
	}
	if !applying {
		return holders, nil
	}
	if allowLive {
		warnAllowLive(out, holders)
		return holders, nil
	}
	return holders, refuseLiveRuntimeError(holders)
}
