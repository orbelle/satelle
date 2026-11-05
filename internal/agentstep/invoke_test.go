package agentstep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
)

// TestCharterSourcesRenderBothRoles: the reviewer and executor charters come from
// ONE source (invoke.go) — each renders its own identity + constraint, and both
// carry the shared briefing (the .satelle read instruction), proving there is no
// per-role copied prose (AC2).
func TestCharterSourcesRenderBothRoles(t *testing.T) {
	rc := reviewerCharter()
	for _, want := range []string{"reviewer", "read-only", "return your verdict"} {
		if !strings.Contains(rc, want) {
			t.Errorf("reviewer charter missing %q: %q", want, rc)
		}
	}
	ec := executorCharter("planner", "plan", "satelle-project-workflow")
	for _, want := range []string{"planner", "plan", "satelle-project-workflow"} {
		if !strings.Contains(ec, want) {
			t.Errorf("executor charter missing %q: %q", want, ec)
		}
	}
	// The shared briefing rides in BOTH — one source, not two copies.
	if !strings.Contains(rc, ".satelle/") || !strings.Contains(ec, ".satelle/") {
		t.Errorf("shared briefing (.satelle read) missing from a role charter:\nreviewer=%q\nexecutor=%q", rc, ec)
	}
}

// TestBuildRequestCanonicalOrderAndOmitsEmpty: buildRequest composes the prompt in
// ONE canonical order — principles, then charter, then rubric — separated by the
// horizontal rule, and OMITS empty sections so a rubric-only invocation (the
// summariser) yields the rubric verbatim (AC1).
func TestBuildRequestCanonicalOrderAndOmitsEmpty(t *testing.T) {
	docs := fakeDocs{
		workflow: testWorkflow,
		extraPrinciples: []docindex.Doc{
			{Kind: "principles", Name: config.OperatingPrinciple, Body: alwaysPrincipleDoc},
		},
	}
	g := New(&fakeRunner{}, docs, "/repo", "")

	req, err := g.buildRequest(context.Background(), invocation{
		charter:    reviewerCharter(),
		rubric:     "RUBRIC-BODY",
		principles: config.PrinciplesSession,
		payload:    map[string]string{"k": "v"},
		tools:      "Read,Grep",
		model:      "sonnet",
	})
	if err != nil {
		t.Fatal(err)
	}
	sp := req.SystemPrompt
	iPrin := strings.Index(sp, "This resident belief MUST be visible")
	iChar := strings.Index(sp, "isolated satelle reviewer")
	iRub := strings.Index(sp, "RUBRIC-BODY")
	if iPrin < 0 || iChar < 0 || iRub < 0 || !(iPrin < iChar && iChar < iRub) {
		t.Errorf("canonical order principles<charter<rubric violated (%d,%d,%d):\n%s", iPrin, iChar, iRub, sp)
	}
	if !strings.Contains(sp, "\n\n---\n\n") {
		t.Errorf("charter/rubric separator missing:\n%s", sp)
	}
	if req.AllowedTools != "Read,Grep" || req.Model != "sonnet" || req.Dir != "/repo" {
		t.Errorf("grant/model/dir not carried: %+v", req)
	}
	if req.Payload != `{"k":"v"}` {
		t.Errorf("payload not marshalled onto stdin: %q", req.Payload)
	}

	// Summariser shape: no charter + no principle injection, but the pull-context
	// call-to-action rides in EVERY prompt (sty_47d31300) — so the prompt is the
	// call-to-action then the rubric, with no charter or principles section.
	req2, err := g.buildRequest(context.Background(), invocation{rubric: "JUST-RUBRIC"})
	if err != nil {
		t.Fatal(err)
	}
	sp2 := req2.SystemPrompt
	if !strings.Contains(sp2, "satelle story get") || !strings.Contains(sp2, "JUST-RUBRIC") {
		t.Errorf("summariser prompt must carry the pull call-to-action + rubric:\n%s", sp2)
	}
	if strings.Contains(sp2, "isolated satelle reviewer") || strings.Contains(sp2, "isolated satelle executor") || strings.Contains(sp2, "Always-resident principles") {
		t.Errorf("summariser prompt must have no charter/principles section:\n%s", sp2)
	}
}

