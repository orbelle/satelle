package costview

import (
	"encoding/json"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Figures is one item's own computed cost — dollars, the fresh/output/cache
// split, and time — derived solely from its ledger rows. CostUSD sums only
// rows that carry cost_usd; CostUnavailableRows counts the rest — an unpriced
// row is never folded in as a zero. CacheWrite is its own field, never folded
// into FreshInput or CacheRead. UnsplitTokens carries a legacy row's TokensIn
// recorded before the fresh/cache split existed.
//
// ElapsedMs is the story clock: first entry into an engaging state to first
// entry into a terminal state (by shape — never a park), or to now when the
// item is still open. AgentMs is DispatchMs (agent_invocation durations) plus
// DriverMs (driver_usage wall time), each also kept separately.
type Figures struct {
	CostUSD             float64 `json:"cost_usd"`
	CostRows            int     `json:"cost_rows"`
	CostUnavailableRows int     `json:"cost_unavailable_rows"`

	FreshInput    int `json:"fresh_input"`
	Output        int `json:"output"`
	CacheRead     int `json:"cache_read"`
	CacheWrite    int `json:"cache_write"`
	UnsplitTokens int `json:"unsplit_tokens,omitempty"`

	// UsageRows/UsageUnavailableRows count rows that fed the token figures
	// above, kept deliberately separate from CostRows/CostUnavailableRows: a
	// row can be priced (CostRows++) while its provider reported no usage at
	// all (UsageAvailable/Available false, contributing no tokens), so cost
	// availability can never stand in for token availability.
	UsageRows            int `json:"usage_rows"`
	UsageUnavailableRows int `json:"usage_unavailable_rows"`

	// UnsplitRows counts measured rows (UsageAvailable true) that reported
	// only the legacy cache-inclusive total, not the fresh/cache split —
	// UsageRows-UnsplitRows is therefore how many measured rows actually fed
	// FreshInput/CacheRead/CacheWrite. A story whose usage rows are ALL
	// legacy-unsplit has UsageRows>0 but zero split-reporting rows, so those
	// three figures must never render a bare 0 — see FormatSplitTokens.
	UnsplitRows int `json:"unsplit_rows,omitempty"`

	ElapsedMs  int64 `json:"elapsed_ms"`
	AgentMs    int64 `json:"agent_ms"`
	DispatchMs int64 `json:"dispatch_ms"`
	DriverMs   int64 `json:"driver_ms"`

	// AmbiguousTokens/AmbiguousCostUSD are the available driver rows whose session
	// another story shared over the window (DriverRow.Ambiguous): visible, and in none
	// of the exact sums above. How many rows is DriverCoverage.AmbiguousRows.
	AmbiguousTokens  int     `json:"ambiguous_tokens,omitempty"`
	AmbiguousCostUSD float64 `json:"ambiguous_cost_usd,omitempty"`

	// Driver says, per adapter, what the driving session's own rows measured and
	// what they could not; DriverStatus is its one-word roll-up (measured,
	// partial, unavailable, none). Without them the gated-and-dispatched total
	// reads as the whole story's cost (sty_ca1ca935).
	Driver       []DriverCoverage `json:"driver,omitempty"`
	DriverStatus string           `json:"driver_status"`

	// Derived is the read-time price of the measured tokens above under the
	// operator's [pricing] configuration (price.go). It is computed on every
	// read and never stored; JSON surfaces it as an added `derived_cost` object
	// and leaves the existing figures keys as they were.
	Derived DerivedCost `json:"derived_cost"`
}

// HasRows reports whether any dispatch or driver row actually measured usage
// — UsageRows counts only rows whose provider reported token usage
// (UsageAvailable/Available true); a row that was priced but reported no
// usage does NOT count here, so a story whose rows are all cost-only-or-
// unreported renders "unavailable" for FreshInput/Output/CacheRead/
// CacheWrite rather than a literal 0, which would misrepresent "nothing
// measured" as "measured nothing".
func (f Figures) HasRows() bool {
	return f.UsageRows > 0
}

// Span is an item's own wall-clock window — engage to terminal (or now).
// Kept separate from Figures.ElapsedMs so a family of items can be unioned
// into the family's wall span rather than summed: a parent open the whole
// time its children run must not report their overlapping clocks added
// together.
type Span struct {
	Start time.Time
	End   time.Time
}

// UnionSpan folds two spans into the one that covers both — the earliest
// Start and the latest End. Either side may be nil (no span to contribute);
// nil is the identity element.
func UnionSpan(a, b *Span) *Span {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	start, end := a.Start, a.End
	if b.Start.Before(start) {
		start = b.Start
	}
	if b.End.After(end) {
		end = b.End
	}
	return &Span{Start: start, End: end}
}

// Fold sums two Figures — used to fold a child's (subtree) figures into a
// parent's running family total.
func Fold(a, b Figures) Figures {
	driver := foldCoverage(a.Driver, b.Driver)
	return Figures{
		CostUSD:              a.CostUSD + b.CostUSD,
		CostRows:             a.CostRows + b.CostRows,
		CostUnavailableRows:  a.CostUnavailableRows + b.CostUnavailableRows,
		FreshInput:           a.FreshInput + b.FreshInput,
		Output:               a.Output + b.Output,
		CacheRead:            a.CacheRead + b.CacheRead,
		CacheWrite:           a.CacheWrite + b.CacheWrite,
		UnsplitTokens:        a.UnsplitTokens + b.UnsplitTokens,
		UsageRows:            a.UsageRows + b.UsageRows,
		UsageUnavailableRows: a.UsageUnavailableRows + b.UsageUnavailableRows,
		UnsplitRows:          a.UnsplitRows + b.UnsplitRows,
		ElapsedMs:            a.ElapsedMs + b.ElapsedMs,
		AgentMs:              a.AgentMs + b.AgentMs,
		DispatchMs:           a.DispatchMs + b.DispatchMs,
		DriverMs:             a.DriverMs + b.DriverMs,
		AmbiguousTokens:      a.AmbiguousTokens + b.AmbiguousTokens,
		AmbiguousCostUSD:     a.AmbiguousCostUSD + b.AmbiguousCostUSD,
		Driver:               driver,
		DriverStatus:         driverStatus(driver),
		Derived:              foldDerived(a.Derived, b.Derived),
	}
}

// Row is one dispatched/reviewed step's recorded cost — the per-gate tokens +
// wall-time captured on an agent_invocation ledger entry. UsageAvailable is
// true only when the transport reported usage (or, for legacy rows without
// the field, when tokens_total > 0). False means unreported — never a
// measured zero.
type Row struct {
	From           string `json:"from"`
	To             string `json:"to"`
	Agent          string `json:"agent"`
	Skill          string `json:"skill,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelResolved  string `json:"model_resolved,omitempty"`
	TokensIn       int    `json:"tokens_in"`
	TokensOut      int    `json:"tokens_out"`
	TokensTotal    int    `json:"tokens_total"`
	DurationMs     int64  `json:"duration_ms"`
	UsageAvailable bool   `json:"usage_available"`
	// UsageUnavailableReason names the adapter and why usage is unavailable;
	// CacheSplitUnavailable marks a provider that reported no cache fields, so
	// the zero split below is unreported rather than measured.
	UsageUnavailableReason string `json:"usage_unavailable_reason,omitempty"`
	CacheSplitUnavailable  bool   `json:"cache_split_unavailable,omitempty"`
	// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn into its
	// disjoint components. Zero on a row recorded before this split existed —
	// reported as "unsplit" rather than mistaken for a measured zero.
	TokensInFresh    int `json:"tokens_in_fresh,omitempty"`
	TokensCacheWrite int `json:"tokens_cache_write,omitempty"`
	TokensCacheRead  int `json:"tokens_cache_read,omitempty"`
	// CostUSD is the row's dollar cost; nil (never a measured zero) when the
	// provider reported none, with CostUnavailableReason naming the adapter
	// and why.
	CostUSD               *float64 `json:"cost_usd,omitempty"`
	CostUnavailableReason string   `json:"cost_unavailable_reason,omitempty"`
	// BundleID/BundleSkills mark ONE measured invocation that judged several
	// rubrics in a single reviewer session (sty_23e10d92). The row's usage is the
	// measured figure for the whole call; a per-rubric split of it is an
	// allocation (see GateValue), never a second measured call.
	BundleID     string   `json:"bundle_id,omitempty"`
	BundleSkills []string `json:"bundle_skills,omitempty"`
	// Adapter names the harness that produced the usage; Models is the row's
	// per-model token split. Both are copied from ledger.Telemetry, the sole
	// usage decoder, and feed read-time pricing (price.go).
	Adapter string               `json:"adapter,omitempty"`
	Models  []ledger.ModelTokens `json:"models,omitempty"`
	// AuthMode is the auth mode the row's binding was declared to run under, as
	// the row recorded it; empty on a row written before it was recorded. It
	// describes how the seat is authorised, never how it is priced (Pricing owns
	// that), and is shown in its own column (ledger.AuthLabel).
	AuthMode string `json:"auth_mode,omitempty"`
	// Attempt is the id of the gate presentation the row belongs to and Seat the
	// panel seat it ran as; Outcome is "unavailable" or "max-turns" for an
	// attempt that produced no verdict. All empty on a row written before they
	// were recorded, and Seat empty on a single-seat gate.
	Attempt string `json:"attempt,omitempty"`
	Seat    string `json:"seat,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Turns and ToolCalls are the run's measured counts; nil means unavailable,
	// with the adapter-named reason beside it — never a zero.
	Turns                      *int   `json:"turns,omitempty"`
	TurnsUnavailableReason     string `json:"turns_unavailable_reason,omitempty"`
	ToolCalls                  *int   `json:"tool_calls,omitempty"`
	ToolCallsUnavailableReason string `json:"tool_calls_unavailable_reason,omitempty"`
}

// DecodeRow decodes one agent_invocation ledger entry into a Row, or ok=false
// when e carries no payload, fails to decode, or is a tool-permission event
// masquerading as an invocation (ledger.IsToolPermissionRow) — the single
// decode point every caller (the story-actual clock, the cost view, the
// gate-value roll-up) shares so agent_invocation handling cannot drift apart.
func DecodeRow(e ledger.Entry) (Row, bool) {
	if len(e.Payload) == 0 || ledger.IsToolPermissionRow(e) {
		return Row{}, false
	}
	var meta struct {
		From  string `json:"from"`
		To    string `json:"to"`
		Agent string `json:"agent"`
		Skill string `json:"skill"`
		Model string `json:"model"`

		BundleID     string   `json:"bundle_id"`
		BundleSkills []string `json:"bundle_skills"`

		Attempt                    string `json:"attempt"`
		Seat                       string `json:"seat"`
		Outcome                    string `json:"outcome"`
		Turns                      *int   `json:"turns"`
		TurnsUnavailableReason     string `json:"turns_unavailable_reason"`
		ToolCalls                  *int   `json:"tool_calls"`
		ToolCallsUnavailableReason string `json:"tool_calls_unavailable_reason"`

		// A performer's row written before sty_58a7bdfe carries its turn count in
		// a budget report; a newer one writes it at the top level, as a
		// reviewer's row does.
		Budget struct {
			Turns                  *int   `json:"turns"`
			TurnsUnavailableReason string `json:"turns_unavailable_reason"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(e.Payload, &meta); err != nil {
		return Row{}, false
	}
	if meta.Turns == nil && meta.TurnsUnavailableReason == "" {
		meta.Turns, meta.TurnsUnavailableReason = meta.Budget.Turns, meta.Budget.TurnsUnavailableReason
	}
	tel := ledger.EventTelemetry(e)
	row := Row{
		From:                   meta.From,
		To:                     meta.To,
		Agent:                  meta.Agent,
		Skill:                  meta.Skill,
		Model:                  meta.Model,
		ModelResolved:          tel.ModelResolved,
		TokensIn:               tel.TokensIn,
		TokensOut:              tel.TokensOut,
		TokensTotal:            tel.TokensTotal,
		DurationMs:             tel.DurationMs,
		UsageAvailable:         tel.UsageAvailable,
		UsageUnavailableReason: tel.UsageUnavailableReason,
		CacheSplitUnavailable:  tel.CacheSplitUnavailable,
		TokensInFresh:          tel.TokensInFresh,
		TokensCacheWrite:       tel.TokensCacheWrite,
		TokensCacheRead:        tel.TokensCacheRead,
		CostUSD:                tel.CostUSD,
		CostUnavailableReason:  tel.CostUnavailableReason,
		BundleID:               meta.BundleID,
		BundleSkills:           meta.BundleSkills,
		Adapter:                tel.Adapter,
		Models:                 tel.ModelUsage,
		AuthMode:               tel.AuthMode,

		Attempt: meta.Attempt, Seat: meta.Seat, Outcome: meta.Outcome,
		Turns: meta.Turns, TurnsUnavailableReason: meta.TurnsUnavailableReason,
		ToolCalls: meta.ToolCalls, ToolCallsUnavailableReason: meta.ToolCallsUnavailableReason,
	}
	if row.Agent == "" {
		row.Agent = tel.Agent
	}
	if row.Model == "" {
		row.Model = tel.Model
	}
	return row, true
}

// DriverRow is the driving (in-loop) session's own recorded usage on one
// engage/close/park window (sty_81caa41b) — the figures subset costview needs
// to fold a driver_usage ledger entry into Figures. The richer payload (session
// cumulative reconciliation, late/pending markers) stays a verb concern; a
// driver_usage entry decodes cleanly into either shape since JSON decode
// ignores fields a target struct does not declare.
type DriverRow struct {
	SessionID  string `json:"session_id"`
	Executable string `json:"executable"`
	Model      string `json:"model,omitempty"`

	FreshInput int `json:"fresh_input"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Output     int `json:"output"`

	CostUSD               *float64 `json:"cost_usd,omitempty"`
	CostUnavailableReason string   `json:"cost_unavailable_reason,omitempty"`

	Available bool `json:"available"`

	// UnavailableReason names the adapter and why Available is false.
	UnavailableReason string `json:"unavailable_reason,omitempty"`

	WallSeconds float64 `json:"wall_seconds"`
	Trigger     string  `json:"trigger"`
	From        string  `json:"from,omitempty"`
	To          string  `json:"to,omitempty"`

	// ModelCalls is the row's own delta of model requests; Cumulative.ModelCalls
	// is the session's absolute count when it was read (what a gate wait diffs).
	// Both are nil when the harness reports none.
	ModelCalls                  *int   `json:"model_calls,omitempty"`
	ModelCallsUnavailableReason string `json:"model_calls_unavailable_reason,omitempty"`
	Cumulative                  struct {
		ModelCalls *int `json:"model_calls,omitempty"`
	} `json:"cumulative"`
	// Turns is the harness turn count the cumulative reflects; Unflushed marks a
	// harness that records a turn's usage only when the turn ends.
	Turns     int  `json:"turns,omitempty"`
	Unflushed bool `json:"unflushed,omitempty"`
	// BaselineFresh marks a row whose zero delta is the baseline being set (the
	// session's first available reading), not a measured zero: its token and cost
	// figures are unreported, and it is not counted as a measured usage row.
	BaselineFresh bool `json:"baseline_fresh,omitempty"`
	// Backfilled marks a row written after the fact from the session record's
	// per-message timestamps: a measured figure, but derived — never a live delta.
	Backfilled bool `json:"backfilled,omitempty"`
	// Overlaps names the other stories that drove this row's session in a window
	// intersecting this story's, so the session's usage cannot be split between them;
	// AttributionReason is the adapter-named sentence saying so, and — alone, with no
	// Overlaps — the adapter's qualifier that a figure may leave subagent spend out.
	Overlaps          []string `json:"overlaps,omitempty"`
	AttributionReason string   `json:"attribution_reason,omitempty"`
	// Superseded is set by MarkSuperseded, never stored: an unavailable row that a
	// later backfill row for the same session recovered. It keeps its wall time and
	// drops out of the measured/unavailable counts.
	Superseded bool `json:"-"`
}

// BackfilledLabel is how every surface says a backfilled figure is not a live delta.
const BackfilledLabel = "derived from timestamps, not a live delta"

// MarkSuperseded flags, in rows (one story's driver rows, oldest first), each
// unavailable row that a later available backfill row for the same session recovers.
// The ledger is append-only, so the old unavailable row stays; what it could not read
// is no longer a gap once the backfill has.
func MarkSuperseded(rows []DriverRow) []DriverRow {
	recoveredAt := map[string]int{} // session -> index of its last available backfill row
	for i, d := range rows {
		if d.Backfilled && d.Available {
			recoveredAt[d.SessionID] = i
		}
	}
	for i := range rows {
		if at, ok := recoveredAt[rows[i].SessionID]; ok && i < at && !rows[i].Available {
			rows[i].Superseded = true
		}
	}
	return rows
}

// AmbiguousLabel is how every surface says a row's figure is the whole session's
// usage over a window another story shared: available, but not this story's alone.
const AmbiguousLabel = "shared session window, usage not split"

// Ambiguous reports whether d is the one classification every surface reads for
// "available but not exact": its session was driven by another story in an
// intersecting window. An ambiguous row keeps its figures and is never summed into
// the exact token and cost totals. A fresh-baseline row is never ambiguous: its zero
// delta is a named baseline gap (BaselineFreshReason), not a figure to label.
func (d DriverRow) Ambiguous() bool {
	return d.Available && !d.BaselineFresh && len(d.Overlaps) > 0
}

// measured reports whether d carries a token delta that was actually measured, and
// is exact: an available row that is neither a fresh baseline nor ambiguous.
func (d DriverRow) measured() bool { return d.Available && !d.BaselineFresh && !d.Ambiguous() }

// BaselineFreshReason is the adapter-named reason a fresh-baseline row reports in
// place of a delta: nothing was measured between two reads, the baseline was set.
func BaselineFreshReason(executable string) string {
	return executable + ": first available reading for this session — baseline set, no delta measured"
}

// unmeasuredReason names why an unmeasured row reports no delta: the adapter's
// unavailable reason, or the baseline being set.
func (d DriverRow) unmeasuredReason() string {
	if d.BaselineFresh {
		return BaselineFreshReason(d.Executable)
	}
	return d.UnavailableReason
}

// DecodeDriverRow decodes one driver_usage ledger entry into a DriverRow, or
// ok=false when e carries no payload or fails to decode.
func DecodeDriverRow(e ledger.Entry) (DriverRow, bool) {
	if len(e.Payload) == 0 {
		return DriverRow{}, false
	}
	var d DriverRow
	if err := json.Unmarshal(e.Payload, &d); err != nil {
		return DriverRow{}, false
	}
	return d, true
}

// accumulator folds ledger rows into Figures. Kept as its own type (rather
// than inline in Own) so a dollar/token/time field added to one accumulation
// point cannot silently miss the other.
type accumulator struct {
	costUSD             float64
	costRows            int
	costUnavailableRows int

	freshInput, output, cacheRead, cacheWrite, unsplit int

	usageRows, usageUnavailableRows, unsplitRows int

	dispatchMs, driverMs int64

	ambiguousTokens  int
	ambiguousCostUSD float64

	driver []DriverCoverage

	// price prices each row at the exact point its tokens fold in, so the
	// counting rule below (unavailable, baseline and superseded rows add
	// nothing) is the only one: a row that adds no tokens can never be priced.
	price pricer
}

// addDispatchRow folds one agent_invocation row (already tool-permission-
// filtered by DecodeRow). DurationMs and cost fold in regardless of token
// usage availability — an agent still spent wall time and may still have a
// priced cost even when its provider reported no token split. Token figures
// fold in only from a measured row — never a zero for an unreported one.
func (a *accumulator) addDispatchRow(row Row) {
	a.dispatchMs += row.DurationMs
	if row.CostUSD != nil {
		a.costUSD += *row.CostUSD
		a.costRows++
	} else {
		a.costUnavailableRows++
	}
	if !row.UsageAvailable {
		a.usageUnavailableRows++
		return
	}
	a.usageRows++
	if row.TokensInFresh > 0 || row.TokensCacheWrite > 0 || row.TokensCacheRead > 0 {
		a.freshInput += row.TokensInFresh
		a.cacheWrite += row.TokensCacheWrite
		a.cacheRead += row.TokensCacheRead
	} else {
		a.unsplit += row.TokensIn
		a.unsplitRows++
	}
	a.output += row.TokensOut
	a.price.dispatch(row)
}

// addDriverRow folds one driver_usage row. WallSeconds is real elapsed time
// recorded regardless of whether the harness's usage read succeeded, so it
// always folds in; the token/cost fields only ever carry non-zero values on
// an Available row.
func (a *accumulator) addDriverRow(d DriverRow) {
	a.driverMs += int64(d.WallSeconds * 1000)
	if d.Superseded {
		return // wall time only: the backfill row that recovered it carries the figures
	}
	a.driver = addCoverage(a.driver, d)
	if d.Ambiguous() {
		// Available, but the session's usage over the window is shared with another
		// story: kept beside the exact figures, never in them.
		a.ambiguousTokens += d.FreshInput + d.CacheRead + d.CacheWrite + d.Output
		if d.CostUSD != nil {
			a.ambiguousCostUSD += *d.CostUSD
		}
		return
	}
	if d.CostUSD != nil {
		a.costUSD += *d.CostUSD
		a.costRows++
	} else {
		a.costUnavailableRows++
	}
	if !d.measured() {
		a.usageUnavailableRows++
		return
	}
	a.usageRows++
	a.freshInput += d.FreshInput
	a.output += d.Output
	a.cacheRead += d.CacheRead
	a.cacheWrite += d.CacheWrite
	a.price.driver(d)
}

func (a *accumulator) figures() Figures {
	f := Figures{
		CostUSD:              a.costUSD,
		CostRows:             a.costRows,
		CostUnavailableRows:  a.costUnavailableRows,
		FreshInput:           a.freshInput,
		Output:               a.output,
		CacheRead:            a.cacheRead,
		CacheWrite:           a.cacheWrite,
		UnsplitTokens:        a.unsplit,
		UsageRows:            a.usageRows,
		UsageUnavailableRows: a.usageUnavailableRows,
		UnsplitRows:          a.unsplitRows,
		AgentMs:              a.dispatchMs + a.driverMs,
		DispatchMs:           a.dispatchMs,
		DriverMs:             a.driverMs,
		AmbiguousTokens:      a.ambiguousTokens,
		AmbiguousCostUSD:     a.ambiguousCostUSD,
		Driver:               foldCoverage(nil, a.driver),
		DriverStatus:         driverStatus(a.driver),
	}
	f.Derived = a.price.result(f)
	return f
}

// Story is one item's own computed cost view: its Figures, the dispatch Rows
// and DriverRows that fed them, and its own wall-clock Span. Span is nil when
// the item has never entered an engaging state (its clock has not started —
// see Own), so a family walk never folds a not-started item's window into the
// union as though it had one. Built by Own.
type Story struct {
	ID         string      `json:"id"`
	Figures    Figures     `json:"figures"`
	Rows       []Row       `json:"rows,omitempty"`
	DriverRows []DriverRow `json:"driver_rows,omitempty"`
	Span       *Span       `json:"-"`
	Estimates  []Estimate  `json:"estimates,omitempty"`
	// Seats is the per-agent+model token and derived-cost breakdown of this
	// item's own rows (BySeat).
	Seats []SeatLine `json:"seats,omitempty"`
}

// StoryWindow returns the story clock's engage and terminal timestamps from entries'
// status transitions: the first transition INTO a state clk.Engaging accepts and the
// first INTO a state clk.Terminal accepts. have* is false for one the ledger has no
// transition to prove — a window is never guessed from the item's current status.
func StoryWindow(entries []ledger.Entry, clk Clock) (engageAt, terminalAt time.Time, haveEngage, haveTerminal bool) {
	for _, e := range entries {
		if e.Kind != ledger.KindStatusTransition {
			continue
		}
		var p struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		if !haveEngage && clk.Engaging != nil && clk.Engaging(p.To) {
			engageAt, haveEngage = e.CreatedAt, true
		}
		if !haveTerminal && clk.Terminal != nil && clk.Terminal(p.To) {
			terminalAt, haveTerminal = e.CreatedAt, true
		}
	}
	return
}

// Own reads item's ledger entries and returns its own Story — figures, rows,
// driver rows, span and parsed estimates — with no child rollup. The story
// clock is derived from clk, never from a status literal: it starts at the
// first status_transition INTO a state clk.Engaging accepts, ends at the
// first status_transition INTO a state clk.Terminal accepts (a park/blocked
// transition does NOT stop the clock, since the story is still open), or now
// when it has not yet reached one.
//
// haveEngage is proven ONLY by a logged engage transition — never guessed at
// from item's current status. A story with no such transition has never
// engaged as far as the ledger can prove, whatever its present status: a
// story cancelled straight out of backlog (backlog -> cancelled, no engaging
// state ever entered) and a legacy item whose transition history predates
// this accounting (done/cancelled with no transitions logged at all) both
// read as unavailable rather than a fabricated CreatedAt-based span — item's
// CreatedAt is never a stand-in for when work actually started.
//
// WithPricing prices the measured tokens under the operator's [pricing]
// configuration at read time; without it the derived cost reads unavailable.
func Own(item workitem.Item, entries []ledger.Entry, clk Clock, now time.Time, opts ...Option) Story {
	p := pricingOf(opts)
	acc := &accumulator{price: newPricer(p)}
	var rows []Row
	var driverRows []DriverRow

	for _, e := range entries {
		switch e.Kind {
		case ledger.KindAgentInvocation:
			row, ok := DecodeRow(e)
			if !ok {
				continue
			}
			acc.addDispatchRow(row)
			rows = append(rows, row)
		case ledger.KindDriverUsage:
			d, ok := DecodeDriverRow(e)
			if !ok {
				continue
			}
			driverRows = append(driverRows, d)
		}
	}
	driverRows = MarkSuperseded(driverRows)
	for _, d := range driverRows {
		acc.addDriverRow(d)
	}
	engageAt, terminalAt, haveEngage, haveTerminal := StoryWindow(entries, clk)

	figures := acc.figures()
	var span *Span
	if haveEngage {
		end := now
		if haveTerminal {
			end = terminalAt
		}
		if end.After(engageAt) {
			figures.ElapsedMs = end.Sub(engageAt).Milliseconds()
		}
		span = &Span{Start: engageAt, End: end}
	} else {
		figures.ElapsedMs = -1 // never engaged — not started, not a measured zero
	}
	return Story{
		ID:         item.ID,
		Figures:    figures,
		Rows:       rows,
		DriverRows: driverRows,
		Span:       span,
		Estimates:  ParseEstimates(item.Tags),
		Seats:      BySeat(rows, driverRows, p),
	}
}
