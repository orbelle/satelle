package agentstep

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_93359e0f: a gate declares its review mode (full | fail-fast) in the route.
// The mode reaches the seat in its work item, a verdict states what it covered,
// and a partial reject is never carried as acceptance of the unreviewed rest.

// modeSeen is one dispatch as the seat saw it: its decoded work item and, when
// the item referenced one, the prior-verdicts file read while the seat ran.
type modeSeen struct {
	item   map[string]any
	prior  []map[string]any
	system string
}

func (m modeSeen) skills() []string {
	if s, ok := m.item["review_skill"].(string); ok && s != "" {
		return []string{s}
	}
	var out []string
	if list, ok := m.item["review_skills"].([]any); ok {
		for _, v := range list {
			out = append(out, v.(string))
		}
	}
	return out
}

func (m modeSeen) mode() (string, bool) {
	v, ok := m.item["review_mode"].(string)
	return v, ok
}

// modeRunner answers every seat with reply(skill) and records the work item each
// seat received. A bundled session gets a verdicts envelope. With envelope set
// the reply rides a claude-shaped JSON envelope carrying turns.
type modeRunner struct {
	mu       sync.Mutex
	seen     []modeSeen
	reply    func(skill string) string
	envelope func(text string) string
}

func (r *modeRunner) Name() string    { return "mode-fake" }
func (r *modeRunner) Command() string { return "mode-fake -p {system}" }

func (r *modeRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var seen modeSeen
	seen.system = req.SystemPrompt
	_ = json.Unmarshal([]byte(req.Payload), &seen.item)
	if ref, ok := seen.item["prior_verdicts"].(map[string]any); ok {
		if b, err := os.ReadFile(ref["path"].(string)); err == nil {
			_ = json.Unmarshal(b, &seen.prior)
		}
	}
	r.mu.Lock()
	r.seen = append(r.seen, seen)
	r.mu.Unlock()
	skills := seen.skills()
	var text string
	if _, bundled := seen.item["review_skills"]; bundled {
		var entries []json.RawMessage
		for _, sk := range skills {
			var e map[string]any
			_ = json.Unmarshal([]byte(r.answer(sk)), &e)
			e["skill"] = sk
			b, _ := json.Marshal(e)
			entries = append(entries, b)
		}
		b, _ := json.Marshal(map[string]any{"verdicts": entries})
		text = string(b)
	} else {
		text = r.answer(skills[0])
	}
	if r.envelope != nil {
		text = r.envelope(text)
	}
	return []byte(text), nil
}

func (r *modeRunner) answer(skill string) string {
	if r.reply != nil {
		if s := r.reply(skill); s != "" {
			return s
		}
	}
	return `{"decision":"accept","notes":"ok"}`
}

