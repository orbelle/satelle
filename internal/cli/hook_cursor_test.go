package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

// sty_7d098d50: cursor-agent held to the edit and commit gates. cursor's hooks.json
// has no tool matcher, so `hook gate` and `hook commitgate` see every tool call and
// classify it themselves. These tests feed them preToolUse payloads taken from the
// committed real captures (internal/agentcli/testdata/cursor).

// cursorFixtureDir is absolute: several tests chdir into a temp repo.
var cursorFixtureDir = func() string {
	abs, err := filepath.Abs("../agentcli/testdata/cursor")
	if err != nil {
		panic(err)
	}
	return abs
}()

// cursorCall is one captured preToolUse tool call.
type cursorCall struct {
	Src   string         // the capture file it came from
	Tool  string         // tool_name
	Input map[string]any // tool_input, recovered even when the log truncated it
}

var (
	cursorToolNameRe = regexp.MustCompile(`"tool_name":"([^"]+)"`)
	cursorScratchRe  = regexp.MustCompile(`/SCRATCH/sb[0-9a-z-]*`)
)

// loadCursorCalls reads the tool calls out of capture hook logs. A hook log keeps
// the first 300-400 characters of each payload, so a long tool_input ends
// mid-value: the string fields (command, file_path) are recovered up to the cut.
func loadCursorCalls(t *testing.T, files ...string) []cursorCall {
	t.Helper()
	var out []cursorCall
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(cursorFixtureDir, f))
		if err != nil {
			t.Fatalf("cursor capture %s: %v", f, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			m := cursorToolNameRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			out = append(out, cursorCall{Src: f, Tool: m[1], Input: decodeCursorToolInput(line)})
		}
	}
	if len(out) == 0 {
		t.Fatalf("no tool calls in %v", files)
	}
	return out
}

func decodeCursorToolInput(line string) map[string]any {
	i := strings.Index(line, `"tool_input":`)
	if i < 0 {
		return map[string]any{}
	}
	rest := line[i+len(`"tool_input":`):]
	var m map[string]any
	if json.NewDecoder(strings.NewReader(rest)).Decode(&m) == nil {
		return m
	}
	m = map[string]any{}
	for _, key := range []string{"command", "file_path"} {
		re := regexp.MustCompile(`"` + key + `":"((?:[^"\\]|\\.)*)`)
		if sm := re.FindStringSubmatch(rest); sm != nil {
			if s, err := strconv.Unquote(`"` + sm[1] + `"`); err == nil {
				m[key] = s
			} else {
				m[key] = sm[1]
			}
		}
	}
	return m
}

// payload is the preToolUse envelope cursor sends, with the capture's scratch
// sandbox rewritten to repo so a path lands inside the tree under test.
func (c cursorCall) payload(repo string) string {
	return cursorEnvelope(c.Tool, c.Input, repo)
}

