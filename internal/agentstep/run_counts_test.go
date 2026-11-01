package agentstep

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_46e950ab: every reviewer row records its turns and tool calls, or the
// adapter-named reason it cannot; a seat attempt that ends at the harness turn
// cap is its own outcome, with the usage it reported.

const eventCommand = "claude -p --output-format stream-json --allowedTools {tools}"

// countsRunner is a reviewer transport that emits tools tool-start events, then
// returns out and err. Its command names the transport the run is judged under.
type countsRunner struct {
	command string
	tools   int
	out     string
	err     error
}

func (r *countsRunner) Name() string    { return "counts-fake" }
func (r *countsRunner) Command() string { return r.command }
func (r *countsRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	for i := 0; i < r.tools; i++ {
		if req.OnEvent != nil {
			req.OnEvent(agentcli.Event{Kind: agentcli.EventToolStart, Tool: "read_file"})
		}
	}
	return []byte(r.out), r.err
}

func usageFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agentcli", "testdata", "usage", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// envelopeWith returns a captured JSON envelope with its reply text swapped for
// text, keeping usage and turns as captured.
func envelopeWith(t *testing.T, name, key, text string) string {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(usageFixtureBytes(t, name), &env); err != nil {
		t.Fatal(err)
	}
	env[key] = text
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const acceptVerdict = `{"decision":"accept","notes":"ok"}`

// grokOutputFor is what the grok CLI prints under command's --output-format: the
// REAL json envelope (its text swapped for text) for json, the bare text for plain.
// The shipped presets' form therefore decides what the tests below can measure.
func grokOutputFor(t *testing.T, command, text string) string {
	t.Helper()
	switch {
	case strings.Contains(command, "--output-format json"):
		return envelopeWith(t, "grok_json.json", "text", text)
	case strings.Contains(command, "--output-format plain"):
		return text
	}
	t.Fatalf("command %q names no grok output form this test models", command)
	return ""
}

func singleSeatGate(t *testing.T, r agentcli.Runner) (verb.GateDecision, error) {
	t.Helper()
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a", ""), extraSkills: bundleSkillDocs("rev-a")}
	g := New(r, docs, "/repo", "")
	g.attempts = 1
	g.warnOut = io.Discard
	return bundleGate(t, g)
}

func TestReviewerRowCarriesMeasuredTurnsAndToolCalls(t *testing.T) {
	r := &countsRunner{command: eventCommand, tools: 3, out: envelopeWith(t, "claude_json_turns.json", "result", acceptVerdict)}
	dec, err := singleSeatGate(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Turns == nil || *dec.Turns != 1 || dec.TurnsUnavailableReason != "" {
		t.Errorf("turns = %v (%q), want the envelope's num_turns 1", dec.Turns, dec.TurnsUnavailableReason)
	}
	if dec.ToolCalls == nil || *dec.ToolCalls != 3 || dec.ToolCallsUnavailableReason != "" {
		t.Errorf("tool calls = %v (%q), want the 3 tool events", dec.ToolCalls, dec.ToolCallsUnavailableReason)
	}
	if rv := dec.Reviewers[0]; rv.Turns == nil || rv.ToolCalls == nil {
		t.Errorf("the verdict row must carry the counts the decision does: %+v", rv.RunCounts)
	}
}

// A transport that emits tool events and saw none measured zero calls; one that
// emits none has no count at all — never a zero in its place.
func TestToolCallsZeroIsMeasuredOnlyWhereEventsAreReported(t *testing.T) {
	dec, err := singleSeatGate(t, &countsRunner{command: eventCommand, out: acceptVerdict})
	if err != nil {
		t.Fatal(err)
	}
	if dec.ToolCalls == nil || *dec.ToolCalls != 0 {
		t.Errorf("an event transport with no tool calls measured zero, got %v (%q)", dec.ToolCalls, dec.ToolCallsUnavailableReason)
	}

	oneShot := &countsRunner{command: agentcli.DefaultGrokCommand, tools: 5, out: grokOutputFor(t, agentcli.DefaultGrokCommand, acceptVerdict)}
	dec, err = singleSeatGate(t, oneShot)
	if err != nil {
		t.Fatal(err)
	}
	if dec.ToolCalls != nil {
		t.Errorf("a one-shot envelope transport has no tool-call count, got %d", *dec.ToolCalls)
	}
	if !strings.HasPrefix(dec.ToolCallsUnavailableReason, "grok command:") {
		t.Errorf("tool-call reason = %q, want it to name the adapter", dec.ToolCallsUnavailableReason)
	}
}

// sty_5ebcd6f9: a grok seat run by the default reviewer command records its turns
// and tokens on its row from the real json envelope, and its verdict still parses.
func TestGrokReviewerDefaultCommandRowCarriesTurnsAndTokens(t *testing.T) {
	cmd := agentcli.DefaultGrokCommand
	dec, err := singleSeatGate(t, &countsRunner{command: cmd, out: grokOutputFor(t, cmd, acceptVerdict)})
	if err != nil {
		t.Fatalf("the verdict must still parse from the command's output form: %v", err)
	}
	if len(dec.Reviewers) == 0 || !dec.Reviewers[0].Accept {
		t.Fatalf("reviewers = %+v, want an accept", dec.Reviewers)
	}
	if dec.Turns == nil || *dec.Turns != 1 || dec.TurnsUnavailableReason != "" {
		t.Errorf("turns = %v (%q), want the envelope's num_turns 1", dec.Turns, dec.TurnsUnavailableReason)
	}
	if !dec.UsageAvailable || dec.TokensOut != 24 || dec.TokensIn != 6435+512 {
		t.Errorf("usage = available %v in %d out %d, want the capture's tokens", dec.UsageAvailable, dec.TokensIn, dec.TokensOut)
	}
}

func TestBundleCarriesCountsOnTheFirstVerdictOnly(t *testing.T) {
	r := &countsRunner{command: eventCommand, tools: 2, out: envelopeWith(t, "claude_json_turns.json", "result", bundleVerdictsText)}
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,rev-c", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c")}
	g := New(r, docs, "/repo", "")
	g.warnOut = io.Discard
	dec, err := bundleGate(t, g)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Reviewers) != 3 {
		t.Fatalf("reviewers = %d", len(dec.Reviewers))
	}
	if first := dec.Reviewers[0]; first.Turns == nil || *first.Turns != 1 || first.ToolCalls == nil || *first.ToolCalls != 2 {
		t.Errorf("the bundle's one measured call carries its counts on the first verdict: %+v", first.RunCounts)
	}
	for _, rv := range dec.Reviewers[1:] {
		if rv.Turns != nil || rv.ToolCalls != nil {
			t.Errorf("a bundle's later verdicts carry no counts (one call, counted once): %+v", rv.RunCounts)
		}
	}
}

