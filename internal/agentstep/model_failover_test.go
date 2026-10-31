package agentstep

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
)

// modelRun is one run a modelRunner saw: the model it was asked for and the
// request's payload and tool grant, which must not change between models.
type modelRun struct {
	model, payload, tools string
}

// modelRunner is a fake agent keyed on Request.Model (sty_7bfa980d): each
// model's behaviour is a func of the run's context and request. A model with no
// behaviour answers an accept verdict.
type modelRunner struct {
	mu     sync.Mutex
	runs   []modelRun
	behave map[string]func(ctx context.Context, req agentcli.Request) ([]byte, error)
}

func (r *modelRunner) Name() string    { return "model-runner" }
func (r *modelRunner) Command() string { return "model-runner -p {system} --model {model}" }
func (r *modelRunner) Run(ctx context.Context, req agentcli.Request) ([]byte, error) {
	r.mu.Lock()
	r.runs = append(r.runs, modelRun{model: req.Model, payload: req.Payload, tools: req.AllowedTools})
	f := r.behave[req.Model]
	r.mu.Unlock()
	if f == nil {
		return []byte(`{"decision":"accept","notes":"answered"}`), nil
	}
	return f(ctx, req)
}

func (r *modelRunner) models() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.runs))
	for i, run := range r.runs {
		out[i] = run.model
	}
	return out
}

// Fake-agent failures, one per class.
func answer(out string) func(context.Context, agentcli.Request) ([]byte, error) {
	return func(context.Context, agentcli.Request) ([]byte, error) { return []byte(out), nil }
}

func fail(err error) func(context.Context, agentcli.Request) ([]byte, error) {
	return func(context.Context, agentcli.Request) ([]byte, error) { return nil, err }
}

// hang runs until its context ends, as a model that never responds does.
func hang(ctx context.Context, _ agentcli.Request) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// killedOnDeadline is a command transport cut off by the hard bound: it reports
// only its exit, never the deadline.
func killedOnDeadline(ctx context.Context, _ agentcli.Request) ([]byte, error) {
	<-ctx.Done()
	return nil, errors.New("agentcli: model-runner: signal: killed")
}

type invocationRows struct {
	mu   sync.Mutex
	rows []map[string]any
}

func (s *invocationRows) record(_ context.Context, _ string, p map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, p)
	return nil
}

func (s *invocationRows) phase(name string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, r := range s.rows {
		if r["phase"] == name {
			out = append(out, r)
		}
	}
	return out
}

// failoverEngine is an engine over r with one attempt per model and no backoff,
// so each model runs exactly once per class.
func failoverEngine(t *testing.T, r agentcli.Runner) (*Engine, *invocationRows) {
	t.Helper()
	g := New(r, fakeDocs{workflow: testWorkflow}, t.TempDir(), "")
	g.SetInjectPrinciples(false)
	g.attempts = 1
	g.backoff = nil
	rows := &invocationRows{}
	g.SetInvocationRecorder(rows.record)
	return g, rows
}

func listBinding(models string) config.AgentBinding {
	return config.AgentBinding{Command: "model-runner", Tools: "Read,Grep", Model: models, Principles: config.PrinciplesNone}
}

func verdictRequest(b config.AgentBinding, r agentcli.Runner) InvokeRequest {
	return InvokeRequest{
		Binding: b, Section: "reviewer", Rubric: "judge", Skill: "mm-review",
		Payload: map[string]string{"story": "sty_x"}, Expect: ExpectVerdict, Runner: r,
	}
}

