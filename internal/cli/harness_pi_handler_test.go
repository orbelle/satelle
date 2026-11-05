package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// The pi extension is TypeScript, so its handlers can only be exercised by
// running it. These tests load the RENDERED extension under node against a stub
// of pi's ExtensionAPI (testdata/pi_driver.mjs), with the real wrapper deployed
// and a fake `satelle` standing in for the binary, so what is asserted is the
// path a pi session takes: pi event -> wrapper or direct command -> result.
//
// A missing node runtime FAILS the test. It is never skipped: a skip would let
// the enforcement path go unproven on exactly the machines that cannot run it.

const piNodeMissing = "node runtime required to drive pi extension handlers (sty_b3c7b37d)"

// piRig is one deployed extension plus the fake satelle it calls.
type piRig struct {
	t       *testing.T
	repo    string
	home    string
	fakeDir string // canned responses: <verb>.out, <verb>.code
	binDir  string // holds the fake `satelle`, put first on PATH
	log     string
	env     []string // extra environment for the driver and the satelle it runs
}

const fakeSatelle = `#!/bin/sh
verb="$1"
if [ "$1" = hook ]; then verb="$2"; fi
{ printf 'ARGV %s\n' "$*"; printf 'STDIN %s\n' "$(cat)"; } >> "$FAKE_LOG"
if [ -f "$FAKE_DIR/$verb.out" ]; then cat "$FAKE_DIR/$verb.out"; fi
code=0
if [ -f "$FAKE_DIR/$verb.code" ]; then code=$(cat "$FAKE_DIR/$verb.code"); fi
exit "$code"
`

func newPiRig(t *testing.T) *piRig {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &piRig{
		t:       t,
		repo:    repo,
		home:    t.TempDir(),
		fakeDir: t.TempDir(),
		binDir:  t.TempDir(),
	}
	r.log = filepath.Join(t.TempDir(), "fake.log")
	if _, _, _, err := scaffoldPiHooks(repo); err != nil {
		t.Fatalf("scaffoldPiHooks: %v", err)
	}
	// The wrapper finds the binary at <repo>/.satelle/satelle (SATELLE_PROJECT_DIR,
	// which the extension sets); the direct commands find it on PATH. HOME is a
	// temp dir so $HOME/.local/bin/satelle — probed first — cannot shadow the fake.
	for _, p := range []string{filepath.Join(repo, ".satelle", "satelle"), filepath.Join(r.binDir, "satelle")} {
		if err := os.WriteFile(p, []byte(fakeSatelle), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// can arranges what the fake prints (and exits with) for a verb.
func (r *piRig) can(verb, stdout string, code int) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.fakeDir, verb+".out"), []byte(stdout), 0o644); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.fakeDir, verb+".code"), []byte(strconv.Itoa(code)), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// canDeny arranges a real satelle deny for verb: the JSON emitPreToolUseDeny
// writes for the pi harness, and the non-zero exit the real handler returns.
func (r *piRig) canDeny(verb, reason string) {
	var buf bytes.Buffer
	if err := emitPreToolUseDeny(&buf, "pi", reason); err != nil {
		r.t.Fatal(err)
	}
	r.can(verb, buf.String(), 1)
}

type piStep struct {
	Event string         `json:"event,omitempty"`
	Arg   map[string]any `json:"arg,omitempty"`
	Ctx   map[string]any `json:"ctx,omitempty"`
	// WaitMessages, set instead of Event, waits until that many user messages
	// have been sent (the driver's own timeout bounds it).
	WaitMessages *int `json:"wait_messages,omitempty"`
	TimeoutMS    int  `json:"timeout_ms,omitempty"`
	// Connect, set instead of Event, is a host:port the driver dials when the
	// step is reached — after every earlier step has returned.
	Connect string `json:"connect,omitempty"`
}

type piResult struct {
	Event  string         `json:"event"`
	Result map[string]any `json:"result"`
	Threw  *string        `json:"threw"`
}

type piOut struct {
	Registered []string   `json:"registered"`
	Results    []piResult `json:"results"`
	Messages   []struct {
		Text string         `json:"text"`
		Opts map[string]any `json:"opts"`
	} `json:"messages"`
	Notices []struct {
		Msg   string `json:"msg"`
		Level string `json:"level"`
	} `json:"notices"`
}

