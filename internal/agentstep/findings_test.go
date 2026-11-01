package agentstep

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_f948d345: the performer of a re-presented edge is handed each gate seat's
// latest verdict as findings_to_answer, and its answers are recorded and told to
// the gates.

func priorResolver(rows ...PriorVerdict) func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
	return func(_ context.Context, _, _, _ string, _ verb.StaleExemption) []PriorVerdict {
		return append([]PriorVerdict(nil), rows...)
	}
}

func findingsOf(t *testing.T, rows ...PriorVerdict) []Finding {
	t.Helper()
	g, _ := newEngine(t, "", fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetPriorVerdictsResolver(priorResolver(rows...))
	tp, _ := g.performerPayload(context.Background(), workitem.Item{ID: "sty_f", Status: "backlog"}, "plan", "plan", "planner", config.AgentBinding{}, "")
	return tp.Findings
}

func TestPerformerPayloadCarriesEachSeatsLatestVerdict(t *testing.T) {
	older := PriorVerdict{Skill: "story-review", Decision: "reject", Notes: "round one", CreatedAt: "2026-10-21T22:00:00Z"}
	panel := PriorVerdict{Skill: "arch-review", Decision: "reject", Notes: "combined", CreatedAt: "2026-10-21T22:10:00Z", Seats: []verb.PanelSeat{
		{Seat: "opus", Notes: "AC3 is a second owner"},
		{Seat: "grok", Notes: "same: AC3 duplicates fillMessages"},
		{Seat: "gemini", Accept: true, Notes: "fine"},
	}}
	latest := PriorVerdict{Skill: "story-review", Decision: "accept", Notes: "ok now", CreatedAt: "2026-10-21T22:11:00Z"}

	got := findingsOf(t, older, panel, latest)
	if len(got) != 4 {
		t.Fatalf("findings = %+v, want 4 (the story-review latest and three panel seats)", got)
	}
	by := map[string]Finding{}
	for _, f := range got {
		by[f.ID] = f
	}
	// story-review's latest row is its 2nd attempt on the edge (older was 1st);
	// the panel row was the 2nd row overall.
	if f := by["story-review#3"]; f.Decision != "accept" || f.Blocking || f.Notes != "ok now" || f.Attempt != 3 || f.Skill != "story-review" || f.Seat != "" {
		t.Errorf("non-panel latest = %+v", f)
	}
	for seat, want := range map[string]string{"opus": "AC3 is a second owner", "grok": "same: AC3 duplicates fillMessages"} {
		f := by["arch-review/"+seat+"#2"]
		if f.Decision != "reject" || !f.Blocking || f.Notes != want || f.Seat != seat || f.Skill != "arch-review" || f.Attempt != 2 {
			t.Errorf("seat %s = %+v", seat, f)
		}
	}
	if f := by["arch-review/gemini#2"]; f.Decision != "accept" || f.Blocking {
		t.Errorf("accepting seat = %+v", f)
	}
	if _, ok := by["story-review#1"]; ok {
		t.Error("an older row of a skill must not be offered, only its latest")
	}
}

func TestPerformerPayloadHasNoFindingsWhenNothingRejected(t *testing.T) {
	if got := findingsOf(t); got != nil {
		t.Errorf("first presentation findings = %+v, want none", got)
	}
	acc := PriorVerdict{Skill: "story-review", Decision: "accept", Notes: "ok"}
	if got := findingsOf(t, acc); got != nil {
		t.Errorf("an accepted edge has nothing to answer, got %+v", got)
	}
	g, _ := newEngine(t, "", fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	tp, _ := g.performerPayload(context.Background(), workitem.Item{ID: "sty_f", Status: "backlog"}, "plan", "plan", "planner", config.AgentBinding{}, "")
	b, _ := json.Marshal(tp)
	if strings.Contains(string(b), "findings_to_answer") {
		t.Errorf("an unwired resolver must leave the payload unchanged: %s", b)
	}
}

func TestFindingNotesOverBudgetAreMarked(t *testing.T) {
	long := strings.Repeat("n", findingNotesBudget+500)
	got := findingsOf(t,
		PriorVerdict{Skill: "long", Decision: "reject", Notes: long},
		PriorVerdict{Skill: "short", Decision: "reject", Notes: "brief"},
	)
	if len(got) != 2 {
		t.Fatalf("findings = %+v", got)
	}
	l := got[0]
	if !l.NotesTruncated || !strings.Contains(l.Notes, "[truncated]") || !strings.Contains(l.Notes, "satelle ledger list --story sty_f") {
		t.Errorf("over-budget notes must be cut with a marker and a fetch command, got %d bytes ending %q", len(l.Notes), l.Notes[len(l.Notes)-80:])
	}
	if len(l.Notes) >= len(long) {
		t.Errorf("notes were not cut: %d bytes", len(l.Notes))
	}
	if s := got[1]; s.NotesTruncated || s.Notes != "brief" {
		t.Errorf("notes within budget must be untouched, got %+v", s)
	}
}

func TestDispatchRecordsFindingAnswers(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|coder|planning-rubric",
		"done")
	const rubric = "---\nname: planning-rubric\ntype: skill\ndescription: test rubric\n---\nDo the work."
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: rubric, skillFound: true})
	r := &fakeRunner{out: `{"answers":[{"finding":"arch-review/opus#1","answer":"adopted","evidence":"moved the owner"}]}`}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file,grep,list_dir"}, true
	})
	g.SetPriorVerdictsResolver(priorResolver(PriorVerdict{Skill: "arch-review", Decision: "reject", Notes: "combined", Seats: []verb.PanelSeat{
		{Seat: "opus", Notes: "second owner"}, {Seat: "grok", Notes: "same"},
	}}))
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_f", Status: "backlog"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.got.Payload, "findings_to_answer") || !strings.Contains(r.got.Payload, "second owner") {
		t.Errorf("the dispatched payload must name the findings to answer: %s", r.got.Payload)
	}
	if len(res.FindingsOffered) != 2 || res.FindingsOffered[0] != "arch-review/opus#1" || res.FindingsOffered[1] != "arch-review/grok#1" {
		t.Errorf("FindingsOffered = %v", res.FindingsOffered)
	}
	if len(res.Answers) != 1 || res.Answers[0].Finding != "arch-review/opus#1" || res.Answers[0].Answer != agentartifact.AnswerAdopted {
		t.Errorf("Answers = %+v", res.Answers)
	}
}