// AC2 + AC4 (engine half): a verdict dispatch whose first model gives no answer,
// an unparseable verdict, a timeout, an idle stall or a rate limit is re-run on
// the second model, once each, with the identical payload and grant; the
// verdict carries the model that gave it, and the move is recorded on a
// model_failover agent_invocation row naming the model it ran and why it moved.
func TestInvokeModelListMovesOnWhenTheModelDoesNotAnswer(t *testing.T) {
	cases := []struct {
		name    string
		m1      func(context.Context, agentcli.Request) ([]byte, error)
		timeout time.Duration
		idle    time.Duration
		reason  string
	}{
		{name: "no answer", m1: answer(""), reason: "no-verdict"},
		{name: "unparseable verdict", m1: answer("### garbled ###"), reason: "no-verdict"},
		{name: "timeout", m1: hang, timeout: 50 * time.Millisecond, reason: "timeout"},
		{name: "idle stall", m1: hang, idle: 50 * time.Millisecond, reason: "stalled"},
		{name: "rate limit", m1: fail(errors.New("HTTP 429 rate limit exceeded")), reason: "no-verdict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){"m1": c.m1}}
			g, rows := failoverEngine(t, r)
			g.idleTimeout = c.idle
			req := verdictRequest(listBinding("m1,m2"), r)
			req.Timeout = c.timeout
			res := g.Invoke(context.Background(), req)
			if res.Err != nil {
				t.Fatalf("the second model answers, got %v", res.Err)
			}
			if got := strings.Join(r.models(), ","); got != "m1,m2" {
				t.Fatalf("runs = %s, want m1 once then m2 once", got)
			}
			if r.runs[0].payload != r.runs[1].payload || r.runs[0].tools != r.runs[1].tools {
				t.Fatalf("only the model may change between attempts:\n%+v\n%+v", r.runs[0], r.runs[1])
			}
			if res.Decision == nil || !res.Decision.Accept || res.Decision.Model != "m2" {
				t.Fatalf("the verdict must be m2's: %+v", res.Decision)
			}
			moves := rows.phase("model_failover")
			if len(moves) != 1 {
				t.Fatalf("model_failover rows = %d, want 1: %v", len(moves), rows.rows)
			}
			mv := moves[0]
			if mv["model"] != "m1" || mv["next_model"] != "m2" || mv["outcome"] != c.reason || mv["error"] == "" {
				t.Fatalf("model_failover row = %v, want model m1 → m2 for %s", mv, c.reason)
			}
		})
	}
}

// AC2: a perform dispatch moves on the same way — an agent that settles with no
// answer, a hard timeout the transport reports only as a kill, an idle stall
// and a rate limit.
func TestInvokeModelListMovesOnForAPerformer(t *testing.T) {
	cases := []struct {
		name    string
		m1      func(context.Context, agentcli.Request) ([]byte, error)
		timeout time.Duration
		idle    time.Duration
		reason  string
	}{
		{name: "settled without an answer", m1: fail(agentcli.Mark(errors.New("agentcli: pi rpc: pi settled without an answer"), agentcli.ErrNoAnswer)), reason: "no-answer"},
		{name: "timeout reported as a kill", m1: killedOnDeadline, timeout: 50 * time.Millisecond, reason: "timeout"},
		{name: "idle stall", m1: hang, idle: 50 * time.Millisecond, reason: "stalled"},
		{name: "rate limit", m1: fail(errors.New("HTTP 429 rate limit exceeded")), reason: "rate-limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
				"m1": c.m1, "m2": answer("performed"),
			}}
			g, rows := failoverEngine(t, r)
			g.idleTimeout = c.idle
			res := g.Invoke(context.Background(), InvokeRequest{
				Binding: listBinding("m1,m2"), Section: "coder", Rubric: "do it",
				Payload: map[string]string{}, Expect: ExpectPerform, Runner: r, Timeout: c.timeout,
			})
			if res.Err != nil || string(res.Stdout) != "performed" {
				t.Fatalf("the second model performs: out=%q err=%v", res.Stdout, res.Err)
			}
			if got := res.RanModel("m1"); got != "m2" {
				t.Fatalf("the performance must be credited to m2, got %q", got)
			}
			if got := strings.Join(r.models(), ","); got != "m1,m2" {
				t.Fatalf("runs = %s, want m1,m2", got)
			}
			if moves := rows.phase("model_failover"); len(moves) != 1 || moves[0]["outcome"] != c.reason {
				t.Fatalf("model_failover rows = %v, want one for %s", moves, c.reason)
			}
		})
	}
}

// AC2: every model is tried exactly once, in order, and the last one's failure
// is what the dispatch returns.
func TestInvokeModelListTriesEachModelOnce(t *testing.T) {
	r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
		"m1": answer(""), "m2": answer(""), "m3": answer(""),
	}}
	g, rows := failoverEngine(t, r)
	res := g.Invoke(context.Background(), verdictRequest(listBinding("m1, m2 ,m3"), r))
	if res.Err == nil || !strings.Contains(res.Err.Error(), "produced no verdict") {
		t.Fatalf("every model failed: want the last no-verdict error, got %v", res.Err)
	}
	if got := strings.Join(r.models(), ","); got != "m1,m2,m3" {
		t.Fatalf("runs = %s, want each model once in order", got)
	}
	if n := len(rows.phase("model_failover")); n != 2 {
		t.Fatalf("model_failover rows = %d, want 2 (m1→m2, m2→m3)", n)
	}
}

