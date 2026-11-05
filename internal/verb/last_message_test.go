package verb_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A performer's agent_invocation row carries the tail of what it last said, so a
// headless drive that stops can quote it (sty_aaafe4e2). Only performer rows do.

func lastMessageOf(t *testing.T, row ledger.Entry) (string, bool) {
	t.Helper()
	var p map[string]json.RawMessage
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		t.Fatalf("row %s payload: %v", row.Kind, err)
	}
	raw, ok := p["last_message"]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("last_message is not a string: %s", raw)
	}
	return s, true
}

func rowsOf(t *testing.T, db *store.DB, id, kind string) []ledger.Entry {
	t.Helper()
	rows, err := db.Ledger.ListByStory(context.Background(), id, kind)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func newStoryAt(t *testing.T, status string) workitem.Item {
	t.Helper()
	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "x", "status": status}), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestLastMessageIsTheUTF8SafeTail(t *testing.T) {
	if got := verb.LastMessage("short"); got != "short" {
		t.Errorf("a short output is kept whole, got %q", got)
	}
	long := strings.Repeat("x", 5000) + "THE END"
	got := verb.LastMessage(long)
	if len(got) != verb.LastMessageMax || !strings.HasSuffix(got, "THE END") {
		t.Errorf("tail = %d bytes ending %q, want %d bytes ending the output", len(got), got[len(got)-7:], verb.LastMessageMax)
	}
	// A cut through a multi-byte rune drops its remaining bytes rather than storing invalid text.
	multi := strings.Repeat("é", 1500)
	if got := verb.LastMessage(multi); !utf8.ValidString(got) || len(got) > verb.LastMessageMax {
		t.Errorf("tail of multi-byte output = %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}

// A resolved env value in a performer's output reaches the ledger as a marker
// naming its key, never as the value; values under RedactMinLen are left alone.
func TestOutputTailScrubsResolvedEnvValues(t *testing.T) {
	env := map[string]string{"TOKEN": "s3cr3t-LOCAL", "OTHER": "s3cr3t", "EMPTY": "", "FLAG": "1", "REGION": "ap"}
	out := "OBSERVED-TOKEN=s3cr3t-LOCAL and s3cr3t again, flag 1 in ap-southeast"
	got := verb.OutputTail(out, env)
	want := "OBSERVED-TOKEN=[redacted:TOKEN] and [redacted:OTHER] again, flag 1 in ap-southeast"
	if got != want {
		t.Errorf("OutputTail = %q, want %q", got, want)
	}
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("a resolved value survived the scrub: %q", got)
	}
	// Scrub before cap: a secret straddling the cap boundary leaves no fragment.
	secret := "zzTOPSECRETzz"
	long := strings.Repeat("x", verb.LastMessageMax-5) + secret + " end"
	if tail := verb.OutputTail(long, map[string]string{"K": secret}); strings.Contains(tail, "TOPSECRET") || strings.Contains(tail, "SECRETzz") {
		t.Errorf("a secret cut by the cap leaked a fragment: %q", tail[len(tail)-40:])
	}
	if len(verb.OutputTail(long, map[string]string{"K": secret})) > verb.LastMessageMax {
		t.Error("the scrubbed tail exceeds the cap")
	}
}

// AC2: dispatchPerformer's row carries the output tail; the transition row beside it none.
func TestDispatchPerformerCarriesLastMessage(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetExecutorDispatcher(&dispatcherStub{res: verb.DispatchResult{
		Dispatched: true, Agent: "coder", Command: "fake {system}", LastMessage: verb.LastMessage(strings.Repeat("a", 3000) + "Should I use X or Y?"),
	}})
	it := newStoryAt(t, "backlog")
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}); err != nil {
		t.Fatal(err)
	}
	var performer int
	for _, row := range rowsOf(t, db, it.ID, ledger.KindAgentInvocation) {
		got, ok := lastMessageOf(t, row)
		if !ok {
			continue
		}
		performer++
		if len(got) != verb.LastMessageMax || !strings.HasSuffix(got, "Should I use X or Y?") {
			t.Errorf("last_message = %d bytes ending %q, want the 2000-byte tail", len(got), got[len(got)-20:])
		}
	}
	if performer != 1 {
		t.Errorf("want exactly one performer row with last_message, got %d", performer)
	}
}

// A performer that printed nothing still writes the key: it is what marks the row a performer's.
func TestDispatchPerformerSilentRunStillWritesTheKey(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetExecutorDispatcher(&dispatcherStub{res: verb.DispatchResult{Dispatched: true, Agent: "coder"}})
	quiet := newStoryAt(t, "backlog")
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": quiet.ID, "status": "in_progress"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range rowsOf(t, db, quiet.ID, ledger.KindAgentInvocation) {
		if got, ok := lastMessageOf(t, row); !ok || got != "" {
			t.Errorf("a silent performer's row = %q (present %v), want the key present and empty", got, ok)
		}
	}
}