func cursorEnvelope(tool string, input map[string]any, repo string) string {
	in := map[string]any{}
	for k, v := range input {
		if s, ok := v.(string); ok {
			v = cursorScratchRe.ReplaceAllString(s, repo)
		}
		in[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"tool_name": tool, "tool_input": in,
		"hook_event_name": "preToolUse", "cursor_version": "2026.10.01-e373342",
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// firstCursorCall returns the first capture call to tool whose command contains
// substr (any call when substr is empty).
func firstCursorCall(t *testing.T, calls []cursorCall, tool, substr string) cursorCall {
	t.Helper()
	for _, c := range calls {
		cmd, _ := c.Input["command"].(string)
		if c.Tool == tool && strings.Contains(cmd, substr) {
			return c
		}
	}
	t.Fatalf("no captured %s call containing %q", tool, substr)
	return cursorCall{}
}

// cursorCaptured is the call set every cursor gate test draws on.
type cursorCaptured struct {
	write, del, read, grep                    cursorCall
	rmShell, sedShell, rmSedShell, plainShell cursorCall
	all                                       []cursorCall
}

func loadCursorCaptured(t *testing.T) cursorCaptured {
	t.Helper()
	all := loadCursorCalls(t, "10a-hooks.log", "10b-hooks.log", "11a-hooks.log", "11b-hooks.log", "11c-hooks.log")
	return cursorCaptured{
		all:        all,
		write:      firstCursorCall(t, loadCursorCalls(t, "11a-hooks.log"), "Write", ""),
		del:        firstCursorCall(t, loadCursorCalls(t, "10a-hooks.log"), "Delete", ""),
		read:       firstCursorCall(t, loadCursorCalls(t, "10a-hooks.log"), "Read", ""),
		grep:       firstCursorCall(t, loadCursorCalls(t, "10a-hooks.log"), "Grep", ""),
		rmShell:    firstCursorCall(t, loadCursorCalls(t, "10b-hooks.log"), "Shell", "rm "),
		sedShell:   firstCursorCall(t, loadCursorCalls(t, "11c-hooks.log"), "Shell", "sed -i"),
		rmSedShell: firstCursorCall(t, loadCursorCalls(t, "11a-hooks.log"), "Shell", "rm -f"),
		plainShell: firstCursorCall(t, loadCursorCalls(t, "10a-hooks.log"), "Shell", "cat "),
	}
}

// cursorScenario arranges the engagement state a gate decides against.
type cursorScenario struct {
	name  string
	setup func(t *testing.T) string // returns the repo root
	// allowMutation: the state permits edits (a performing executor step).
	allowMutation bool
}

func cursorScenarios() []cursorScenario {
	pin := func(t *testing.T, repo string) string {
		// cursor sets CLAUDE_PROJECT_DIR for its hooks (testdata/cursor/clean-6-hook-env.txt).
		t.Setenv("CLAUDE_PROJECT_DIR", repo)
		t.Setenv("SATELLE_PROJECT_DIR", "")
		prev := hookHarnessFlag
		t.Cleanup(func() { hookHarnessFlag = prev })
		resolved, err := filepath.EvalSymlinks(repo)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	return []cursorScenario{
		{"no story engaged", func(t *testing.T) string {
			repo := tempRepo(t)
			t.Chdir(repo)
			return pin(t, repo)
		}, false},
		{"executor state", func(t *testing.T) string {
			repo, _ := liveSeatRepo(t)
			return pin(t, repo)
		}, true},
		{"planning state", func(t *testing.T) string {
			return pin(t, editStateRepo(t, "plan", "plan", false))
		}, false},
	}
}

// decideHook runs one hook verb and reports whether it denied, with its output.
func decideHook(t *testing.T, verb, harness, payload string) (denied bool, out string) {
	t.Helper()
	out, err := runRootIn(t, payload, "hook", verb, "--harness", harness)
	return err != nil, out
}

// verbFor is the verb the Claude scaffold routes a tool to: Write and Edit to
// gate, Bash (cursor's Shell) to commitgate.
func verbFor(tool string) string {
	if agentcli.ClassifyTool(tool) == agentcli.ClassShell {
		return "commitgate"
	}
	return "gate"
}

// AC4: satelle's reason reaches the model, and an unknown tool is named.
func TestCursorGateDenyReasons(t *testing.T) {
	cc := loadCursorCaptured(t)
	repo := cursorScenarios()[0].setup(t)

	_, out := decideHook(t, "gate", "cursor", cc.write.payload(repo))
	if !denyReasonContains(out, noEngagedStoryEditReason) {
		t.Errorf("Write deny lacks satelle's no-engaged-story reason:\n%s", out)
	}
	_, out = decideHook(t, "gate", "cursor", cc.del.payload(repo))
	if !denyReasonContains(out, noEngagedStoryEditReason) {
		t.Errorf("Delete deny lacks satelle's no-engaged-story reason:\n%s", out)
	}
	_, out = decideHook(t, "gate", "cursor", cursorEnvelope("FutureTool", map[string]any{}, repo))
	for _, want := range []string{"unrecognised cursor tool FutureTool", "cannot tell whether it mutates"} {
		if !strings.Contains(out, want) {
			t.Errorf("unknown-tool deny lacks %q:\n%s", want, out)
		}
	}
	// A payload with no tool name cannot be placed either.
	_, out = decideHook(t, "gate", "cursor", `{"cursor_version":"2026.10.01-e373342","tool_input":{}}`)
	if !strings.Contains(out, "unrecognised cursor tool") {
		t.Errorf("nameless payload should fail closed:\n%s", out)
	}
}

// AC5: with a story engaged, a cursor payload gets the decision a Claude payload
// for the same target gets — an executor state allows, a non-performing state
// denies — and an exempt path stays exempt.
func TestCursorEngagedPolicyMatchesClaude(t *testing.T) {
	for _, sc := range cursorScenarios()[1:] {
		t.Run(sc.name, func(t *testing.T) {
			repo := sc.setup(t)
			target := filepath.Join(repo, "internal", "foo.go")
			cursorDenied, _ := decideHook(t, "gate", "cursor", cursorEnvelope("Write", map[string]any{"file_path": target}, repo))
			claudeDenied, _ := decideHook(t, "gate", "claude", `{"tool_name":"Write","tool_input":{"file_path":`+strconv.Quote(target)+`}}`)
			if cursorDenied != claudeDenied || cursorDenied == sc.allowMutation {
				t.Errorf("cursor denied=%v claude denied=%v, want both %v", cursorDenied, claudeDenied, !sc.allowMutation)
			}
		})
	}

	// The exempt path is exercised under an engaged non-performing (planning)
	// story, where a non-exempt Write is denied, so the allow is the exemption's.
	t.Run("exempt path", func(t *testing.T) {
		repo := cursorScenarios()[2].setup(t)
		cfg := filepath.Join(repo, ".satelle", "satelle.toml")
		if err := os.WriteFile(cfg, []byte("[review]\ngate_create = false\n\n[gate]\nedit_exempt_paths = [\"docs/\"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		exempt := filepath.Join(repo, "docs", "note.md")
		for _, h := range []string{"cursor", "claude"} {
			if denied, out := decideHook(t, "gate", h, cursorEnvelope("Write", map[string]any{"file_path": exempt}, repo)); denied {
				t.Errorf("%s: exempt path denied:\n%s", h, out)
			}
		}
		if denied, _ := decideHook(t, "gate", "cursor", cursorEnvelope("Write", map[string]any{"file_path": filepath.Join(repo, "internal", "foo.go")}, repo)); !denied {
			t.Error("a non-exempt path must still be denied")
		}
	})
}

// The 11c capture is why: with a Claude matcher of Write|Edit|Bash, cursor's Delete
// escaped it (other.txt: absent) while the edit and the shell were refused.
func TestCursorDeleteEscapesClaudeMatcherCapture(t *testing.T) {
	fs, err := os.ReadFile(filepath.Join(cursorFixtureDir, "11c.fs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fs), "other.txt: absent") || !strings.Contains(string(fs), "line two") {
		t.Errorf("11c.fs: want the delete to have landed and the edit refused, got %q", fs)
	}
	settings, err := os.ReadFile(filepath.Join(cursorFixtureDir, "11c-claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), `"matcher":"Write|Edit|Bash"`) {
		t.Errorf("11c settings matcher changed: %s", settings)
	}
}

// AC3: no cursor tool name or harness literal in the cli's gate code — the names
// live in agentcli's table and the property in harnessHookSpec.NoToolMatcher.
func TestNoCursorLiteralsInCliGateCode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{`"Delete"`, `"Shell"`, `== "cursor"`}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, lit := range forbidden {
			if strings.Contains(string(raw), lit) {
				t.Errorf("%s contains %s; classify through agentcli.ClassifyTool and harnessHooks(...).NoToolMatcher", name, lit)
			}
		}
	}
}

// Only cursor sets the property today; a harness gaining it must be a decision.
func TestOnlyCursorHasNoToolMatcher(t *testing.T) {
	for _, h := range []string{"claude", "grok", "pi", "unknown", ""} {
		if agentcli.HarnessHooks(h).NoToolMatcher {
			t.Errorf("HarnessHooks(%q).NoToolMatcher is set", h)
		}
		if hs := agentcli.HarnessHooks(h); hs.GateMatcher == "" || hs.CommitMatcher == "" {
			t.Errorf("HarnessHooks(%q) has an empty matcher", h)
		}
	}
	hs := agentcli.HarnessHooks(agentcli.HarnessCursor)
	if !hs.NoToolMatcher || hs.GateMatcher != "" || hs.CommitMatcher != "" {
		t.Errorf("cursor spec = %+v, want NoToolMatcher with no matchers", hs)
	}
}

// A quoted argument names a target as a bare one does. cursor's shell tool quotes
// its absolute paths (testdata/cursor/10b, 11b), and the word list used to drop
// them, so `rm "f"` classified as no mutation at all.
func TestBashMutationTargetsSeeQuotedArgs(t *testing.T) {
	anchor := "/work/repo"
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{`rm "/work/repo/other.txt"`, []string{"/work/repo/other.txt"}},
		{`rm 'other.txt'`, []string{"/work/repo/other.txt"}},
		{`touch "a b.txt"`, []string{"/work/repo/a b.txt"}},
		{`cp src.txt "/work/repo/dest.txt"`, []string{"/work/repo/dest.txt"}},
		{`rm -f "/work/repo/x" other.txt`, []string{"/work/repo/x", "/work/repo/other.txt"}},
		{`echo "rm /work/repo/other.txt"`, nil},
		{`git commit -m "rm /work/repo/other.txt"`, nil},
	} {
		got, _ := bashMutationTargets(tc.command, anchor)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("bashMutationTargets(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