func gateFailureOf(t *testing.T, err error) *verb.GateFailure {
	t.Helper()
	var f *verb.GateFailure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want a *verb.GateFailure carrying the attempt's stamp", err)
	}
	if len(f.Stamps) != 1 {
		t.Fatalf("stamps = %d, want 1", len(f.Stamps))
	}
	return f
}

// AC2: the grok plain-output run that ends at the cap is a max-turns attempt —
// with its own invocation stamp — and not a reject. The command here is the plain
// form on purpose: the cap text is a plain-output capture, and plain reports no
// usage or turns.
func TestMaxTurnsSingleSeatFromGrokCapOutput(t *testing.T) {
	capText := string(usageFixtureBytes(t, "grok_max_turns.txt"))
	const plainGrok = "grok -p {payload} --system-prompt-override {system} --output-format plain --max-turns 32"
	for _, c := range []struct {
		name string
		err  error
	}{
		{"non-zero exit", errFakeExit},
		{"exit zero, no verdict", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &countsRunner{command: plainGrok, out: capText, err: c.err}
			dec, err := singleSeatGate(t, r)
			if err == nil {
				t.Fatalf("a run cut short at the cap judged nothing; got %+v", dec)
			}
			if !errors.Is(err, agentcli.ErrMaxTurns) {
				t.Errorf("err = %v, want it to wrap agentcli.ErrMaxTurns", err)
			}
			if got := classifyOutcome(err); got != verb.OutcomeMaxTurns {
				t.Errorf("classifyOutcome = %q, want %q", got, verb.OutcomeMaxTurns)
			}
			stamp := gateFailureOf(t, err).Stamps[0]
			if stamp.Outcome != verb.OutcomeMaxTurns || stamp.Command == "" || stamp.Skill != "rev-a" {
				t.Errorf("stamp = outcome %q command %q skill %q", stamp.Outcome, stamp.Command, stamp.Skill)
			}
			// Plain grok output reports no usage and no turns: recorded unavailable
			// with the adapter's reason, not zero.
			if stamp.UsageAvailable || stamp.UsageUnavailableReason == "" {
				t.Errorf("usage must be unavailable with a reason: %+v", stamp.UsageNote)
			}
			if stamp.Turns != nil || !strings.HasPrefix(stamp.TurnsUnavailableReason, "grok command:") {
				t.Errorf("turns = %v (%q)", stamp.Turns, stamp.TurnsUnavailableReason)
			}
		})
	}
}