// drive runs steps through the extension. withSatelleOnPath=false removes the
// fake from PATH, the "satelle is not installed" case for the direct commands.
func (r *piRig) drive(withSatelleOnPath bool, steps ...piStep) piOut {
	r.t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		r.t.Fatal(piNodeMissing)
	}
	for i := range steps {
		if steps[i].Ctx == nil {
			steps[i].Ctx = map[string]any{}
		}
		if _, ok := steps[i].Ctx["cwd"]; !ok {
			steps[i].Ctx["cwd"] = r.repo
		}
	}
	stepsPath := filepath.Join(r.t.TempDir(), "steps.json")
	raw, _ := json.Marshal(steps)
	if err := os.WriteFile(stepsPath, raw, 0o644); err != nil {
		r.t.Fatal(err)
	}
	pathEnv := "/usr/bin:/bin"
	if withSatelleOnPath {
		pathEnv = r.binDir + ":" + pathEnv
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types",
		filepath.Join("testdata", "pi_driver.mjs"),
		filepath.Join(r.repo, filepath.FromSlash(piExtensionRel)), stepsPath)
	cmd.Env = append([]string{"HOME=" + r.home, "PATH=" + pathEnv, "TMPDIR=" + r.t.TempDir(),
		"FAKE_LOG=" + r.log, "FAKE_DIR=" + r.fakeDir}, r.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("pi driver: %v\nstderr:\n%s", err, stderr.String())
	}
	var out piOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		r.t.Fatalf("driver output is not JSON: %v\n%s", err, stdout.String())
	}
	return out
}

// argv returns the fake's recorded command lines, in order.
func (r *piRig) argv() []string {
	return r.logLines("ARGV ")
}

// stdin returns the fake's recorded stdin payloads, in order.
func (r *piRig) stdin() []string {
	return r.logLines("STDIN ")
}

func (r *piRig) logLines(prefix string) []string {
	raw, err := os.ReadFile(r.log)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, strings.TrimPrefix(l, prefix))
		}
	}
	return out
}

func toolStep(tool string, input map[string]any) piStep {
	return piStep{Event: "tool_call", Arg: map[string]any{"toolName": tool, "input": input}}
}

// blocked returns the block reason of a tool_call result, and whether it blocked.
func blocked(res piResult) (string, bool) {
	if res.Result == nil {
		return "", false
	}
	b, _ := res.Result["block"].(bool)
	reason, _ := res.Result["reason"].(string)
	return reason, b
}

// AC2: a deny is a BLOCK the harness enforces, not a notification.
func TestPiHandler_EditDeniedBecomesBlock(t *testing.T) {
	r := newPiRig(t)
	r.canDeny("gate", noEngagedStoryEditReason)
	out := r.drive(true, toolStep("edit", map[string]any{"path": "internal/foo.go"}))
	reason, ok := blocked(out.Results[0])
	if !ok {
		t.Fatalf("a denied edit must return {block:true}; got %+v (notices %+v)", out.Results[0], out.Notices)
	}
	if reason != noEngagedStoryEditReason {
		t.Fatalf("the refusal must name the edit-gate rule; reason = %q", reason)
	}
	if len(out.Notices) != 0 {
		t.Errorf("a deny is enforced, not merely announced: %+v", out.Notices)
	}
	if got := r.argv(); len(got) != 1 || got[0] != "hook gate --harness pi" {
		t.Fatalf("gate must run through the wrapper as `hook gate --harness pi`; argv = %v", got)
	}
	// AC3 normalisation: pi's `path` reaches satelle as the claude-shaped file_path.
	var ev map[string]any
	if err := json.Unmarshal([]byte(r.stdin()[0]), &ev); err != nil {
		t.Fatalf("stdin to satelle is not JSON: %v", err)
	}
	ti, _ := ev["tool_input"].(map[string]any)
	if ev["tool_name"] != "Edit" || ti["file_path"] != "internal/foo.go" || ev["hook_event_name"] != "PreToolUse" {
		t.Fatalf("event not normalised to the claude shape: %v", ev)
	}
}

// AC4 (allow): an engaged story's edit gets no verdict from satelle and proceeds.
func TestPiHandler_EngagedEditAllowed(t *testing.T) {
	r := newPiRig(t)
	r.can("gate", "", 0)
	out := r.drive(true, toolStep("write", map[string]any{"path": "internal/foo.go", "content": "x"}))
	if out.Results[0].Result != nil || out.Results[0].Threw != nil {
		t.Fatalf("an allowed edit must return nothing and not throw: %+v", out.Results[0])
	}
	if len(r.argv()) != 1 {
		t.Fatalf("the write must still be asked of satelle: %v", r.argv())
	}
}

