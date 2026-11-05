package agentstep

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
)

// Reviewer tool isolation through the dispatch seam (sty_ef3efb51): an
// unsupported binding runs with a warning (sty_2d5e583a), and each adapter's
// invocation records the system prompt size and the offered-tool figure with its
// source (AC5).

const isolationVerdict = `{"decision":"accept","notes":"ok"}`

// startCounter writes an executable shim named name that records one line per
// start and prints stdout. The shim IS the injected spawner: its marker file
// counts how many processes a dispatch really started.
func startCounter(t *testing.T, name, stdout string) (path, marker string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, name)
	marker = filepath.Join(dir, "starts")
	script := "#!/bin/sh\necho start >> " + marker + "\ncat > /dev/null\nprintf '%s' '" + stdout + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, marker
}

func starts(marker string) int {
	b, _ := os.ReadFile(marker)
	return strings.Count(string(b), "start")
}

func reviewerInvoke(t *testing.T, iface, command, tools string) InvokeResult {
	t.Helper()
	runner, err := agentcli.RunnerFromBinding(iface, command)
	if err != nil {
		t.Fatal(err)
	}
	g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
	return g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Tools: tools, Principles: config.PrinciplesNone},
		Section: "reviewer", Rubric: "judge", Payload: map[string]string{"story": "sty_1"},
		Expect: ExpectVerdict, Runner: runner, Skill: "test-skill",
	})
}

// namedPeer copies the fake ACP or stream peer under name, so the adapter
// detection (which reads the binary's name) sees a grok, claude or mystery
// harness that actually speaks the transport (a shell stub would wait forever).
func namedPeer(t *testing.T, iface, name string) string {
	t.Helper()
	skipNoPython(t)
	peer := writeFakeACPPeer
	if iface == "stream" {
		peer = writeFakeStreamPeer
	}
	src, err := os.ReadFile(peer(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// warnedInvoke dispatches a reviewer with the warning writer captured.
func warnedInvoke(t *testing.T, iface, command, tools string, b config.AgentBinding) (InvokeResult, string) {
	t.Helper()
	runner, err := agentcli.RunnerFromBinding(iface, command)
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
	g.SetWarnWriter(&warn)
	b.Tools, b.Principles = tools, config.PrinciplesNone
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: b, Section: "reviewer", Rubric: "judge", Payload: map[string]string{"story": "sty_1"},
		Expect: ExpectVerdict, Runner: runner, Skill: "test-skill",
	})
	return res, warn.String()
}

// A reviewer binding no adapter can hold to its read-only grant is REFUSED:
// the dispatch fails with an error naming the binding and the gap, and no
// process starts. There is no attestation bypass.
func TestInvoke_UnsupportedReviewerIsRefused(t *testing.T) {
	const ro = "Read,Grep,Glob"
	cases := []struct {
		name, iface, binary, args, tools string
		wantGap                          string
	}{
		{"grok command always-approve, no allow-list", "command", "grok-shim", "-p {payload} --always-approve", ro, "tools not held to the grant"},
		{"grok command always-approve, wider allow-list", "command", "grok-shim", "-p {payload} --tools read_file,run_terminal_command --always-approve", ro, "tools not held to the grant"},
		{"claude bypass without allow-list", "command", "claude-shim", "-p --dangerously-skip-permissions", ro, "tools not held to the grant"},
		{"unrecognised harness", "command", "mystery-shim", "-p {payload}", ro, "no adapter knows"},
		{"unrestricted pi", "command", "pi", "-p {payload}", "read,grep,find,ls", "pi/command"},
		{"write-capable grant", "command", "claude-shim", "-p --tools {tools}", ro + ",Write", "write-capable"},
		{"scoped shell grant", "command", "claude-shim", "-p --tools {tools}", ro + ",Bash(satelle:*)", "write-capable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, marker := startCounter(t, tc.binary, isolationVerdict)
			res, _ := warnedInvoke(t, tc.iface, path+" "+tc.args, tc.tools, config.AgentBinding{})
			if res.Err == nil || res.Decision != nil {
				t.Fatalf("want a refusal, got %+v", res)
			}
			if !strings.Contains(res.Err.Error(), `"reviewer"`) || !strings.Contains(res.Err.Error(), tc.wantGap) {
				t.Errorf("refusal %q must name the binding and %q", res.Err, tc.wantGap)
			}
			if n := starts(marker); n != 0 {
				t.Fatalf("a refused reviewer started %d processes", n)
			}
		})
	}

	t.Run("grok acp is not a transport", func(t *testing.T) {
		if _, err := agentcli.RunnerFromBinding("acp", "grok agent stdio"); err == nil {
			t.Fatal("grok acp must be refused")
		}
	})

	t.Run("stock grok command preset runs with no warning and no limitation", func(t *testing.T) {
		path, marker := startCounter(t, "grok-shim", isolationVerdict)
		command := strings.Replace(agentcli.DefaultGrokCommand, "grok ", path+" ", 1)
		res, warn := warnedInvoke(t, "command", command, ro, config.AgentBinding{})
		if res.Err != nil {
			t.Fatalf("stock grok preset refused: %v", res.Err)
		}
		if n := starts(marker); n != 1 {
			t.Fatalf("start count = %d, want 1", n)
		}
		if warn != "" || res.IsolationLimitation != "" {
			t.Errorf("stock preset warned %q / limited %q", warn, res.IsolationLimitation)
		}
	})
}