func TestDispatchRefusesAMalformedAnswersBlock(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|coder|planning-rubric",
		"done")
	const rubric = "---\nname: planning-rubric\ntype: skill\ndescription: test rubric\n---\nDo the work."
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: rubric, skillFound: true})
	r := &fakeRunner{out: `{"answers":[{"finding":"x#1","answer":"maybe"}]}`}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file"}, true
	})
	g.SetPriorVerdictsResolver(priorResolver(PriorVerdict{Skill: "s", Decision: "reject", Notes: "n"}))
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_f", Status: "backlog"}, "in_progress"); err == nil || !strings.Contains(err.Error(), "answers") {
		t.Fatalf("a malformed answers block must fail the dispatch, got %v", err)
	}
}

func dispatchRow(t *testing.T, at time.Time, from, to string, row map[string]any) ledger.Entry {
	t.Helper()
	row["from"], row["to"] = from, to
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Entry{Kind: ledger.KindAgentInvocation, Payload: b, CreatedAt: at}
}

func TestFindingAnswersFromPicksLatestEdgeRow(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	entries := []ledger.Entry{
		dispatchRow(t, t0, "backlog", "plan", map[string]any{"findings_offered": []string{"a#1", "b#1"}}),
		// A later attempt on the same edge: this is the one the gates judge.
		dispatchRow(t, t0.Add(2*time.Hour), "backlog", "plan", map[string]any{
			"findings_offered": []string{"a#2", "b#2", "c#2"},
			"finding_answers": []agentartifact.Answer{
				{Finding: "a#2", Answer: "adopted"}, {Finding: "c#2", Answer: "rebutted", Evidence: "engine.go:1336"},
				{Finding: "zzz#9", Answer: "adopted"}, // not offered: answers nothing
			}}),
		// Another edge, and a reviewer-shaped row: neither counts.
		dispatchRow(t, t0.Add(3*time.Hour), "plan", "in_progress", map[string]any{"findings_offered": []string{"q#1"}}),
		dispatchRow(t, t0.Add(4*time.Hour), "backlog", "plan", map[string]any{"skill": "gate"}),
	}
	fa, ok := FindingAnswersFrom(entries, "backlog", "plan")
	if !ok {
		t.Fatal("no answers reported for an edge whose performer was offered findings")
	}
	if len(fa.Answered) != 2 || fa.Answered[0].Finding != "a#2" || fa.Answered[1].Finding != "c#2" {
		t.Errorf("Answered = %+v", fa.Answered)
	}
	if len(fa.Missing) != 1 || fa.Missing[0] != "b#2" {
		t.Errorf("Missing = %v, want [b#2]", fa.Missing)
	}
	if _, ok := FindingAnswersFrom(entries, "plan", "done"); ok {
		t.Error("an edge no performer was offered findings on must report nothing")
	}
	// A row stored out of order still resolves by creation time, not position.
	rev := []ledger.Entry{entries[1], entries[0]}
	if fa, _ := FindingAnswersFrom(rev, "backlog", "plan"); len(fa.Missing) != 1 || fa.Missing[0] != "b#2" {
		t.Errorf("order of the list changed the pick: %+v", fa)
	}
}