// The usage and turns a run reported before the cap ended it are kept on the
// attempt's stamp: a non-zero exit no longer discards them.
func TestMaxTurnsKeepsTheUsageAndTurnsTheRunReported(t *testing.T) {
	r := &countsRunner{command: eventCommand, tools: 8, out: string(usageFixtureBytes(t, "claude_max_turns.json")), err: errFakeExit}
	_, err := singleSeatGate(t, r)
	if !errors.Is(err, agentcli.ErrMaxTurns) {
		t.Fatalf("err = %v, want ErrMaxTurns", err)
	}
	stamp := gateFailureOf(t, err).Stamps[0]
	if stamp.Outcome != verb.OutcomeMaxTurns {
		t.Errorf("outcome = %q", stamp.Outcome)
	}
	if !stamp.UsageAvailable || stamp.TokensOut != 1893 {
		t.Errorf("usage = available %v out %d, want the envelope's", stamp.UsageAvailable, stamp.TokensOut)
	}
	if stamp.Turns == nil || *stamp.Turns != 9 {
		t.Errorf("turns = %v, want 9", stamp.Turns)
	}
	if stamp.ToolCalls == nil || *stamp.ToolCalls != 8 {
		t.Errorf("tool calls = %v, want the 8 tool events", stamp.ToolCalls)
	}
}

func TestAnotherFailureIsUnavailableNotMaxTurns(t *testing.T) {
	_, err := singleSeatGate(t, &countsRunner{command: agentcli.DefaultGrokCommand, err: errFakeAgent})
	if errors.Is(err, agentcli.ErrMaxTurns) {
		t.Fatalf("an ordinary failure is not a turn cap: %v", err)
	}
	if got := gateFailureOf(t, err).Stamps[0].Outcome; got != verb.OutcomeUnavailable {
		t.Errorf("outcome = %q, want %q", got, verb.OutcomeUnavailable)
	}
}

// panelEngine runs a two-seat panel: seat-a judges, seat-b is cut off at the cap.
func panelEngine(t *testing.T) *Engine {
	t.Helper()
	capText := string(usageFixtureBytes(t, "grok_max_turns.txt"))
	foldSkill := "---\nname: fold\ntype: skill\ndescription: combine\ncheck: \"run-fold\"\n---\n# fold\n"
	skills := append(bundleSkillDocs("rev-a"), docindex.Doc{Kind: "skills", Name: "fold", Body: foldSkill})
	wf := spineWF("", "", "", "in_progress|executor||rev-a|||panel = [\"seat-a\", \"seat-b\"]\ncombine = \"fold\"", "done")
	g := New(&fakeRunner{}, fakeDocs{workflow: wf, extraSkills: skills}, "/repo", "")
	g.attempts = 1
	g.warnOut = io.Discard
	g.backoff = func(int) time.Duration { return 0 }
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		return config.AgentBinding{Role: config.RoleReviewer, Tools: "Read,Grep,Glob", Command: name + "-cli -p {payload}"}, true
	})
	g.newRunner = func(_, command string) (agentcli.Runner, error) {
		if strings.HasPrefix(command, "seat-b") {
			return &countsRunner{command: command, out: capText, err: errFakeExit}, nil
		}
		return &countsRunner{command: command, out: acceptVerdict}, nil
	}
	g.check = func(context.Context, string, string, string) (string, error) { return "", nil }
	return g
}

func TestMaxTurnsPanelSeatIsMarkedAndStamped(t *testing.T) {
	g := panelEngine(t)
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_p", Status: "backlog", Category: "feature"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	rv := dec.Reviewers[0]
	if len(rv.Seats) != 2 {
		t.Fatalf("seats = %+v", rv.Seats)
	}
	a, b := rv.Seats[0], rv.Seats[1]
	if a.Seat != "seat-a" || !a.Accept || a.Unavailable || a.MaxTurns {
		t.Errorf("seat-a = %+v, want a plain accept", a)
	}
	if b.Seat != "seat-b" || b.Accept || !b.Unavailable || !b.MaxTurns {
		t.Errorf("seat-b = %+v, want unavailable with max_turns set", b)
	}
	if len(rv.SeatStamps) != 2 {
		t.Fatalf("stamps = %d, want one per seat that started", len(rv.SeatStamps))
	}
	sa, sb := rv.SeatStamps[0], rv.SeatStamps[1]
	if sa.Outcome != "" || sa.Seat != "seat-a" || sa.ToolCalls == nil && sa.ToolCallsUnavailableReason == "" {
		t.Errorf("a seat that judged carries no outcome and has its counts or reasons: %+v", sa)
	}
	if sb.Outcome != verb.OutcomeMaxTurns || sb.Seat != "seat-b" || sb.Command == "" {
		t.Errorf("seat-b stamp = outcome %q seat %q command %q", sb.Outcome, sb.Seat, sb.Command)
	}
	if sb.Turns != nil || !strings.Contains(sb.TurnsUnavailableReason, "no turn count") {
		t.Errorf("seat-b turns = %v (%q)", sb.Turns, sb.TurnsUnavailableReason)
	}
}