// An observed mid-session breach — a write tool that ran without a permission
// request — fails the verdict, and is ledgered and warned.
func TestInvoke_ObservedBreachFailsTheVerdict(t *testing.T) {
	skipNoPython(t)
	src, err := os.ReadFile(writeFakeACPPeer(t))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "    elif method == \"session/prompt\":\n"
	breach := marker + `        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","kind":"edit","title":"write_file","status":"in_progress"}}})` + "\n"
	script := strings.Replace(string(src), marker, breach, 1)
	if script == string(src) {
		t.Fatal("the fake ACP peer no longer has a session/prompt branch to inject the breach into")
	}
	path := filepath.Join(t.TempDir(), "claude-shim")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromBinding("acp", path+" agent stdio")
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
	g.SetWarnWriter(&warn)
	recs := captureTelemetry(g)
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Tools: "Read,Grep,Glob", Principles: config.PrinciplesNone},
		Section: "reviewer", Rubric: "judge", Payload: map[string]string{"story": "sty_1"},
		Expect: ExpectVerdict, Runner: runner, Skill: "test-skill",
	})
	if res.Err == nil || res.Decision != nil {
		t.Fatalf("a breach must fail the verdict: %+v", res)
	}
	if !strings.Contains(res.Err.Error(), "left its read-only grant") {
		t.Errorf("the failure must say the grant was left: %v", res.Err)
	}
	if countKind(*recs, "reviewer-isolation-breach") != 1 {
		t.Errorf("want one reviewer-isolation-breach event: %+v", *recs)
	}
}

// A restricted pi binding is a recognised adapter that holds the grant.
func TestInvoke_PiReviewerIsRecognised(t *testing.T) {
	const ro = "read,grep,find,ls"
	path, marker := startCounter(t, "pi", isolationVerdict)
	res, warn := warnedInvoke(t, "command", path+" -p {payload} --tools read,grep,find,ls", ro, config.AgentBinding{})
	if res.Err != nil || starts(marker) != 1 {
		t.Fatalf("err = %v starts = %d", res.Err, starts(marker))
	}
	if warn != "" {
		t.Errorf("a restricted pi binding warned: %q", warn)
	}
	if res.OfferedToolCount == nil || *res.OfferedToolCount != 4 || res.OfferedToolsSource != agentcli.OfferedSourceFlag {
		t.Errorf("offered = %v (%q), want 4 via flag", res.OfferedToolCount, res.OfferedToolsSource)
	}
}

// A performer dispatch carries the deny of the satelle status verbs as a
// tool grant, and an adapter that cannot express a deny refuses it.
func TestInvoke_PerformerDeniedStatusVerbs(t *testing.T) {
	t.Run("claude: --disallowedTools carries the status verbs", func(t *testing.T) {
		dir := t.TempDir()
		argv := filepath.Join(dir, "argv")
		path := filepath.Join(dir, "claude-shim")
		script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\" >> " + argv + "; done\ncat > /dev/null\nprintf done\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		runner, err := agentcli.RunnerFromBinding("command", path+" -p --disallowedTools Write --append-system-prompt {system}")
		if err != nil {
			t.Fatal(err)
		}
		g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
		res := g.Invoke(context.Background(), InvokeRequest{
			Binding: config.AgentBinding{Tools: "Read,Edit", Principles: config.PrinciplesNone},
			Section: "coder", Rubric: "do", Payload: map[string]string{"story": "sty_1"},
			Expect: ExpectPerform, Runner: runner, Skill: "test-skill",
		})
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got, _ := os.ReadFile(argv)
		want := "Write,Bash(satelle story set:*),Bash(satelle task set:*),Bash(satelle execution set:*)"
		if !strings.Contains(string(got), want) {
			t.Errorf("argv %q must carry the deny %q", got, want)
		}
		if strings.Contains(string(got), "Do NOT change") {
			t.Errorf("the performer limit must not be prompt text: %q", got)
		}
	})
	t.Run("grok: refused before any process starts", func(t *testing.T) {
		path, marker := startCounter(t, "grok-shim", "done")
		runner, err := agentcli.RunnerFromBinding("command", path+" -p {payload} --system-prompt-override {system}")
		if err != nil {
			t.Fatal(err)
		}
		g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
		res := g.Invoke(context.Background(), InvokeRequest{
			Binding: config.AgentBinding{Tools: "read_file", Principles: config.PrinciplesNone},
			Section: "coder", Rubric: "do", Payload: map[string]string{"story": "sty_1"},
			Expect: ExpectPerform, Runner: runner, Skill: "test-skill",
		})
		if res.Err == nil || !strings.Contains(res.Err.Error(), "cannot express a tool deny") {
			t.Fatalf("want a refusal, got %+v", res)
		}
		if n := starts(marker); n != 0 {
			t.Fatalf("a refused performer started %d processes", n)
		}
	})
}