// AC5: SessionStart context and the next prompt's reminder both reach the model,
// through DIRECT commands — the wrapper is deleted so it cannot be what carried them.
func TestPiHandler_SessionContextAndPromptReminder(t *testing.T) {
	r := newPiRig(t)
	if err := os.Remove(filepath.Join(r.repo, filepath.FromSlash(satelleHookScriptRel))); err != nil {
		t.Fatal(err)
	}
	r.can("reindex", "", 0)
	r.can("context", `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"SESSION-PRINCIPLES-MARKER"}}`, 0)
	r.can("prompt", `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"EDITS-REQUIRE-A-STORY-MARKER"}}`, 0)
	out := r.drive(true,
		piStep{Event: "session_start", Arg: map[string]any{}},
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "do it", "systemPrompt": "BASE PROMPT"}},
	)
	sp, _ := out.Results[1].Result["systemPrompt"].(string)
	for _, want := range []string{"BASE PROMPT", "SESSION-PRINCIPLES-MARKER", "EDITS-REQUIRE-A-STORY-MARKER"} {
		if !strings.Contains(sp, want) {
			t.Errorf("system prompt lacks %q: %q", want, sp)
		}
	}
	want := []string{"reindex", "hook context --harness pi", "hook prompt --harness pi"}
	if got := r.argv(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	// The session context is re-asserted on a later run, not only the first.
	out = r.drive(true,
		piStep{Event: "session_start", Arg: map[string]any{}},
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "a", "systemPrompt": "B"}},
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "b", "systemPrompt": "B"}},
	)
	if sp2, _ := out.Results[2].Result["systemPrompt"].(string); !strings.Contains(sp2, "SESSION-PRINCIPLES-MARKER") {
		t.Errorf("session context must ride every run: %q", sp2)
	}
	// A prompt event carries the user's prompt to satelle in the claude shape.
	var ev map[string]any
	_ = json.Unmarshal([]byte(r.stdin()[2]), &ev)
	if ev["hook_event_name"] != "UserPromptSubmit" || ev["prompt"] != "do it" {
		t.Errorf("prompt event = %v", ev)
	}
}

// sty_15a374a8 AC6: a notice satelle puts in the session-context answer's
// systemMessage reaches the USER through pi's notify(); an answer without one
// notifies nothing, and the model-facing context is unchanged either way.
func TestPiHandler_SessionStartNoticeReachesUser(t *testing.T) {
	r := newPiRig(t)
	r.can("reindex", "", 0)
	r.can("context", string(agentcli.SessionContextOutput("pi", "SessionStart", "SESSION-PRINCIPLES-MARKER", "RELEASE-NOTICE-MARKER")), 0)
	out := r.drive(true,
		piStep{Event: "session_start", Arg: map[string]any{}},
		piStep{Event: "before_agent_start", Arg: map[string]any{"prompt": "go", "systemPrompt": "BASE"}},
	)
	if len(out.Notices) != 1 || out.Notices[0].Msg != "RELEASE-NOTICE-MARKER" || out.Notices[0].Level != "info" {
		t.Fatalf("session_start must notify the user once with the notice: %+v", out.Notices)
	}
	if sp, _ := out.Results[1].Result["systemPrompt"].(string); !strings.Contains(sp, "SESSION-PRINCIPLES-MARKER") {
		t.Errorf("the model context must still ride the system prompt: %q", sp)
	}

	r.can("context", string(agentcli.SessionContextOutput("pi", "SessionStart", "SESSION-PRINCIPLES-MARKER", "")), 0)
	out = r.drive(true, piStep{Event: "session_start", Arg: map[string]any{}})
	if len(out.Notices) != 0 {
		t.Fatalf("no notice, no notification: %+v", out.Notices)
	}
}

// AC6: a refused stop comes back as a message that starts another turn, and the
// extension honours satelle's own anti-loop (stop_hook_active).
func TestPiHandler_StopRefusalIsRePrompted(t *testing.T) {
	r := newPiRig(t)
	block := `{"decision":"block","reason":"STOP BLOCKED: ungated edit of internal/foo.go"}`
	r.can("stopcheck", block, 0)
	out := r.drive(true,
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": false}},
		piStep{Event: "agent_settled", Ctx: map[string]any{"idle": true}},
	)
	if len(out.Messages) != 2 {
		t.Fatalf("each refused settle must send the reason back: %+v", out.Messages)
	}
	if out.Messages[0].Text != "STOP BLOCKED: ungated edit of internal/foo.go" || out.Messages[0].Opts["deliverAs"] != "followUp" {
		t.Errorf("a busy session queues the refusal as a follow-up: %+v", out.Messages[0])
	}
	if out.Messages[1].Opts != nil {
		t.Errorf("an idle session starts a turn with a plain message: %+v", out.Messages[1])
	}
	in := r.stdin()
	if len(in) != 2 || !strings.Contains(in[0], `"stop_hook_active":false`) || !strings.Contains(in[1], `"stop_hook_active":true`) {
		t.Fatalf("the second settle follows our own re-prompt and must say so: %v", in)
	}

	// Once stopcheck allows, the chain is over: the next settle starts clean.
	r2 := newPiRig(t)
	r2.can("stopcheck", "", 0)
	out = r2.drive(true, piStep{Event: "agent_settled"})
	if len(out.Messages) != 0 || len(out.Notices) != 0 {
		t.Fatalf("an allowed stop is silent: %+v %+v", out.Messages, out.Notices)
	}
}