func TestGatePayloadReportsMissingAnswer(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	var gotFrom, gotTo string
	g.SetFindingAnswersResolver(func(_ context.Context, itemID, from, to string) (FindingAnswers, bool) {
		gotFrom, gotTo = from, to
		return FindingAnswers{
			Answered: []agentartifact.Answer{{Finding: "a#1", Answer: "adopted"}},
			Missing:  []string{"x#1"},
		}, true
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_fa", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	if gotFrom != "in_progress" || gotTo != "done" {
		t.Errorf("resolver asked for edge %s→%s", gotFrom, gotTo)
	}
	var top struct {
		FindingAnswers FindingAnswers `json:"finding_answers"`
	}
	if err := json.Unmarshal([]byte(r.got.Payload), &top); err != nil {
		t.Fatal(err)
	}
	if len(top.FindingAnswers.Missing) != 1 || top.FindingAnswers.Missing[0] != "x#1" || len(top.FindingAnswers.Answered) != 1 {
		t.Errorf("gate payload finding_answers = %+v\n%s", top.FindingAnswers, r.got.Payload)
	}

	// A gate whose edge's performer was offered nothing is left unchanged.
	g2, r2 := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g2.SetFindingAnswersResolver(func(context.Context, string, string, string) (FindingAnswers, bool) { return FindingAnswers{}, false })
	if _, err := g2.Gate(context.Background(), workitem.Item{ID: "sty_fa", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r2.got.Payload, "finding_answers") {
		t.Errorf("no offered findings must add nothing: %s", r2.got.Payload)
	}
}

// sty_4470bb38: a performer run at the step the story is already in is offered
// the findings of the step's exit that was rejected most recently, and its
// answers are read back by that exit's gate.

// edgeResolver returns rows only for the edge from→to, as the ledger resolver does.
func edgeResolver(from, to string, rows ...PriorVerdict) func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
	return func(_ context.Context, _, f, t string, _ verb.StaleExemption) []PriorVerdict {
		if f != from || t != to {
			return nil
		}
		return append([]PriorVerdict(nil), rows...)
	}
}

func exitRejectOf(from, to string) func(context.Context, string, string) string {
	return func(_ context.Context, _, f string) string {
		if f == from {
			return to
		}
		return ""
	}
}

func TestSameStatusDispatchOffersTheRejectedExitsFindingsAndMessages(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|coder|planning-rubric",
		"integration",
		"done")
	const rubric = "---\nname: planning-rubric\ntype: skill\ndescription: test rubric\n---\nDo the work."
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: rubric, skillFound: true})
	r := &fakeRunner{out: `{"answers":[{"finding":"code-review#2","answer":"adopted","evidence":"guard refuses a -dev downgrade"}]}`}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file,grep,list_dir"}, true
	})
	g.SetPriorVerdictsResolver(edgeResolver("in_progress", "integration",
		PriorVerdict{Skill: "code-review", Decision: "reject", Notes: "first look"},
		PriorVerdict{Skill: "code-review", Decision: "reject", Notes: "deploy-guard allows a downgrade over a -dev build"},
	))
	g.SetExitRejectResolver(exitRejectOf("in_progress", "integration"))
	g.SetMessagesResolver(stateResolver([]MessageState{
		{ID: "msg_00000009", From: "orchestrator", To: "coder", Body: "ORCH-DECISION: keep the guard in deploy.go", State: "waiting"},
	}))

	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_f", Status: "in_progress"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		Findings []Finding      `json:"findings_to_answer"`
		EdgeTo   string         `json:"findings_edge_to"`
		Messages []MessageState `json:"messages"`
	}
	if err := json.Unmarshal([]byte(r.got.Payload), &top); err != nil {
		t.Fatalf("payload: %v\n%s", err, r.got.Payload)
	}
	// Numbered over the exit edge's full history: the latest is attempt 2.
	if len(top.Findings) != 1 || top.Findings[0].ID != "code-review#2" || !top.Findings[0].Blocking ||
		top.Findings[0].Notes != "deploy-guard allows a downgrade over a -dev build" {
		t.Errorf("findings_to_answer = %+v, want the exit reviewer's latest verdict as code-review#2", top.Findings)
	}
	if top.EdgeTo != "integration" {
		t.Errorf("findings_edge_to = %q, want integration", top.EdgeTo)
	}
	if len(top.Messages) != 1 || top.Messages[0].Body != "ORCH-DECISION: keep the guard in deploy.go" {
		t.Errorf("messages = %+v, want the waiting orchestrator→coder message", top.Messages)
	}
	if len(res.FindingsOffered) != 1 || res.FindingsOffered[0] != "code-review#2" || res.FindingsEdgeTo != "integration" ||
		len(res.Answers) != 1 || res.Answers[0].Finding != "code-review#2" {
		t.Errorf("result = offered %v edge %q answers %+v", res.FindingsOffered, res.FindingsEdgeTo, res.Answers)
	}

	// With no rejected exit, a same-status run is offered nothing.
	g.SetExitRejectResolver(func(context.Context, string, string) string { return "" })
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_f", Status: "in_progress"}, "in_progress"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.got.Payload, "findings_to_answer") || strings.Contains(r.got.Payload, "findings_edge_to") {
		t.Errorf("no rejected exit must add no findings: %s", r.got.Payload)
	}
	// sty_47525ee9: the orchestrator's message alone still reaches the performer.
	var alone struct {
		Messages []MessageState `json:"messages"`
	}
	if err := json.Unmarshal([]byte(r.got.Payload), &alone); err != nil {
		t.Fatalf("payload: %v\n%s", err, r.got.Payload)
	}
	if len(alone.Messages) != 1 || alone.Messages[0].Body != "ORCH-DECISION: keep the guard in deploy.go" {
		t.Errorf("with no rejected exit, messages = %+v, want the waiting orchestrator→coder message", alone.Messages)
	}
}