// TestPullContextCallToActionInEveryRole: the pull-context call-to-action (the CLI
// commands a clean-start agent uses to reconstruct context) rides in the reviewer,
// executor, AND summariser prompts alike — one seam (buildRequest), applied
// uniformly (AC1, sty_47d31300).
func TestPullContextCallToActionInEveryRole(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{workflow: testWorkflow}, "/repo", "")
	roles := map[string]invocation{
		"reviewer":   {charter: reviewerCharter(), rubric: "r"},
		"executor":   {charter: executorCharter("planner", "plan", "wf"), rubric: "r"},
		"summariser": {rubric: "r"},
	}
	for name, inv := range roles {
		req, err := g.buildRequest(context.Background(), inv)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, cmd := range []string{"satelle story get", "satelle story docs", "satelle story doc ", "satelle ledger list --story"} {
			if !strings.Contains(req.SystemPrompt, cmd) {
				t.Errorf("%s prompt missing pull command %q:\n%s", name, cmd, req.SystemPrompt)
			}
		}
		// sty_58fa970e: payload docs is the primary read channel for attachments.
		if !strings.Contains(req.SystemPrompt, "`docs`") && !strings.Contains(req.SystemPrompt, "docs array") {
			t.Errorf("%s prompt missing payload docs channel:\n%s", name, req.SystemPrompt)
		}
		if !strings.Contains(req.SystemPrompt, "obsolete") && !strings.Contains(req.SystemPrompt, "Do **not** look under") {
			t.Errorf("%s prompt must forbid the in-repo .satelle/stories/ path:\n%s", name, req.SystemPrompt)
		}
	}
}