// AC8b, closed side: PreToolUse fails visible. An unusable satelle refuses edits
// and commit/push, and leaves non-mutating bash usable so the agent can diagnose.
func TestPiHandler_InfraFailureFailsClosedThroughWrapper(t *testing.T) {
	r := newPiRig(t)
	for _, verb := range []string{"gate", "commitgate"} {
		r.can(verb, "not json at all", 2)
	}
	out := r.drive(true,
		toolStep("edit", map[string]any{"path": "internal/foo.go"}),
		toolStep("bash", map[string]any{"command": "git commit -m x"}),
		toolStep("bash", map[string]any{"command": "git push origin main"}),
		toolStep("bash", map[string]any{"command": "ls"}),
	)
	for i := 0; i < 3; i++ {
		reason, ok := blocked(out.Results[i])
		if !ok || !strings.Contains(reason, "INFRASTRUCTURE") {
			t.Errorf("step %d must fail closed with the infra reason: %+v", i, out.Results[i])
		}
	}
	if out.Results[3].Result != nil {
		t.Errorf("non-mutating bash stays allowed when satelle is unusable: %+v", out.Results[3])
	}
}

// AC8b: the wrapper file itself gone is the same failure, reported as such.
func TestPiHandler_MissingWrapperBlocks(t *testing.T) {
	r := newPiRig(t)
	if err := os.Remove(filepath.Join(r.repo, filepath.FromSlash(satelleHookScriptRel))); err != nil {
		t.Fatal(err)
	}
	out := r.drive(true,
		toolStep("edit", map[string]any{"path": "internal/foo.go"}),
		toolStep("bash", map[string]any{"command": "git commit -m x"}),
		toolStep("bash", map[string]any{"command": "ls"}),
	)
	for i := 0; i < 2; i++ {
		reason, ok := blocked(out.Results[i])
		if !ok || !strings.Contains(reason, "wrapper unavailable") {
			t.Errorf("step %d: a missing wrapper must block, naming it: %+v", i, out.Results[i])
		}
	}
	if out.Results[2].Result != nil {
		t.Errorf("non-mutating bash stays allowed with the wrapper gone: %+v", out.Results[2])
	}
	if len(r.argv()) != 0 {
		t.Errorf("satelle must not have been reached: %v", r.argv())
	}
}

// AC8b, open side: SessionStart, prompt and Stop fail OPEN exactly as claude's
// direct commands do — a failing or absent satelle never wedges a session.
func TestPiHandler_DirectHooksFailOpen(t *testing.T) {
	steps := []piStep{
		{Event: "session_start"},
		{Event: "before_agent_start", Arg: map[string]any{"prompt": "x", "systemPrompt": "B"}},
		{Event: "agent_settled"},
	}
	check := func(t *testing.T, out piOut) {
		t.Helper()
		for _, res := range out.Results {
			if res.Threw != nil {
				t.Errorf("%s threw: %s", res.Event, *res.Threw)
			}
			if res.Result != nil {
				t.Errorf("%s must add nothing when satelle is unusable: %+v", res.Event, res.Result)
			}
		}
		if len(out.Messages) != 0 {
			t.Errorf("no re-prompt without a verdict: %+v", out.Messages)
		}
		if len(out.Notices) == 0 {
			t.Errorf("a failing hook must at least say so")
		}
	}
	t.Run("satelle fails", func(t *testing.T) {
		r := newPiRig(t)
		for _, verb := range []string{"reindex", "context", "prompt", "stopcheck"} {
			r.can(verb, "garbage", 3)
		}
		check(t, r.drive(true, steps...))
		if len(r.argv()) == 0 {
			t.Fatal("the fake must have been the binary that ran")
		}
	})
	t.Run("satelle absent from PATH", func(t *testing.T) {
		r := newPiRig(t)
		check(t, r.drive(false, steps...))
		if got := r.argv(); len(got) != 0 {
			t.Fatalf("no satelle was on PATH yet one ran: %v", got)
		}
	})
}

// AC9: the extension registers exactly the pi events the event map names.
func TestPiHandler_RegistersTheMappedEvents(t *testing.T) {
	r := newPiRig(t)
	out := r.drive(true)
	want := map[string]bool{}
	for _, b := range piBindings() {
		want[b.Pi] = true
	}
	var wantList, gotList []string
	for e := range want {
		wantList = append(wantList, e)
	}
	seen := map[string]bool{}
	for _, e := range out.Registered {
		if !seen[e] {
			seen[e] = true
			gotList = append(gotList, e)
		}
	}
	sort.Strings(wantList)
	sort.Strings(gotList)
	if strings.Join(wantList, ",") != strings.Join(gotList, ",") {
		t.Fatalf("registered pi events = %v, event map = %v", gotList, wantList)
	}
}
