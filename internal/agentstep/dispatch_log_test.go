package agentstep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_2b89feab: a dispatch's log records what the agent was given — its system
// prompt, its payload, the sections a gate or rework relies on and its scratch
// material — before its events, and ends with its progress and final answer.

const logRubric = "---\nname: planning-rubric\ntype: skill\ndescription: test rubric\n---\nDo the work."

var logBinding = config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}

// dispatchLogsOf lists the logs agent's dispatches for storyID wrote under dir.
func dispatchLogsOf(t *testing.T, dir, agent, storyID string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "dispatch", "dispatch-"+agent+"-*-"+storyID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// readDispatchLog reads the one log agent's dispatch for storyID wrote.
func readDispatchLog(t *testing.T, dir, agent, storyID string) string {
	t.Helper()
	m := dispatchLogsOf(t, dir, agent, storyID)
	if len(m) != 1 {
		all, _ := filepath.Glob(filepath.Join(dir, "dispatch", "*"))
		t.Fatalf("want one dispatch-%s-*-%s.log, got %v (all: %v)", agent, storyID, m, all)
	}
	b, err := os.ReadFile(m[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// logBlock is the text of the fenced block labelled label.
func logBlock(t *testing.T, log, label string) string {
	t.Helper()
	open, end := "=== "+label+" ===\n", "\n=== end "+label+" ==="
	i := strings.Index(log, open)
	if i < 0 {
		t.Fatalf("log has no %q block:\n%s", label, log)
	}
	rest := log[i+len(open):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("block %q is not closed:\n%s", label, log)
	}
	return rest[:j]
}

// logEventRunner records its request, emits events during the run and prints out.
// With usage set it reports that usage, as a stream transport does.
type logEventRunner struct {
	command string
	events  []agentcli.Event
	out     string
	got     agentcli.Request
}

func (r *logEventRunner) Name() string { return "event-fake" }
func (r *logEventRunner) Command() string {
	if r.command == "" {
		return "event-fake -p {system}"
	}
	return r.command
}
func (r *logEventRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	r.got = req
	for _, ev := range r.events {
		if req.OnEvent != nil {
			req.OnEvent(ev)
		}
	}
	return []byte(r.out), nil
}

type logUsageRunner struct {
	*logEventRunner
	usage agentcli.UsageResult
}

func (r logUsageRunner) RunUsage(ctx context.Context, req agentcli.Request) ([]byte, agentcli.UsageResult, error) {
	out, err := r.Run(ctx, req)
	return out, r.usage, err
}

// AC2, AC5, AC6: the log holds the system prompt and payload as sent, with only
// the binding's secret values redacted and a persistent document's body
// replaced by its path; the input comes before the run's first event.
func TestDispatchLogRecordsTheInputAsSent(t *testing.T) {
	const secret = "s3cr3t-sentinel"
	docFile := filepath.Join(t.TempDir(), "plan.md")
	docBody := "---\nname: plan\ntype: plan\n---\nDOC-BODY-SENTINEL: touch a.go\n"
	if err := os.WriteFile(docFile, []byte(docBody), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wf := spineWF("", "", "", "plan|planner|planning-rubric", "done")
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: logRubric + " The value is " + secret + ".", skillFound: true})
	g.SetLogDir(dir, logfile.Config{})
	g.SetDocsResolver(func(context.Context, string) []DocState {
		return []DocState{{Name: "plan", Type: "plan", Body: docBody, Path: docFile}}
	})
	r := &logEventRunner{
		events: []agentcli.Event{{Kind: agentcli.EventMessage, Text: "working with " + secret}},
		out:    "finished with " + secret,
	}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	b := logBinding
	b.Env = map[string]string{"SECRET": secret}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return b, true })

	item := workitem.Item{ID: "sty_in1", Status: "backlog", Body: "the story names " + secret, AcceptanceCriteria: "1. one"}
	if _, err := g.DispatchExecutor(context.Background(), item, "plan"); err != nil {
		t.Fatal(err)
	}
	log := readDispatchLog(t, dir, "planner", "sty_in1")

	red := func(s string) string { return strings.ReplaceAll(s, secret, "[redacted:SECRET]") }
	// A local performer is sent the document as a path to a scratch copy
	// (sty_73a2dc68), so the payload carries no body to substitute.
	if strings.Contains(r.got.Payload, "DOC-BODY-SENTINEL") {
		t.Fatalf("fixture: a local performer's payload must name the document by path:\n%s", r.got.Payload)
	}
	if got, want := logBlock(t, log, "input: payload"), red(r.got.Payload); got != want {
		t.Errorf("logged payload differs from the sent one after redaction:\n got %s\nwant %s", got, want)
	}
	if got, want := logBlock(t, log, "input: system prompt"), red(r.got.SystemPrompt); got != want {
		t.Errorf("logged system prompt differs from the sent one after redaction:\n got %s\nwant %s", got, want)
	}

	if strings.Contains(log, secret) {
		t.Errorf("the secret value reached the log:\n%s", log)
	}
	if !strings.Contains(log, "[redacted:SECRET]") {
		t.Errorf("the secret must be named redacted:\n%s", log)
	}
	if strings.Contains(log, "DOC-BODY-SENTINEL") {
		t.Errorf("a persistent document's body was copied into the log:\n%s", log)
	}
	if !strings.Contains(logBlock(t, log, "input: sections"), "(scratch copy of "+docFile+", removed after dispatch)") {
		t.Errorf("the scratch copy must name the persistent document it was made from:\n%s", log)
	}
	scratch := r.got.Env[config.ScratchEnv]
	for _, want := range []string{scratch, `"sty_in1"`, `performing step "plan"`} {
		if want == "" || !strings.Contains(log, want) {
			t.Errorf("over-redacted: the log must keep %q:\n%s", want, log)
		}
	}
	if i, j := strings.Index(log, "=== input: system prompt ==="), strings.Index(log, "\tmessage\t"); i < 0 || j < 0 || i > j {
		t.Errorf("the input must come before the run's first event (input %d, message %d):\n%s", i, j, log)
	}
	if !strings.Contains(logBlock(t, log, "summary"), "final_answer:\nfinished with [redacted:SECRET]") {
		t.Errorf("the final answer must be the scrubbed output:\n%s", log)
	}
}

// AC6: a failover's secondary binding's secrets are redacted too, from the
// primary's own input onwards.
func TestDispatchLogRedactsTheSecondaryBindingsSecrets(t *testing.T) {
	const primarySecret, secondarySecret = "primary-sentinel-1", "secondary-sentinel-2"
	primary := &stubRunner{fail: true}
	secondary := &stubRunner{out: "secondary saw " + secondarySecret}
	dir := t.TempDir()
	g := New(primary, nil, t.TempDir(), "")
	g.SetInjectPrinciples(false)
	g.SetLogDir(dir, logfile.Config{})
	g.SetSecondaryResolver(func(string, config.AgentBinding) (config.AgentBinding, string, bool) {
		return config.AgentBinding{Command: "secondary-cmd", Role: "agent", Principles: config.PrinciplesNone,
			Env: map[string]string{"SEC2": secondarySecret}}, "secondary", true
	})
	g.newRunner = func(_, command string) (agentcli.Runner, error) {
		if command == "secondary-cmd" {
			return secondary, nil
		}
		return primary, nil
	}
	b := config.AgentBinding{Command: "primary-cmd", Role: "agent", Principles: config.PrinciplesNone,
		Env: map[string]string{"SECRET": primarySecret}}
	dlog, _, closeLog := g.openDispatchLog("coder", "sty_fo", b)
	res := g.Invoke(context.Background(), InvokeRequest{
		Section: "coder", Binding: b, Rubric: "names " + primarySecret + " and " + secondarySecret,
		Expect: ExpectPerform, Runner: primary, OnEvent: dlog.handler(), Log: dlog, StoryID: "sty_fo",
	})
	closeLog()
	if res.Err != nil {
		t.Fatalf("secondary should succeed: %v", res.Err)
	}
	log := readDispatchLog(t, dir, "coder", "sty_fo")
	for _, s := range []string{primarySecret, secondarySecret} {
		if strings.Contains(log, s) {
			t.Errorf("secret %q reached the log:\n%s", s, log)
		}
	}
	for _, want := range []string{"[redacted:SECRET]", "[redacted:SEC2]"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if n := strings.Count(log, "=== input: system prompt ==="); n != 2 {
		t.Errorf("each binding's run records its own input: got %d", n)
	}
}

// AC3: a re-dispatched performer's log names the findings it must answer and
// the messages delivered to it.
func TestDispatchLogNamesARedispatchsFindingsAndMessages(t *testing.T) {
	dir := t.TempDir()
	wf := spineWF("", "", "", "in_progress|coder|planning-rubric", "integration", "done")
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: logRubric, skillFound: true})
	g.SetLogDir(dir, logfile.Config{})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return &fakeRunner{out: "ok"}, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return logBinding, true })
	g.SetPriorVerdictsResolver(edgeResolver("in_progress", "integration",
		PriorVerdict{Skill: "code-review", Decision: "reject", Notes: "deploy-guard allows a downgrade"}))
	g.SetExitRejectResolver(exitRejectOf("in_progress", "integration"))
	g.SetMessagesResolver(stateResolver([]MessageState{
		{ID: "msg_00000009", From: "orchestrator", To: "coder", Body: "ORCH-DECISION: keep the guard\nsecond line", State: "waiting"},
	}))
	item := workitem.Item{ID: "sty_rd", Status: "in_progress", AcceptanceCriteria: "1. guard holds\n2. test proves it"}
	if _, err := g.DispatchExecutor(context.Background(), item, "in_progress"); err != nil {
		t.Fatal(err)
	}
	sections := logBlock(t, readDispatchLog(t, dir, "coder", "sty_rd"), "input: sections")
	for _, want := range []string{
		"findings_to_answer:\n  - code-review#1 decision=reject blocking",
		"messages:\n  - msg_00000009 orchestrator → coder: ORCH-DECISION: keep the guard\n",
		"acceptance_criteria:\n  1. guard holds\n  2. test proves it",
		"material: none",
	} {
		if !strings.Contains(sections, want) {
			t.Errorf("sections missing %q:\n%s", want, sections)
		}
	}
}