// AC2: a bundled session that names no verdict fails every rubric closed; with
// another model listed it is re-run there, and only the last model's result
// stands.
func TestInvokeModelListMovesOnAFailedClosedBundle(t *testing.T) {
	r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
		"m1": answer("nothing useful"),
		"m2": answer(`{"verdicts":[{"skill":"a","decision":"accept","notes":"ok"}]}`),
	}}
	g, rows := failoverEngine(t, r)
	req := verdictRequest(listBinding("m1,m2"), r)
	req.Expect, req.Skills = ExpectVerdicts, []string{"a"}
	res := g.Invoke(context.Background(), req)
	if res.Err != nil || res.NoVerdict {
		t.Fatalf("m2 answers the bundle: err=%v noVerdict=%v", res.Err, res.NoVerdict)
	}
	if got := strings.Join(r.models(), ","); got != "m1,m2" {
		t.Fatalf("runs = %s, want m1,m2", got)
	}
	if len(res.Decisions) != 1 || !res.Decisions[0].Accept {
		t.Fatalf("m2's verdict must stand: %+v", res.Decisions)
	}
	if moves := rows.phase("model_failover"); len(moves) != 1 || moves[0]["outcome"] != "no-verdict" {
		t.Fatalf("model_failover rows = %v", moves)
	}
}

// AC3: a refused grant, a broken command, a cancel and a turn cap — reached
// directly or through attempt exhaustion — never move to the next model, and
// the error comes back as it was.
func TestInvokeModelListStaysOnOtherFailures(t *testing.T) {
	t.Run("refused grant", func(t *testing.T) {
		r := &modelRunner{}
		g, rows := failoverEngine(t, r)
		b := listBinding("m1,m2")
		b.Tools = "Edit"
		req := verdictRequest(b, r)
		req.Payload, req.Material = transitionPayload{From: "a", To: "b"}, &reviewerMaterial{}
		res := g.Invoke(context.Background(), req)
		if res.Err == nil || !strings.Contains(res.Err.Error(), "admits no read tool") {
			t.Fatalf("want the grant refusal, got %v", res.Err)
		}
		assertNoMove(t, r, rows, 0)
	})
	t.Run("broken command", func(t *testing.T) {
		r := &modelRunner{}
		g, rows := failoverEngine(t, r)
		g.newRunner = func(string, string) (agentcli.Runner, error) { return nil, errors.New("unbalanced quote") }
		req := verdictRequest(listBinding("m1,m2"), nil)
		res := g.Invoke(context.Background(), req)
		if res.Err == nil || !strings.Contains(res.Err.Error(), "broken command") {
			t.Fatalf("want the broken-command error, got %v", res.Err)
		}
		assertNoMove(t, r, rows, 0)
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
			"m1": func(context.Context, agentcli.Request) ([]byte, error) { cancel(); return nil, context.Canceled },
		}}
		g, rows := failoverEngine(t, r)
		res := g.Invoke(ctx, verdictRequest(listBinding("m1,m2"), r))
		if res.Err == nil {
			t.Fatal("a cancelled dispatch fails")
		}
		assertNoMove(t, r, rows, 1)
	})
	t.Run("turn cap through attempt exhaustion", func(t *testing.T) {
		r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
			"m1": func(context.Context, agentcli.Request) ([]byte, error) {
				return []byte("Max turns reached"), errors.New("exit status 1")
			},
		}}
		g, rows := failoverEngine(t, r)
		res := g.Invoke(context.Background(), verdictRequest(listBinding("m1,m2"), r))
		if !errors.Is(res.Err, agentcli.ErrMaxTurns) || !errors.Is(res.Err, errNoVerdict) {
			t.Fatalf("want a max-turns cause inside the no-verdict error, got %v", res.Err)
		}
		assertNoMove(t, r, rows, 1)
	})
	t.Run("turn cap on a performer", func(t *testing.T) {
		r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
			"m1": fail(agentcli.Mark(errors.New("aborted"), agentcli.ErrMaxTurns)),
		}}
		g, rows := failoverEngine(t, r)
		res := g.Invoke(context.Background(), InvokeRequest{
			Binding: listBinding("m1,m2"), Section: "coder", Payload: map[string]string{}, Expect: ExpectPerform, Runner: r,
		})
		if !errors.Is(res.Err, agentcli.ErrMaxTurns) {
			t.Fatalf("want the turn cap, got %v", res.Err)
		}
		assertNoMove(t, r, rows, 1)
	})
}