// TestBuildRequestMarshalsSettings pins AC2: a non-empty invocation.settings is
// JSON-marshalled onto req.Settings (deterministic key order, via encoding/json's
// sorted map keys), and an unset/empty settings yields "" — so buildArgs drops the
// {settings} placeholder and its flag exactly as it does for an empty model.
// TestJudgingBriefingReplacesThePullContext: a judging prompt tells the
// reviewer to open paths and names no tool. A shell grant keeps the three CLI
// lines. A performer prompt still carries the pull-context call to action.
func TestJudgingBriefingReplacesThePullContext(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{workflow: testWorkflow}, "/repo", "")
	judge, err := g.buildRequest(context.Background(), invocation{
		charter: reviewerCharter(), rubric: "r", judging: true, tools: "Read,Grep,Glob",
	})
	if err != nil {
		t.Fatal(err)
	}
	sp := judge.SystemPrompt
	for _, want := range []string{"JSON on the transport", "docs[].path", "diff.patch_path", "prior_verdicts", "definition_edits", "material", "could not be opened", ".satelle/stories/"} {
		if !strings.Contains(sp, want) {
			t.Errorf("judging prompt missing %q:\n%s", want, sp)
		}
	}
	for _, banned := range []string{"satelle story get", "Read, Grep, Glob", "read_file", "plan body"} {
		if strings.Contains(sp, banned) {
			t.Errorf("judging prompt without a shell must not contain %q", banned)
		}
	}
	// isolatedAgentBriefing still says the work item arrives on stdin. The
	// judging briefing must not: the plan body and the patch are files.
	brief := judgingContextBriefing(false)
	if strings.Contains(brief, "on stdin") || strings.Contains(brief, "plan body") {
		t.Fatalf("judging briefing must not put the plan or the patch on stdin:\n%s", brief)
	}
	withShell, err := g.buildRequest(context.Background(), invocation{
		charter: reviewerCharter(), rubric: "r", judging: true, shell: true, tools: "Read,Bash(satelle:*)",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"satelle story get", "satelle story docs", "satelle ledger list --story"} {
		if !strings.Contains(withShell.SystemPrompt, cmd) {
			t.Errorf("shell judging prompt missing %q", cmd)
		}
	}
	perform, err := g.buildRequest(context.Background(), invocation{
		charter: executorCharter("coder", "in_progress", "default"), rubric: "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(perform.SystemPrompt, "satelle story get") {
		t.Error("performer prompt must keep the pull-context call to action")
	}
	if strings.Contains(perform.SystemPrompt, "diff.patch_path") {
		t.Error("performer prompt must not use the judging briefing")
	}
}

func TestBuildRequestMarshalsSettings(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{workflow: testWorkflow}, "/repo", "")

	req, err := g.buildRequest(context.Background(), invocation{
		rubric:   "r",
		settings: map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic"}}`
	if req.Settings != want {
		t.Errorf("req.Settings = %q, want %q", req.Settings, want)
	}

	req2, err := g.buildRequest(context.Background(), invocation{rubric: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if req2.Settings != "" {
		t.Errorf("unset settings should marshal to empty string, got %q", req2.Settings)
	}
}

// TestRunOnceUsesSuppliedRunnerAndTimeout: runOnce runs the runner it is GIVEN
// (the executor's binding runner, not the engine's g.runner) and honours the
// per-invocation deadline — the two ways executor dispatch differs from a gate run.
func TestRunOnceUsesSuppliedRunnerAndTimeout(t *testing.T) {
	g := New(&fakeRunner{out: "ENGINE"}, fakeDocs{workflow: testWorkflow}, "/repo", "")

	supplied := &fakeRunner{out: "SUPPLIED"}
	out, _, err := g.runOnce(context.Background(), supplied, agentcli.Request{SystemPrompt: "x"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "SUPPLIED" {
		t.Errorf("runOnce must use the SUPPLIED runner, not g.runner, got %q", out)
	}
	if supplied.got.SystemPrompt != "x" {
		t.Errorf("request not passed through to the supplied runner: %+v", supplied.got)
	}

	if _, _, err := g.runOnce(context.Background(), &blockingRunner{}, agentcli.Request{}, time.Millisecond, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("runOnce must honour the per-invocation timeout, got %v", err)
	}
}

func TestExpectFromRole(t *testing.T) {
	if ExpectFromRole(config.RoleReviewer) != ExpectVerdict {
		t.Error("reviewer role must map to ExpectVerdict")
	}
	if ExpectFromRole(config.RoleAgent) != ExpectPerform {
		t.Error("agent role must map to ExpectPerform")
	}
	if ExpectFromRole("") != ExpectPerform {
		t.Error("empty role must map to ExpectPerform")
	}
}

func TestBuildRequestConstitutionOrderZero(t *testing.T) {
	docs := fakeDocs{
		workflow: testWorkflow,
		extraPrinciples: []docindex.Doc{
			{Kind: "principles", Name: config.OperatingPrinciple, Body: alwaysPrincipleDoc},
		},
	}
	g := New(&fakeRunner{}, docs, "/repo", "")
	g.SetConstitution("CONSTITUTION-BODY")
	req, err := g.buildRequest(context.Background(), invocation{
		charter:    reviewerCharter(),
		rubric:     "R",
		principles: config.PrinciplesSession,
		payload:    map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	sp := req.SystemPrompt
	iC := strings.Index(sp, "CONSTITUTION-BODY")
	iP := strings.Index(sp, "This resident belief MUST be visible")
	iChar := strings.Index(sp, "isolated satelle reviewer")
	if iC < 0 || iP < 0 || !(iC < iP && iP < iChar) {
		t.Errorf("constitution must precede principles which precede charter (%d,%d,%d):\n%s", iC, iP, iChar, sp)
	}
	if !strings.Contains(sp, "# Project constitution") {
		t.Error("missing Project constitution heading")
	}

	// principles=none: neither constitution nor principles
	req2, err := g.buildRequest(context.Background(), invocation{
		rubric: "R", principles: config.PrinciplesNone, payload: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req2.SystemPrompt, "CONSTITUTION-BODY") || strings.Contains(req2.SystemPrompt, "Always-resident principles") {
		t.Errorf("principles=none must inject nothing:\n%s", req2.SystemPrompt)
	}
}

func TestInvokeExpectVerdictParsesDecision(t *testing.T) {
	r := &fakeRunner{out: `{"decision":"accept","notes":"ok","reasoning":"because"}`}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	g.SetReviewerBinding(config.AgentBinding{
		Command: "claude", Tools: "Read", Model: "sonnet", Principles: config.PrinciplesNone,
	})
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: g.reviewerBinding,
		Section: "reviewer",
		Rubric:  "judge",
		Payload: map[string]string{"x": "1"},
		Expect:  ExpectVerdict,
		Runner:  r,
		Timeout: 0,
		Skill:   "test-skill",
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Decision == nil || !res.Decision.Accept || res.Decision.Notes != "ok" || res.Decision.Reasoning != "because" {
		t.Errorf("unexpected decision: %+v", res.Decision)
	}
}

// TestInvokeExpectVerdictSetsCaptureFull (sty_844b6ab1 AC6): the verdict path
// must opt into CaptureFull so an ACP decision emitted before trailing chatter
// is not dropped by the answer-only segment rule.
func TestInvokeExpectVerdictSetsCaptureFull(t *testing.T) {
	r := &fakeRunner{out: `{"decision":"accept","notes":"ok"}`}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	g.SetReviewerBinding(config.AgentBinding{
		Command: "claude", Tools: "Read", Model: "sonnet", Principles: config.PrinciplesNone,
	})
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: g.reviewerBinding,
		Section: "reviewer",
		Rubric:  "judge",
		Payload: map[string]string{},
		Expect:  ExpectVerdict,
		Runner:  r,
		Skill:   "test-skill",
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if r.got.Capture != agentcli.CaptureFull {
		t.Fatalf("ExpectVerdict Capture = %v, want CaptureFull", r.got.Capture)
	}
}

// TestInvokeExpectPerformKeepsCaptureAnswer (sty_844b6ab1): perform path leaves
// Capture at the zero value so ACP prose artifacts drop pre-tool narration.
func TestInvokeExpectPerformKeepsCaptureAnswer(t *testing.T) {
	r := &fakeRunner{out: "did the work"}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone},
		Section: "planner",
		Rubric:  "do it",
		Payload: map[string]string{},
		Charter: executorCharter("planner", "plan", "wf"),
		Expect:  ExpectPerform,
		Runner:  r,
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if r.got.Capture != agentcli.CaptureAnswer {
		t.Fatalf("ExpectPerform Capture = %v, want CaptureAnswer (zero)", r.got.Capture)
	}
}

func TestInvokeExpectPerformReturnsStdout(t *testing.T) {
	r := &fakeRunner{out: "did the work"}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "claude", Tools: "Read", Principles: config.PrinciplesNone},
		Section: "planner",
		Rubric:  "do it",
		Payload: map[string]string{},
		Charter: executorCharter("planner", "plan", "wf"),
		Expect:  ExpectPerform,
		Runner:  r,
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if string(res.Stdout) != "did the work" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.Decision != nil {
		t.Error("perform must not set Decision")
	}
}

func TestInvokeExpectPerformCarriesDispatchMarkers(t *testing.T) {
	t.Setenv(config.SessionEnv, "sess-A")
	r := &fakeRunner{out: "ok"}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{
			Command: "fake run",
			Env:     map[string]string{"KEEP": "binding", config.DispatchAgentEnv: "spoof"},
		},
		Section: "planner",
		Expect:  ExpectPerform,
		Runner:  r,
		StoryID: "sty_mark",
		Step:    "plan",
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	want := map[string]string{
		"KEEP":                  "binding",
		config.DispatchAgentEnv: "planner",
		config.DispatchStepEnv:  "plan",
		config.DispatchItemEnv:  "sty_mark",
		config.SessionEnv:       "sess-A",
	}
	for k, v := range want {
		if r.got.Env[k] != v {
			t.Errorf("Env[%q] = %q, want %q; full env=%v", k, r.got.Env[k], v, r.got.Env)
		}
	}
}

// TestInvokeVerdictCarriesDispatchMarkers pins AC4 (sty_719c4a7b): a verdict
// (reviewer) dispatch's child process inherits the driver's SATELLE_SESSION
// from the OS environment exactly as a performer's does — composeEnv bases
// every transport on os.Environ() regardless of Expect — so a verdict
// dispatch must carry the same SATELLE_DISPATCH_* markers a performer does,
// or its own hooks would publish role in-loop over the driver's file.
func TestInvokeVerdictCarriesDispatchMarkers(t *testing.T) {
	t.Setenv(config.SessionEnv, "sess-A")
	r := &fakeRunner{out: `{"decision":"accept","notes":"ok"}`}
	g := New(r, fakeDocs{workflow: testWorkflow}, "/repo", "")
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: "fake run", Env: map[string]string{"KEEP": "binding"}},
		Section: "reviewer",
		Expect:  ExpectVerdict,
		Runner:  r,
		StoryID: "sty_mark",
		Step:    "plan",
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	want := map[string]string{
		"KEEP":                  "binding",
		config.DispatchAgentEnv: "reviewer",
		config.DispatchStepEnv:  "plan",
		config.DispatchItemEnv:  "sty_mark",
		config.SessionEnv:       "sess-A",
	}
	for k, v := range want {
		if r.got.Env[k] != v {
			t.Errorf("Env[%q] = %q, want %q; full env=%v", k, r.got.Env[k], v, r.got.Env)
		}
	}
}

func TestParseDecisionStreamUnwrappedResult(t *testing.T) {
	// stream Captured() unwraps {"type":"result","result":"<json>"} to the inner
	// bytes (sty_d244fe1b). parseDecision must accept those exact bytes.
	dec, err := parseDecision([]byte(`{"decision":"accept","notes":"stream-ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Accept || dec.Notes != "stream-ok" {
		t.Errorf("unwrapped stream result: %+v", dec)
	}
}

func TestParseDecisionReasoningOptional(t *testing.T) {
	dec, err := parseDecision([]byte(`{"decision":"reject","notes":"n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Accept || dec.Notes != "n" || dec.Reasoning != "" {
		t.Errorf("notes-only: %+v", dec)
	}
	dec2, err := parseDecision([]byte(`{"decision":"accept","notes":"n","reasoning":"r"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !dec2.Accept || dec2.Reasoning != "r" {
		t.Errorf("with reasoning: %+v", dec2)
	}
}