// AC3: a first dispatch's log says plainly that it carried no findings,
// messages, criteria or material.
func TestDispatchLogSaysNoneForAFirstDispatch(t *testing.T) {
	dir := t.TempDir()
	wf := spineWF("", "", "", "plan|planner|planning-rubric", "done")
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: logRubric, skillFound: true})
	g.SetLogDir(dir, logfile.Config{})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return &fakeRunner{out: "ok"}, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return logBinding, true })
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_fd", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	sections := logBlock(t, readDispatchLog(t, dir, "planner", "sty_fd"), "input: sections")
	want := "findings_to_answer: none\nmessages: none\nacceptance_criteria: none\nmaterial: none"
	if sections != want {
		t.Errorf("sections = %q, want %q", sections, want)
	}
}

// AC3, AC5: a reviewer handed its work item as scratch paths still has its
// messages, criteria and material named in its log, and each scratch file's
// content kept there, because scratch is removed after the dispatch. A scratch
// copy of a persistent document names the original instead of its content.
func TestReviewerDispatchLogKeepsTheSwappedWorkItem(t *testing.T) {
	dir := t.TempDir()
	docFile := filepath.Join(t.TempDir(), "plan.md")
	const persistent = "PERSISTENT-DOC-BODY"
	if err := os.WriteFile(docFile, []byte(persistent), 0o644); err != nil {
		t.Fatal(err)
	}
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetLogDir(dir, logfile.Config{})
	g.SetDocsResolver(func(context.Context, string) []DocState {
		return []DocState{
			{Name: "plan", Type: "plan", Body: persistent, Path: docFile},
			{Name: "note", Type: "output", Body: "SCRATCH-ONLY-NOTE"},
		}
	})
	g.SetMessagesResolver(stateResolver([]MessageState{
		{ID: "msg_r1", From: "orchestrator", To: "*", Body: "REVIEW-MESSAGE-BODY\nmore"},
	}))
	g.SetDiffResolver(func(context.Context, string) *DiffState {
		return &DiffState{Files: []string{"a.go"}, Patch: "+DIFF-PATCH-LINE"}
	})
	item := workitem.Item{ID: "sty_rv", Status: "backlog", AcceptanceCriteria: "1. first\n2. second"}
	if _, err := g.Gate(context.Background(), item, "in_progress"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.got.Payload, "REVIEW-MESSAGE-BODY") {
		t.Fatalf("fixture: the reviewer must have been handed paths, not the messages:\n%s", r.got.Payload)
	}
	scratch := r.got.Env[config.ScratchEnv]
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch %s should be removed after an accepted review (err %v)", scratch, err)
	}
	log := readDispatchLog(t, dir, "reviewer", "sty_rv")
	sections := logBlock(t, log, "input: sections")
	material := filepath.Join(scratch, "material")
	for _, want := range []string{
		"findings_to_answer: none",
		"messages:\n  - msg_r1 orchestrator → *: REVIEW-MESSAGE-BODY\n",
		"acceptance_criteria:\n  1. first\n  2. second",
		"- " + filepath.Join(material, "messages.json") + " (scratch, removed after dispatch; content below)",
		"- " + filepath.Join(material, "docs", "plan") + " (scratch copy of " + docFile + ", removed after dispatch)",
	} {
		if !strings.Contains(sections, want) {
			t.Errorf("sections missing %q:\n%s", want, sections)
		}
	}
	for file, want := range map[string]string{
		filepath.Join(material, "messages.json"): "REVIEW-MESSAGE-BODY",
		filepath.Join(material, "docs", "note"):  "SCRATCH-ONLY-NOTE",
		filepath.Join(material, "diff.json"):     "+DIFF-PATCH-LINE",
	} {
		if got := logBlock(t, log, "input: material "+file); !strings.Contains(got, want) {
			t.Errorf("material %s block = %q, want it to carry %q", file, got, want)
		}
	}
	if strings.Contains(log, persistent) {
		t.Errorf("a persistent document's body was copied into the log:\n%s", log)
	}
	if !strings.Contains(logBlock(t, log, "summary"), `"decision": "accept"`) {
		t.Errorf("the summary must carry the verdict:\n%s", log)
	}
}