// AC2: a failed dispatch's audit row carries what the performer printed before it failed.
func TestRecordDispatchFailureCarriesLastMessage(t *testing.T) {
	withWiring(t)
	db := wire(t)
	verb.SetExecutorDispatcher(&dispatcherStub{
		res: verb.DispatchResult{Dispatched: true, Agent: "coder", LastMessage: "half way there"},
		err: errors.New("named agent coder failed"),
	})
	it := newStoryAt(t, "backlog")
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"}); err == nil {
		t.Fatal("a failed dispatch refuses the transition")
	}
	var failed int
	for _, row := range rowsOf(t, db, it.ID, ledger.KindAgentInvocation) {
		if !strings.Contains(row.Body, "dispatch failed") {
			continue
		}
		failed++
		if got, ok := lastMessageOf(t, row); !ok || got != "half way there" {
			t.Errorf("failure row last_message = %q (present %v), want the output", got, ok)
		}
	}
	if failed != 1 {
		t.Fatalf("want one failure row, got %d", failed)
	}
}

// AC2: RecordStepPerformance's success and failure rows carry the output tail.
func TestRecordStepPerformanceCarriesLastMessage(t *testing.T) {
	withWiring(t)
	db := wire(t)
	it := newStoryAt(t, "in_progress")
	res := verb.DispatchResult{Dispatched: true, Agent: "executor", Command: "fake", LastMessage: verb.LastMessage(strings.Repeat("b", 2500) + "TAIL")}
	verb.RecordStepPerformance(context.Background(), it, "in_progress", res, nil)
	verb.RecordStepPerformance(context.Background(), it, "in_progress", verb.DispatchResult{LastMessage: "died here"}, errors.New("rate limited"))
	rows := rowsOf(t, db, it.ID, ledger.KindAgentInvocation)
	if len(rows) != 2 {
		t.Fatalf("want a success and a failure row, got %d", len(rows))
	}
	if got, ok := lastMessageOf(t, rows[0]); !ok || len(got) != verb.LastMessageMax || !strings.HasSuffix(got, "TAIL") {
		t.Errorf("success row last_message = %d bytes (present %v), want the 2000-byte tail", len(got), ok)
	}
	if got, ok := lastMessageOf(t, rows[1]); !ok || got != "died here" {
		t.Errorf("failure row last_message = %q (present %v), want the output", got, ok)
	}
}

// sty_23ceeca2: a headless executor step is judged as a dispatched step is — a gap
// is shown, and refuses the run only under skill_evidence = reject — and its run
// stream is recorded as one tool_trace row for the step's exit gate.
func TestRecordStepPerformanceAppliesTheEvidenceSettingAndRecordsTheStream(t *testing.T) {
	withWiring(t)
	db := wire(t)
	it := newStoryAt(t, "in_progress")
	gap := &verb.EvidenceReport{Items: []verb.EvidenceItem{{Skill: "ev", Kind: verb.EvidenceCommand, Name: "go test", Status: verb.EvidenceMissing}}}
	trace := &verb.RunTrace{
		Adapter: "claude", CommandsCovered: true, ToolsCovered: true,
		Calls: []verb.TraceCall{{Tool: "Bash", Command: "go vet ./..."}},
	}
	res := verb.DispatchResult{Dispatched: true, Agent: "executor", Command: "fake", Evidence: gap, Trace: trace}

	if err := verb.RecordStepPerformance(context.Background(), it, "in_progress", res, nil); err != nil {
		t.Fatalf("under record a gap is shown, not refused: %v", err)
	}
	gap.Strict = true
	err := verb.RecordStepPerformance(context.Background(), it, "in_progress", res, nil)
	if err == nil || !strings.Contains(err.Error(), `missing command "go test"`) {
		t.Fatalf("under reject the run is refused naming the gap, got %v", err)
	}
	rows := rowsOf(t, db, it.ID, ledger.KindToolTrace)
	if len(rows) != 2 {
		t.Fatalf("want one stream row per recorded run, got %d", len(rows))
	}
	var p verb.ToolTracePayload
	if err := json.Unmarshal(rows[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Source != verb.ToolTraceStream || !p.CommandsCovered || len(p.Calls) != 1 || p.Calls[0].Command != "go vet ./..." {
		t.Errorf("stream payload = %+v, want the run's calls with their coverage", p)
	}
	res.Trace = nil // a step that declares nothing carries no trace
	_ = verb.RecordStepPerformance(context.Background(), it, "in_progress", res, nil)
	if got := len(rowsOf(t, db, it.ID, ledger.KindToolTrace)); got != 2 {
		t.Errorf("a run without a trace must add no tool_trace row, have %d", got)
	}
}

// invokingSummariser returns a summary that billed a summariser invocation.
type invokingSummariser struct{}

func (invokingSummariser) Summarise(context.Context, workitem.Item, string, string) (verb.SummaryResult, error) {
	return verb.SummaryResult{Text: "summary", Command: "fake-summariser", Context: "satelle-step-summary"}, nil
}
func (invokingSummariser) MandatorySummary(context.Context, workitem.Item) bool { return false }