// AC5: every reviewer invocation records system_prompt_bytes and the offered
// tool figure with its source, per adapter. The count is what the harness
// offers (rendered --tools, or the harness's own init report) — never the grant
// length. A grok acp reviewer runs with a warning and records an unverified
// source (the warning test).
func TestInvoke_RecordsPromptBytesAndOfferedToolsPerAdapter(t *testing.T) {
	const ro = "Read,Grep,Glob"
	intp := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}

	t.Run("claude command: rendered --tools, source=flag", func(t *testing.T) {
		path, _ := startCounter(t, "claude-shim", `{"result":"{\"decision\":\"accept\",\"notes\":\"ok\"}"}`)
		res := reviewerInvoke(t, "command", strings.Replace(agentcli.DefaultClaudeCommand, "claude ", path+" ", 1), ro)
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		if res.SystemPromptBytes == 0 {
			t.Error("system_prompt_bytes not recorded")
		}
		if got := intp(res.OfferedToolCount); got != 3 || res.OfferedToolsSource != "flag" || res.IsolationLimitation != "" {
			t.Errorf("isolation = %v/%q/%q, want 3/flag/none", got, res.OfferedToolsSource, res.IsolationLimitation)
		}
	})

	t.Run("claude stream: the harness's own init report wins", func(t *testing.T) {
		skipNoPython(t)
		peer := writeFakeStreamPeer(t)
		runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceStream, peer+" --output-format stream-json")
		if err != nil {
			t.Fatal(err)
		}
		g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
		res := g.Invoke(context.Background(), InvokeRequest{
			// The peer reports two tools; the grant names three: the recorded
			// count must be the reported two.
			Binding: config.AgentBinding{Tools: ro, Principles: config.PrinciplesNone, Env: map[string]string{"STREAM_INIT_TOOLS": "Read,Grep"}},
			Section: "reviewer", Rubric: "judge", Payload: map[string]string{"story": "sty_1"},
			Expect: ExpectVerdict, Runner: runner, Skill: "test-skill",
		})
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		if res.SystemPromptBytes == 0 {
			t.Error("system_prompt_bytes not recorded")
		}
		if got := intp(res.OfferedToolCount); got != 2 || res.OfferedToolsSource != "harness" {
			t.Errorf("isolation = %v/%q, want 2/harness", got, res.OfferedToolsSource)
		}
	})

	t.Run("claude stream: no init report falls back to the rendered --tools", func(t *testing.T) {
		skipNoPython(t)
		peer := writeFakeStreamPeer(t)
		runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceStream, peer+" --output-format stream-json")
		if err != nil {
			t.Fatal(err)
		}
		g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
		res := g.Invoke(context.Background(), InvokeRequest{
			Binding: config.AgentBinding{Tools: ro, Principles: config.PrinciplesNone},
			Section: "reviewer", Rubric: "judge", Payload: map[string]string{"story": "sty_1"},
			Expect: ExpectVerdict, Runner: runner, Skill: "test-skill",
		})
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		if got := intp(res.OfferedToolCount); got != 3 || res.OfferedToolsSource != "flag" {
			t.Errorf("isolation = %v/%q, want 3/flag", got, res.OfferedToolsSource)
		}
	})

	t.Run("grok command: rendered --tools, source=flag", func(t *testing.T) {
		path, _ := startCounter(t, "grok-shim", isolationVerdict)
		res := reviewerInvoke(t, "command", strings.Replace(agentcli.DefaultGrokCommand, "grok ", path+" ", 1), ro)
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		if res.SystemPromptBytes == 0 {
			t.Error("system_prompt_bytes not recorded")
		}
		if got := intp(res.OfferedToolCount); got != 3 || res.OfferedToolsSource != "flag" {
			t.Errorf("isolation = %v/%q, want 3/flag", got, res.OfferedToolsSource)
		}
	})

}

func skipNoPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}