func TestFindingAnswersFromReadsAStepRunByTheExitItAnswered(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 22, 0, 0, 0, time.UTC)
	entries := []ledger.Entry{
		dispatchRow(t, t0, "in_progress", "in_progress", map[string]any{
			"findings_offered": []string{"code-review#2"}, "findings_edge_to": "integration",
			"finding_answers": []agentartifact.Answer{{Finding: "code-review#2", Answer: "adopted"}}}),
		// Newer runs answering another exit, and a row with no source edge: the
		// integration gate reads neither.
		dispatchRow(t, t0.Add(time.Hour), "in_progress", "in_progress", map[string]any{
			"findings_offered": []string{"blocked-review#1"}, "findings_edge_to": "blocked"}),
		dispatchRow(t, t0.Add(2*time.Hour), "in_progress", "in_progress", map[string]any{
			"findings_offered": []string{"old#1"}}),
	}
	fa, ok := FindingAnswersFrom(entries, "in_progress", "integration")
	if !ok || len(fa.Answered) != 1 || fa.Answered[0].Finding != "code-review#2" || len(fa.Missing) != 0 {
		t.Errorf("integration gate read %+v (%v), want code-review#2 answered", fa, ok)
	}
	fa, ok = FindingAnswersFrom(entries, "in_progress", "blocked")
	if !ok || len(fa.Missing) != 1 || fa.Missing[0] != "blocked-review#1" {
		t.Errorf("blocked gate read %+v (%v), want its own run's offer", fa, ok)
	}
	if _, ok := FindingAnswersFrom(entries, "in_progress", "done"); ok {
		t.Error("an exit no run answered must report nothing")
	}
}