func (r *modeRunner) seenFor(skill string) []modeSeen {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []modeSeen
	for _, s := range r.seen {
		for _, sk := range s.skills() {
			if sk == skill {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

const foldCheckSkill = "---\nname: fold-check\nscope: system\ntype: skill\ntags: [type:skill, type:functional-check]\ndescription: fold a panel\n---\n\n# fold-check\n\n```check\nexit 0\n```\n"

func modeEngine(t *testing.T, edgeExtra, gates string, r *modeRunner, skills ...string) *Engine {
	t.Helper()
	wf := spineWF("", "", gates, "in_progress|executor||"+strings.Join(skills, ",")+"|reviewer|0|"+edgeExtra, "done")
	docs := fakeDocs{
		workflow:        wf,
		extraSkills:     append(bundleSkillDocs(append(append([]string{}, skills...), "rev-g")...), docindex.Doc{Kind: "skills", Name: "fold-check", Body: foldCheckSkill}),
		extraPrinciples: embeddedPrincipleDocs().extraPrinciples,
	}
	g := New(r, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }
	g.check = func(context.Context, string, string, string) (string, error) { return "ok\n", nil }
	g.SetDocsResolver(func(context.Context, string) []DocState {
		return []DocState{{Name: "plan", Type: "plan", Body: carryPlan}}
	})
	return g
}

func modeGate(t *testing.T, g *Engine) verb.GateDecision {
	t.Helper()
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_m", Status: "backlog", Category: "feature", Title: "T", Body: "B"}, "in_progress")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	return dec
}

const failFastGateNode = "\n[[gate]]\nskill = \"rev-g\"\non = [\"in_progress\"]\nreview_mode = \"fail-fast\"\n"

// AC2: the seat's work item names the gate's mode — absent for full, "fail-fast"
// for a step-listed reviewer and for a [[gate]] node.
func TestReviewMode_SeatPayloadNamesTheMode(t *testing.T) {
	r := &modeRunner{}
	g := modeEngine(t, `fail_fast = ["rev-b"]`, failFastGateNode, r, "rev-a", "rev-b")
	modeGate(t, g)
	for skill, want := range map[string]string{"rev-a": "", "rev-b": "fail-fast", "rev-g": "fail-fast"} {
		seen := r.seenFor(skill)
		if len(seen) != 1 {
			t.Fatalf("%s: dispatched %d times, want 1", skill, len(seen))
		}
		got, present := seen[0].mode()
		if got != want || present != (want != "") {
			t.Errorf("%s: review_mode = %q (present %v), want %q — absent for full", skill, got, present, want)
		}
	}
}

func TestReviewMode_FullPayloadCarriesNoModeKey(t *testing.T) {
	r := &modeRunner{}
	g := modeEngine(t, "", "", r, "rev-a")
	modeGate(t, g)
	seen := r.seenFor("rev-a")
	if len(seen) != 1 {
		t.Fatalf("dispatched %d times", len(seen))
	}
	if _, present := seen[0].item["review_mode"]; present {
		t.Errorf("a gate with no declared mode must leave the work item unchanged: %v", seen[0].item)
	}
}

// AC2: gates of different modes never share a bundled session; gates of the same
// fail-fast mode do, and the bundled session is told the mode.
func TestReviewMode_BundleSplitsOnMode(t *testing.T) {
	r := &modeRunner{}
	g := modeEngine(t, "bundle = true\nfail_fast = [\"rev-b\"]", "", r, "rev-a", "rev-b", "rev-c")
	modeGate(t, g)
	if len(r.seen) != 2 {
		t.Fatalf("a full pair and a fail-fast one are two sessions, got %d", len(r.seen))
	}
	for _, s := range r.seen {
		got, present := s.mode()
		switch sk := strings.Join(s.skills(), ","); sk {
		case "rev-a,rev-c":
			if present {
				t.Errorf("the full bundle must carry no review_mode, got %q", got)
			}
		case "rev-b":
			if got != "fail-fast" {
				t.Errorf("the fail-fast gate must be told its mode, got %q", got)
			}
		default:
			t.Errorf("unexpected session judging %q", sk)
		}
	}

	r = &modeRunner{}
	g = modeEngine(t, "bundle = true\nfail_fast = [\"rev-a\", \"rev-b\"]", "", r, "rev-a", "rev-b")
	dec := modeGate(t, g)
	if len(r.seen) != 1 {
		t.Fatalf("gates of one mode still share a session, got %d", len(r.seen))
	}
	if got, _ := r.seen[0].mode(); got != "fail-fast" {
		t.Errorf("the bundled session review_mode = %q, want fail-fast", got)
	}
	for _, rv := range dec.Reviewers {
		if rv.ReviewMode != "fail-fast" {
			t.Errorf("%s verdict ReviewMode = %q, want fail-fast", rv.Skill, rv.ReviewMode)
		}
	}
}

// AC3: a verdict may record its coverage; the binary carries it onto the verdict.
func TestReviewMode_VerdictRecordsCoverage(t *testing.T) {
	r := &modeRunner{reply: func(skill string) string {
		switch skill {
		case "rev-a":
			return `{"decision":"reject","notes":"AC2 has no step","reviewed":"AC2","coverage":{"complete":false,"stopped_at":"AC2 has no plan entry"}}`
		case "rev-b":
			return `{"decision":"accept","notes":"ok","coverage":{"complete":true}}`
		}
		return `{"decision":"accept","notes":"ok"}`
	}}
	g := modeEngine(t, `fail_fast = ["rev-a"]`, "", r, "rev-b", "rev-a", "rev-c")
	dec := modeGate(t, g)
	a := reviewerOf(t, dec, "rev-a")
	if a.ReviewMode != "fail-fast" || a.Coverage == nil || a.Coverage.Complete || a.Coverage.StoppedAt != "AC2 has no plan entry" {
		t.Errorf("rev-a = mode %q coverage %+v, want a fail-fast reject stopped at AC2", a.ReviewMode, a.Coverage)
	}
	if dec.Accept {
		t.Errorf("a reject blocks the edge: %+v", dec)
	}
	if dec.Coverage == nil || dec.Coverage.StoppedAt != "AC2 has no plan entry" {
		t.Errorf("the decision mirrors the deciding verdict's coverage, got %+v", dec.Coverage)
	}
	b := reviewerOf(t, dec, "rev-b")
	if b.Coverage == nil || !b.Coverage.Complete || b.ReviewMode != "" {
		t.Errorf("rev-b = mode %q coverage %+v, want a full accept stating complete coverage", b.ReviewMode, b.Coverage)
	}
}

func TestReviewMode_CoverageParsing(t *testing.T) {
	long := strings.Repeat("x", reviewedCeiling+50)
	cases := []struct {
		name string
		out  string
		want *verb.Coverage
	}{
		{"absent", `{"decision":"accept"}`, nil},
		{"complete", `{"decision":"accept","coverage":{"complete":true}}`, &verb.Coverage{Complete: true}},
		{"stopped", `{"decision":"reject","coverage":{"complete":false,"stopped_at":"AC2"}}`, &verb.Coverage{StoppedAt: "AC2"}},
		{"unreadable shape is stated nothing", `{"decision":"reject","coverage":"partial"}`, nil},
		{"stopped_at is bounded", `{"decision":"reject","coverage":{"complete":false,"stopped_at":"` + long + `"}}`, &verb.Coverage{StoppedAt: long[:reviewedCeiling]}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := parseDecision([]byte(tc.out))
			if err != nil {
				t.Fatalf("parseDecision: %v", err)
			}
			switch {
			case tc.want == nil && d.Coverage != nil:
				t.Errorf("Coverage = %+v, want nil: absent must never read as complete", d.Coverage)
			case tc.want != nil && (d.Coverage == nil || *d.Coverage != *tc.want):
				t.Errorf("Coverage = %+v, want %+v", d.Coverage, tc.want)
			}
			if d.Notes != "" || d.Accept != strings.Contains(tc.out, `"accept"`) {
				t.Errorf("the verdict itself must be unaffected: %+v", d)
			}
		})
	}
}

// AC3: a panel gate declares the mode once; every seat is told it and each seat's
// own coverage is stored on its record in the panel row.
func TestReviewMode_PanelSeatsInheritModeAndRecordCoverage(t *testing.T) {
	r := &modeRunner{reply: func(string) string {
		return `{"decision":"reject","notes":"stopped","coverage":{"complete":false,"stopped_at":"AC2"}}`
	}}
	gate := "\n[[gate]]\nskill = \"rev-g\"\non = [\"in_progress\"]\nreview_mode = \"fail-fast\"\npanel = [\"reviewer\"]\ncombine = \"fold-check\"\n"
	g := modeEngine(t, "", gate, r)
	dec := modeGate(t, g)
	seen := r.seenFor("rev-g")
	if len(seen) != 1 {
		t.Fatalf("the seat ran %d times, want 1", len(seen))
	}
	if got, _ := seen[0].mode(); got != "fail-fast" {
		t.Errorf("the panel seat review_mode = %q, want fail-fast", got)
	}
	rv := reviewerOf(t, dec, "rev-g")
	if rv.ReviewMode != "fail-fast" {
		t.Errorf("the skill-level panel verdict ReviewMode = %q, want fail-fast", rv.ReviewMode)
	}
	if len(rv.Seats) != 1 || rv.Seats[0].Coverage == nil || rv.Seats[0].Coverage.StoppedAt != "AC2" {
		t.Errorf("seat records = %+v, want the seat's coverage stopped at AC2", rv.Seats)
	}
}

// AC4: the seat re-presented an edge whose earlier verdict stopped early is told
// the prior was partial, and where it stopped.
func TestReviewMode_RePresentedSeatIsToldItsPriorWasPartial(t *testing.T) {
	r := &modeRunner{}
	g := modeEngine(t, `fail_fast = ["rev-a"]`, "", r, "rev-a")
	g.SetPriorVerdictsResolver(func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
		return []PriorVerdict{
			PriorVerdictFrom(verb.PriorVerdict{
				Skill: "rev-a", Decision: "reject", Notes: "AC2",
				ReviewMode: "fail-fast", Coverage: &verb.Coverage{StoppedAt: "AC2 has no plan entry"}, Partial: true,
			}),
		}
	})
	modeGate(t, g)
	seen := r.seenFor("rev-a")
	if len(seen) != 1 || len(seen[0].prior) != 1 {
		t.Fatalf("the seat must read one prior verdict, got %+v", seen)
	}
	p := seen[0].prior[0]
	cov, _ := p["coverage"].(map[string]any)
	if p["partial"] != true || p["review_mode"] != "fail-fast" || cov["stopped_at"] != "AC2 has no plan entry" {
		t.Errorf("prior verdict = %v, want partial with the stopped-at finding", p)
	}
}

// AC4: a partial verdict is never carried as acceptance, however unchanged its
// quotation; the same accept without partial is carried.
func TestReviewMode_PartialVerdictIsNeverCarried(t *testing.T) {
	for _, partial := range []bool{false, true} {
		prior := []PriorVerdict{rejected("rev-a"), {Skill: "rev-b", Decision: "accept", Reviewed: "PLAN-B", Partial: partial}, rejected("rev-c")}
		// Parallel, so every reviewer that is not carried is dispatched.
		g, r := carryEngine(t, "3", "", prior, map[string]string{"rev-a": "reject"})
		carryGate(t, g)
		dispatched := r.sessionsFor("rev-b")
		if partial && dispatched != 1 {
			t.Errorf("a partial accept must be dispatched fresh, got %d sessions", dispatched)
		}
		if !partial && dispatched != 0 {
			t.Errorf("a complete accept with an unchanged quotation is still carried, got %d sessions", dispatched)
		}
	}
}

func claudeEnvelope(turns *int) func(string) string {
	return func(text string) string {
		env := map[string]any{"result": text, "usage": map[string]any{"input_tokens": 100, "output_tokens": 5}}
		if turns != nil {
			env["num_turns"] = *turns
		}
		b, _ := json.Marshal(env)
		return string(b)
	}
}

// AC6: a reviewer seat's turns are recorded when the adapter reports them, and
// the unavailable reason (never a zero) when it reports usage but no count.
func TestReviewMode_SeatTurnsAreRecordedNotInvented(t *testing.T) {
	four := 4
	r := &modeRunner{envelope: claudeEnvelope(&four)}
	dec := modeGate(t, modeEngine(t, "", "", r, "rev-a"))
	a := reviewerOf(t, dec, "rev-a")
	if a.Turns == nil || *a.Turns != 4 || a.TurnsUnavailableReason != "" {
		t.Errorf("turns = %v reason %q, want 4 and no reason", a.Turns, a.TurnsUnavailableReason)
	}

	r = &modeRunner{envelope: claudeEnvelope(nil)}
	dec = modeGate(t, modeEngine(t, "", "", r, "rev-a"))
	a = reviewerOf(t, dec, "rev-a")
	if a.Turns != nil || !strings.Contains(a.TurnsUnavailableReason, "num_turns") {
		t.Errorf("turns = %v reason %q, want no count and the adapter-named reason", a.Turns, a.TurnsUnavailableReason)
	}

	r = &modeRunner{}
	dec = modeGate(t, modeEngine(t, "", "", r, "rev-a"))
	a = reviewerOf(t, dec, "rev-a")
	// A run that reported no usage still names why it has no count: never a bare
	// zero, never a silent gap (sty_46e950ab's RunCounts contract).
	if a.Turns != nil || a.TurnsUnavailableReason == "" {
		t.Errorf("a run that reported no usage records no turns and names the reason: %v %q", a.Turns, a.TurnsUnavailableReason)
	}
}