func assertNoMove(t *testing.T, r *modelRunner, rows *invocationRows, wantRuns int) {
	t.Helper()
	if got := r.models(); len(got) != wantRuns || (wantRuns > 0 && got[0] != "m1") {
		t.Fatalf("runs = %v, want %d run(s) on m1 only — m2 must never run", got, wantRuns)
	}
	if moves := rows.phase("model_failover"); len(moves) != 0 {
		t.Fatalf("no model_failover row may be written: %v", moves)
	}
}

// AC1 (dispatch): a single model runs once exactly as before, and an
// unresolved binding still carrying the raw list runs its first model first.
func TestInvokeSingleModelRunsOnce(t *testing.T) {
	r := &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){"m1": answer("")}}
	g, rows := failoverEngine(t, r)
	res := g.Invoke(context.Background(), verdictRequest(listBinding("m1"), r))
	if res.Err == nil {
		t.Fatal("a single model that does not answer fails as today")
	}
	if got := r.models(); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("runs = %v, want one run on m1", got)
	}
	if res.Model != "" {
		t.Fatalf("no move: the result names no moved-to model, got %q", res.Model)
	}
	if n := len(rows.phase("model_failover")); n != 0 {
		t.Fatalf("a single model writes no model_failover row, got %d", n)
	}
}

// AC5: once the list is spent the existing secondary failover applies
// unchanged — m1, then m2, then the secondary — and with no secondary the
// last model's error is returned.
func TestInvokeModelListThenSecondary(t *testing.T) {
	rateLimited := fail(errors.New("HTTP 429 rate limit exceeded"))
	newRunners := func() (*modelRunner, *stubRunner) {
		return &modelRunner{behave: map[string]func(context.Context, agentcli.Request) ([]byte, error){
			"m1": rateLimited, "m2": rateLimited,
		}}, &stubRunner{out: "secondary performed"}
	}
	perform := func(r agentcli.Runner) InvokeRequest {
		return InvokeRequest{
			Binding: listBinding("m1,m2"), Section: "coder", Payload: map[string]string{},
			Expect: ExpectPerform, Runner: r,
		}
	}

	t.Run("secondary", func(t *testing.T) {
		primary, secondary := newRunners()
		g, _ := failoverEngine(t, primary)
		var events []string
		g.SetTelemetry(func(_ context.Context, _, _, kind string, _ map[string]any) { events = append(events, kind) })
		g.SetSecondaryResolver(func(string, config.AgentBinding) (config.AgentBinding, string, bool) {
			return config.AgentBinding{Command: "secondary-cmd", Role: "agent", Principles: config.PrinciplesNone}, "sec", true
		})
		g.newRunner = func(_, command string) (agentcli.Runner, error) {
			if command == "secondary-cmd" {
				return secondary, nil
			}
			return primary, nil
		}
		res := g.Invoke(context.Background(), perform(primary))
		if res.Err != nil || string(res.Stdout) != "secondary performed" {
			t.Fatalf("the secondary answers once the list is spent: out=%q err=%v", res.Stdout, res.Err)
		}
		if got := strings.Join(primary.models(), ","); got != "m1,m2" || secondary.calls != 1 {
			t.Fatalf("order must be m1, m2, then the secondary once: primary=%s secondary=%d", got, secondary.calls)
		}
		want := []string{"agent-model-failover", "agent-secondary-failover"}
		if strings.Join(events, ",") != strings.Join(want, ",") {
			t.Fatalf("telemetry = %v, want %v", events, want)
		}
	})
	t.Run("no secondary", func(t *testing.T) {
		primary, _ := newRunners()
		g, _ := failoverEngine(t, primary)
		res := g.Invoke(context.Background(), perform(primary))
		if res.Err == nil || !strings.Contains(res.Err.Error(), "429") {
			t.Fatalf("with no secondary the last model's error is returned, got %v", res.Err)
		}
		if got := strings.Join(primary.models(), ","); got != "m1,m2" {
			t.Fatalf("runs = %s, want m1,m2", got)
		}
	})
}