// AC4: a stream run's summary counts its tool calls, reports its usage and
// ends the log with its decoded artifact.
func TestDispatchLogSummaryCountsAStreamRun(t *testing.T) {
	dir := t.TempDir()
	wf := spineWF("", "", "", "plan|planner|planning-rubric", "done")
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: logRubric, skillFound: true})
	g.SetLogDir(dir, logfile.Config{})
	r := logUsageRunner{
		logEventRunner: &logEventRunner{
			command: "claude -p --output-format stream-json {system}",
			events: []agentcli.Event{
				{Kind: agentcli.EventToolStart, Tool: "Read"}, {Kind: agentcli.EventToolEnd, Tool: "Read"},
				{Kind: agentcli.EventToolStart, Tool: "Bash"}, {Kind: agentcli.EventToolEnd, Tool: "Bash"},
				{Kind: agentcli.EventUsage, Usage: &agentcli.UsageResult{TotalTokens: 15}},
			},
			out: `{"artifact":{"name":"design","type":"design-note","body":"ARTIFACT-BODY"}}`,
		},
		usage: agentcli.UsageResult{Available: true, InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	b := logBinding
	b.Interface = config.InterfaceStream
	b.Command = r.command
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return b, true })
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_sm", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	log := readDispatchLog(t, dir, "planner", "sty_sm")
	if !strings.HasSuffix(strings.TrimSpace(log), "=== end summary ===") {
		t.Fatalf("the log must end with the summary:\n%s", log)
	}
	summary := logBlock(t, log, "summary")
	for _, want := range []string{
		"tool_calls: 2\n",
		"usage: input=10 output=5 total=15\n",
		"outcome: ok\n",
		"final_answer:\nartifact design (design-note):\nARTIFACT-BODY",
		"turns: " + agentcli.TurnsUnavailableReason(config.InterfaceStream, r.command) + "\n",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
	if last := agentcli.LastEventLine(log); !strings.Contains(last, "\tusage\t") {
		t.Errorf("the last run event is %q, want the usage event; the summary must not hide it", last)
	}
}

