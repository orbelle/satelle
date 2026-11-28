package kernel

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/ledger"
)

var judgeT0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func judgeEvent(t *testing.T, item string, minute int, r gate.Result) ledger.Event {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Event{Item: item, Kind: gate.KindRun, At: judgeT0.Add(time.Duration(minute) * time.Minute), Payload: b}
}

func judgeRow(t *testing.T, rows []JudgeRow, g, model string) JudgeRow {
	t.Helper()
	for _, r := range rows {
		if r.Gate == g && r.Model == model {
			return r
		}
	}
	t.Fatalf("no row %s/%s in %+v", g, model, rows)
	return JudgeRow{}
}

func TestJudgeRowsCountPerGateAndModel(t *testing.T) {
	one := func(model, result, cov string, in, out int, ms int64) gate.Result {
		return gate.Result{Gate: "code", Binding: "b-" + model, Model: model, Result: result, Coverage: cov, TokensIn: in, TokensOut: out, DurationMS: ms}
	}
	panel := gate.Result{Gate: "sec", Binding: "p1,p2", Result: gate.Accept, Coverage: "c1", Members: []gate.Member{
		{Binding: "p1", Model: "m-a", Result: gate.Accept, TokensIn: 100, TokensOut: 10, DurationMS: 3000},
		{Binding: "p2", Model: "m-b", Result: gate.Accept, TokensIn: 200, TokensOut: 20, DurationMS: 5000},
	}}
	rows := judgeRows([][]ledger.Event{
		{judgeEvent(t, "i1", 0, one("m-a", gate.Reject, "c1", 10, 1, 100)), judgeEvent(t, "i1", 1, one("m-a", gate.Accept, "c2", 30, 3, 300)), judgeEvent(t, "i1", 2, panel)},
		{judgeEvent(t, "i2", 0, one("m-b", gate.Error, "c9", 5, 0, 50)), judgeEvent(t, "i2", 1, gate.Result{Gate: "fmt", Result: gate.Accept}), judgeEvent(t, "i2", 2, gate.Result{Gate: "code", Result: gate.Carried})},
	}, time.Time{}, time.Time{})
	want := []JudgeRow{
		{Gate: "code", Model: "m-a", Runs: 2, Accepts: 1, Rejects: 1, ConfirmedCatches: 1, Measured: 2, TokensIn: 40, TokensOut: 4, MedianDurationMS: 200},
		{Gate: "code", Model: "m-b", Runs: 1, Errors: 1, Measured: 1, TokensIn: 5, MedianDurationMS: 50},
		{Gate: "sec", Model: "m-a", Runs: 1, Accepts: 1, Measured: 1, TokensIn: 100, TokensOut: 10, MedianDurationMS: 3000},
		{Gate: "sec", Model: "m-b", Runs: 1, Accepts: 1, Measured: 1, TokensIn: 200, TokensOut: 20, MedianDurationMS: 5000},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v, want %+v", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestJudgeRowsReportAMissingModelAsUnknown(t *testing.T) {
	rows := judgeRows([][]ledger.Event{{
		judgeEvent(t, "i1", 0, gate.Result{Gate: "g", Binding: "judge-yes", Result: gate.Accept}),
		judgeEvent(t, "i1", 1, gate.Result{Gate: "g", Binding: "a,b", Result: gate.Accept, Members: []gate.Member{{Binding: "a", Result: gate.Accept}, {Binding: "b", Model: "m-b", Result: gate.Accept}}}),
	}}, time.Time{}, time.Time{})
	if r := judgeRow(t, rows, "g", UnknownModel); r.Runs != 2 || r.Accepts != 2 {
		t.Errorf("unknown row %+v, want the model-less run and member", r)
	}
	if r := judgeRow(t, rows, "g", "m-b"); r.Runs != 1 {
		t.Errorf("m-b row %+v", r)
	}
	if len(rows) != 2 {
		t.Errorf("rows %+v, want only unknown and m-b", rows)
	}
}

func TestJudgeRowsCountOnlyTheWindow(t *testing.T) {
	events := [][]ledger.Event{{
		judgeEvent(t, "i1", 0, gate.Result{Gate: "g", Binding: "j", Model: "m", Result: gate.Accept}),
		judgeEvent(t, "i1", 10, gate.Result{Gate: "g", Binding: "j", Model: "m", Result: gate.Accept}),
		judgeEvent(t, "i1", 20, gate.Result{Gate: "g", Binding: "j", Model: "m", Result: gate.Accept}),
	}}
	for _, c := range []struct {
		name         string
		since, until time.Time
		runs         int
	}{
		{"unbounded", time.Time{}, time.Time{}, 3},
		{"since is inclusive", judgeT0.Add(10 * time.Minute), time.Time{}, 2},
		{"until is exclusive", time.Time{}, judgeT0.Add(10 * time.Minute), 1},
		{"both", judgeT0.Add(5 * time.Minute), judgeT0.Add(20 * time.Minute), 1},
		{"empty", judgeT0.Add(30 * time.Minute), time.Time{}, 0},
	} {
		rows := judgeRows(events, c.since, c.until)
		runs := 0
		for _, r := range rows {
			runs += r.Runs
		}
		if runs != c.runs {
			t.Errorf("%s: %d runs, want %d (%+v)", c.name, runs, c.runs, rows)
		}
	}
}

func TestJudgeRowsClassifyRejects(t *testing.T) {
	res := func(g, result, cov string) gate.Result {
		return gate.Result{Gate: g, Binding: "j", Model: "m", Result: result, Coverage: cov}
	}
	panel := func(result, cov string, members ...gate.Member) gate.Result {
		return gate.Result{Gate: "p", Binding: "x,y,z", Result: result, Coverage: cov, Members: members}
	}
	mem := func(model, result string) gate.Member {
		return gate.Member{Binding: model, Model: model, Result: result}
	}
	for _, c := range []struct {
		name               string
		events             [][]ledger.Event
		until              time.Time
		model              string
		catches, overturns int
	}{
		{"changed coverage is a catch", [][]ledger.Event{{judgeEvent(t, "i", 0, res("g", gate.Reject, "A")), judgeEvent(t, "i", 1, res("g", gate.Accept, "B"))}}, time.Time{}, "m", 1, 0},
		{"same coverage is overturned", [][]ledger.Event{{judgeEvent(t, "i", 0, res("g", gate.Reject, "A")), judgeEvent(t, "i", 1, res("g", gate.Accept, "A"))}}, time.Time{}, "m", 0, 1},
		{"each reject meets the next accept", [][]ledger.Event{{judgeEvent(t, "i", 0, res("g", gate.Reject, "A")), judgeEvent(t, "i", 1, res("g", gate.Reject, "B")), judgeEvent(t, "i", 2, res("g", gate.Accept, "B"))}}, time.Time{}, "m", 1, 1},
		{"no later accept is neither", [][]ledger.Event{{judgeEvent(t, "i", 0, res("g", gate.Reject, "A"))}}, time.Time{}, "m", 0, 0},
		{"errors, skips and carries are passed over", [][]ledger.Event{{
			judgeEvent(t, "i", 0, res("g", gate.Reject, "A")), judgeEvent(t, "i", 1, res("g", gate.Error, "B")),
			judgeEvent(t, "i", 2, gate.Result{Gate: "g", Result: gate.Skipped}), judgeEvent(t, "i", 3, gate.Result{Gate: "g", Result: gate.Carried, Coverage: "A"}),
			judgeEvent(t, "i", 4, res("other", gate.Accept, "A")), judgeEvent(t, "i", 5, res("g", gate.Accept, "B")),
		}}, time.Time{}, "m", 1, 0},
		{"a member rejecting in an accepted panel is overturned", [][]ledger.Event{{judgeEvent(t, "i", 0, panel(gate.Accept, "A", mem("mx", gate.Accept), mem("my", gate.Accept), mem("mz", gate.Reject)))}}, time.Time{}, "mz", 0, 1},
		{"a panel reject then a changed accept is a catch per rejecting member", [][]ledger.Event{{
			judgeEvent(t, "i", 0, panel(gate.Reject, "A", mem("mx", gate.Reject), mem("my", gate.Accept))),
			judgeEvent(t, "i", 1, panel(gate.Accept, "B", mem("mx", gate.Accept), mem("my", gate.Accept))),
		}}, time.Time{}, "mx", 1, 0},
		{"another item does not resolve it", [][]ledger.Event{{judgeEvent(t, "i", 0, res("g", gate.Reject, "A"))}, {judgeEvent(t, "j", 1, res("g", gate.Accept, "B"))}}, time.Time{}, "m", 0, 0},
		{"an accept after the window still resolves it", [][]ledger.Event{{judgeEvent(t, "i", 0, res("g", gate.Reject, "A")), judgeEvent(t, "i", 30, res("g", gate.Accept, "B"))}}, judgeT0.Add(10 * time.Minute), "m", 1, 0},
	} {
		catches, overturns := 0, 0
		for _, r := range judgeRows(c.events, time.Time{}, c.until) {
			if r.Model == c.model {
				catches += r.ConfirmedCatches
				overturns += r.OverturnedRejects
			}
		}
		if catches != c.catches || overturns != c.overturns {
			t.Errorf("%s: %d catches, %d overturned; want %d, %d", c.name, catches, overturns, c.catches, c.overturns)
		}
	}
}

func TestJudgeRowsAttributeMemberTokens(t *testing.T) {
	measured := func(ms ...int64) gate.Result {
		r := gate.Result{Gate: "p", Binding: "a,b", Result: gate.Accept}
		for i, d := range ms {
			r.Members = append(r.Members, gate.Member{Binding: "m", Model: []string{"m-a", "m-b"}[i], Result: gate.Accept, TokensIn: 10 * (i + 1), TokensOut: i + 1, DurationMS: d})
		}
		return r
	}
	old := gate.Result{Gate: "p", Binding: "a,b", Result: gate.Accept, TokensIn: 999, TokensOut: 99, DurationMS: 9999,
		Members: []gate.Member{{Binding: "a", Model: "m-a", Result: gate.Accept}, {Binding: "b", Model: "m-b", Result: gate.Accept}}}
	rows := judgeRows([][]ledger.Event{{
		judgeEvent(t, "i", 0, measured(1000, 4000)),
		judgeEvent(t, "i", 1, measured(3000, 6000)),
		judgeEvent(t, "i", 2, measured(2000, 5000)),
		judgeEvent(t, "i", 3, old),
	}}, time.Time{}, time.Time{})
	a := judgeRow(t, rows, "p", "m-a")
	if a.Runs != 4 || a.Measured != 3 || a.TokensIn != 30 || a.TokensOut != 3 || a.MedianDurationMS != 2000 {
		t.Errorf("m-a %+v, want 4 runs, 3 measured, 30/3 tokens, median 2000", a)
	}
	b := judgeRow(t, rows, "p", "m-b")
	if b.Runs != 4 || b.Measured != 3 || b.TokensIn != 60 || b.TokensOut != 6 || b.MedianDurationMS != 5000 {
		t.Errorf("m-b %+v, want 4 runs, 3 measured, 60/6 tokens, median 5000", b)
	}
	if got := median([]int64{4, 1, 3, 2}); got != 2 {
		t.Errorf("even median %d, want 2", got)
	}
	if got := median(nil); got != 0 {
		t.Errorf("empty median %d, want 0", got)
	}
}

func TestUsageRowsCountPerHarnessModelAndGate(t *testing.T) {
	fell := &gate.FallbackFrom{Binding: "agy-rev", Command: "agy", Model: "gemini-x"}
	one := func(cmd, model, result string, ms int64, fb *gate.FallbackFrom) gate.Result {
		return gate.Result{Gate: "code", Binding: "b", Command: cmd, Model: model, Result: result, DurationMS: ms, FallbackFrom: fb}
	}
	panel := gate.Result{Gate: "sec", Binding: "p1,p2", Result: gate.Reject, DurationMS: 99000, Members: []gate.Member{
		{Binding: "p1", Command: "agy", Model: "gemini-x", Result: gate.Reject, DurationMS: 3000},
		{Binding: "p2", Command: "cursor-agent", Model: "gemini-x", Result: gate.Accept, DurationMS: 5000, FallbackFrom: fell},
	}}
	rows := usageRows([][]ledger.Event{
		{
			judgeEvent(t, "i1", 0, one("agy", "gemini-x", gate.Accept, 3000, nil)),
			judgeEvent(t, "i1", 1, one("/usr/bin/agy --x", "gemini-x", gate.Reject, 5000, nil)),
			judgeEvent(t, "i1", 2, one("cursor-agent", "gemini-x", gate.Accept, 4000, fell)),
			judgeEvent(t, "i1", 3, panel),
		},
		{
			judgeEvent(t, "i2", 0, one("", "", gate.Error, 700, nil)),
			judgeEvent(t, "i2", 1, gate.Result{Gate: "fmt", Result: gate.Accept, DurationMS: 10}),
			judgeEvent(t, "i2", 2, gate.Result{Gate: "code", Result: gate.Carried}),
		},
	}, time.Time{}, time.Time{})
	want := []UsageRow{
		{Harness: "agy", Model: "gemini-x", Gate: "code", Runs: 2, Accepts: 1, Rejects: 1, DurationMS: 8000},
		{Harness: "agy", Model: "gemini-x", Gate: "sec", Runs: 1, Rejects: 1, DurationMS: 3000},
		{Harness: "cursor-agent", Model: "gemini-x", Gate: "code", Runs: 1, Accepts: 1, Fallbacks: 1, DurationMS: 4000},
		{Harness: "cursor-agent", Model: "gemini-x", Gate: "sec", Runs: 1, Accepts: 1, Fallbacks: 1, DurationMS: 5000},
		{Harness: UnknownModel, Model: UnknownModel, Gate: "code", Runs: 1, Errors: 1, DurationMS: 700},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v, want %+v", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestUsageRowsCountOnlyTheWindow(t *testing.T) {
	run := gate.Result{Gate: "g", Binding: "j", Command: "agy", Model: "m", Result: gate.Accept}
	events := [][]ledger.Event{{
		judgeEvent(t, "i1", -30*60, run),
		judgeEvent(t, "i1", -2*60, run),
		judgeEvent(t, "i1", -30, run),
		judgeEvent(t, "i1", 0, run),
	}}
	for _, c := range []struct {
		name         string
		since, until time.Time
		runs         int
	}{
		{"the last 24 hours", judgeT0.Add(-24 * time.Hour), time.Time{}, 3},
		{"the last hour", judgeT0.Add(-time.Hour), time.Time{}, 2},
		{"until is exclusive", judgeT0.Add(-24 * time.Hour), judgeT0, 2},
		{"since is inclusive", judgeT0.Add(-2 * time.Hour), time.Time{}, 3},
	} {
		runs := 0
		for _, r := range usageRows(events, c.since, c.until) {
			runs += r.Runs
		}
		if runs != c.runs {
			t.Errorf("%s: %d runs, want %d", c.name, runs, c.runs)
		}
	}
}