// AC4: a json command run emits no tool events, so its summary gives the
// adapter's reason, never a zero.
func TestDispatchLogSummaryNamesWhyToolCallsAreUncounted(t *testing.T) {
	dir := t.TempDir()
	wf := spineWF("", "", "", "plan|planner|planning-rubric", "done")
	g, _ := newEngine(t, "", fakeDocs{workflow: wf, skillBody: logRubric, skillFound: true})
	g.SetLogDir(dir, logfile.Config{})
	r := &logEventRunner{command: "claude -p --output-format json {system}", out: "plain answer"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	b := logBinding
	b.Command = r.command
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return b, true })
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_js", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	summary := logBlock(t, readDispatchLog(t, dir, "planner", "sty_js"), "summary")
	want := "tool_calls: " + agentcli.ToolCallsUnavailableReason(config.InterfaceCommand, r.command) + "\n"
	if !strings.Contains(summary, want) || strings.Contains(summary, "tool_calls: 0") {
		t.Errorf("summary = %q, want %q and no zero", summary, want)
	}
	if !strings.Contains(summary, "final_answer:\nplain answer\nfull output: "+filepath.Join(dir, "executor.log")) {
		t.Errorf("an undecoded answer is its tail plus where the whole output is:\n%s", summary)
	}
}

// A payload that still carries a persistent document's body inline (a remote
// performer, which cannot read the scratch copies) is logged with the body
// replaced by the document's path, every other byte as sent.
func TestDispatchLogNamesAnInlineDocumentByPath(t *testing.T) {
	docs := []DocState{{Name: "plan", Type: "plan", Body: "DOC-BODY-SENTINEL", Path: "/store/plan.md"}}
	sent := `{"docs":[{"name":"plan","type":"plan","body":"DOC-BODY-SENTINEL"}]}`
	if got, want := substituteDocPaths(sent, docs), `{"docs":[{"name":"plan","type":"plan","body":"/store/plan.md"}]}`; got != want {
		t.Errorf("logged payload:\n got %s\nwant %s", got, want)
	}
	if got := inputMaterial(transitionPayload{Docs: docs}, "", nil); len(got) != 1 || got[0].path != "/store/plan.md" || got[0].inline {
		t.Errorf("an inline persistent document is named by its path, never copied: %+v", got)
	}
}
