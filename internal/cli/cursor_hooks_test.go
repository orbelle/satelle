package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// isolateCursor is isolate with Cursor's hook environment cleared.
func isolateCursor(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	t.Setenv("CURSOR_PROJECT_DIR", "")
	t.Setenv("CURSOR_VERSION", "")
	return dir
}

// hookTestdata is internal/hook/testdata, resolved before a test changes dir.
var hookTestdata = func() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "hook", "testdata")
}()

// cursorPayload is a captured Cursor hook payload (internal/hook/testdata,
// from the sto_e51c7fb9 probe) rewritten for this test: its session, its
// workspace root, and the edit path or shell command when given. cwd stays
// as Cursor sends it (null for edits, "" for Shell).
func cursorPayload(t *testing.T, fixture, session, root, path, command string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(hookTestdata, "cursor-"+fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc["session_id"], doc["conversation_id"], doc["generation_id"] = session, session, session
	doc["workspace_roots"] = []string{root}
	if ti, ok := doc["tool_input"].(map[string]any); ok {
		if path != "" {
			ti["file_path"] = path
		}
		if command != "" {
			ti["command"] = command
		}
	}
	out, _ := json.Marshal(doc)
	return string(out)
}

// claudePayload is a claude PreToolUse payload for the same call.
func claudePayload(session, cwd, tool, path, command string) string {
	b, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": session, "cwd": cwd, "tool_name": tool,
		"tool_input": map[string]string{"file_path": path, "command": command},
	})
	return string(b)
}

// cursorAnswer runs a hook verb under --harness cursor and decodes its answer
// ("" decodes to nil).
func cursorAnswer(t *testing.T, payload, verb string) (raw string, doc map[string]any) {
	t.Helper()
	out, err := run(t, payload, "hook", verb, "--harness", "cursor")
	if err != nil {
		t.Fatalf("hook %s: %v\n%s", verb, err, out)
	}
	raw = strings.TrimSpace(out)
	if raw == "" {
		return raw, nil
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("hook %s answer %q: %v", verb, raw, err)
	}
	return raw, doc
}

// claudeDenyReason runs the claude gate and returns its deny reason ("" when
// it allows).
func claudeDenyReason(t *testing.T, payload string) string {
	t.Helper()
	out, err := run(t, payload, "hook", "gate", "--harness", "claude")
	if err != nil {
		t.Fatalf("claude gate: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		return ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.HookSpecificOutput.PermissionDecisionReason
}

// cursorDeny asserts a Cursor gate answer is a deny and returns its reason.
func cursorDeny(t *testing.T, raw string, doc map[string]any) string {
	t.Helper()
	if doc["permission"] != "deny" {
		t.Fatalf("not a Cursor deny: %s", raw)
	}
	reason, _ := doc["user_message"].(string)
	if reason == "" || doc["agent_message"] != reason {
		t.Fatalf("Cursor deny does not carry the reason in user_message and agent_message: %s", raw)
	}
	return reason
}

func appendSettings(t *testing.T, dir, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".satelle", "satelle.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// satelle agents install cursor writes .cursor/hooks.json keeping the user's
// entries; remove strips only satelle's (sto_e51c7fb9 AC2).
func TestAgentsInstallCursor(t *testing.T) {
	dir := isolateCursor(t)
	must(t, "init")
	path := filepath.Join(dir, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := `{"version":1,"hooks":{"sessionStart":[{"command":"./mine.sh"}]}}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := must(t, "agents", "install", "cursor"); !strings.Contains(out, "installed cursor hooks: "+path) {
		t.Fatalf("install: %s", out)
	}
	b, _ := os.ReadFile(path)
	for _, verb := range []string{hook.VerbGate, hook.VerbContext, hook.VerbPrompt, hook.VerbStopcheck} {
		if strings.Count(string(b), hook.Command(verb, hook.Cursor)) != 1 {
			t.Errorf("%s entry missing:\n%s", verb, b)
		}
	}
	if !strings.Contains(string(b), "./mine.sh") {
		t.Fatalf("user entry lost:\n%s", b)
	}
	if out := must(t, "agents", "remove", "cursor"); !strings.Contains(out, "removed cursor hooks: "+path) {
		t.Fatalf("remove: %s", out)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "satelle hook") || !strings.Contains(string(b), "./mine.sh") {
		t.Fatalf("after remove:\n%s", b)
	}
}

// A Cursor session's unseated edit (Write and Delete) and its operator-only
// shell verb are refused with the claude gate's reason, byte for byte; once
// seated in a performing state the edit passes (sto_e51c7fb9 AC3).
func TestCursorGateMatchesClaudeGate(t *testing.T) {
	dir := isolateCursor(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")
	main := filepath.Join(dir, "main.go")

	for _, fx := range []string{"pretooluse-write", "pretooluse-delete"} {
		raw, doc := cursorAnswer(t, cursorPayload(t, fx, "c-1", dir, main, ""), "gate")
		got := cursorDeny(t, raw, doc)
		want := claudeDenyReason(t, claudePayload("c-1", dir, "Edit", main, ""))
		if want == "" || got != want {
			t.Errorf("%s: cursor reason\n%q\nclaude reason\n%q", fx, got, want)
		}
	}
	// A shell write into this repo's own tree reaches the seat check (the
	// operator-verb and fence checks pass it): unseated, it is refused with
	// the claude gate's seat reason.
	shellWrite := "echo x > " + main
	raw, doc := cursorAnswer(t, cursorPayload(t, "pretooluse-shell", "c-1", dir, "", shellWrite), "gate")
	gotSh := cursorDeny(t, raw, doc)
	wantSh := claudeDenyReason(t, claudePayload("c-1", dir, "Bash", "", shellWrite))
	if wantSh == "" || gotSh != wantSh || !strings.Contains(gotSh, "seat") {
		t.Errorf("unseated shell write: cursor reason\n%q\nclaude reason\n%q", gotSh, wantSh)
	}
	record := "satelle story record " + id + " --file note.md"
	raw, doc = cursorAnswer(t, cursorPayload(t, "pretooluse-shell", "c-1", dir, "", record), "gate")
	if got := cursorDeny(t, raw, doc); got != hook.OperatorRecordDenied || got != claudeDenyReason(t, claudePayload("c-1", dir, "Bash", "", record)) {
		t.Errorf("operator record: cursor reason %q", got)
	}

	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "seat", id, "--session", "c-1")
	for _, fx := range []string{"pretooluse-write", "pretooluse-delete"} {
		if raw, _ := cursorAnswer(t, cursorPayload(t, fx, "c-1", dir, main, ""), "gate"); raw != "" {
			t.Errorf("%s: a seated Cursor edit was answered: %s", fx, raw)
		}
	}
	if raw, _ := cursorAnswer(t, cursorPayload(t, "pretooluse-shell", "c-1", dir, "", shellWrite), "gate"); raw != "" {
		t.Errorf("a seated Cursor shell write in this repo was answered: %s", raw)
	}
	raw, doc = cursorAnswer(t, cursorPayload(t, "pretooluse-shell", "c-1", dir, "", record), "gate")
	if cursorDeny(t, raw, doc) != hook.OperatorRecordDenied {
		t.Error("a seat allowed the operator record verb")
	}
}

// The other-tree fence holds for Cursor, with the session's repo known only
// from CURSOR_PROJECT_DIR: the process runs elsewhere, the payload has no cwd
// and no workspace roots, and CLAUDE_PROJECT_DIR is unset. A seated edit in
// the repo passes (so the repo was found from CURSOR_PROJECT_DIR); an edit or
// shell write into another repository's tree is refused with the claude
// gate's fence reason.
func TestCursorGateFencesAnotherRepository(t *testing.T) {
	dir := isolateCursor(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")
	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "seat", id, "--session", "c-1")

	foreign, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, foreign, "init", "-q")
	gittest.Git(t, foreign, "commit", "-q", "--allow-empty", "-m", "init")
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	// Both repositories live under the temp dir, which the gate leaves alone;
	// narrow its temp roots to a dir holding neither so the fence is reached.
	hookTempRoots = []string{elsewhere}
	t.Cleanup(func() { hookTempRoots = nil })
	t.Setenv("CURSOR_PROJECT_DIR", dir)

	strip := func(p string) string {
		var doc map[string]any
		_ = json.Unmarshal([]byte(p), &doc)
		delete(doc, "workspace_roots")
		delete(doc, "cwd")
		b, _ := json.Marshal(doc)
		return string(b)
	}
	if raw, _ := cursorAnswer(t, strip(cursorPayload(t, "pretooluse-write", "c-1", dir, filepath.Join(dir, "main.go"), "")), "gate"); raw != "" {
		t.Fatalf("a seated edit in the CURSOR_PROJECT_DIR repo was answered: %s", raw)
	}
	target := filepath.Join(foreign, "main.go")
	raw, doc := cursorAnswer(t, strip(cursorPayload(t, "pretooluse-write", "c-1", dir, target, "")), "gate")
	got := cursorDeny(t, raw, doc)
	if !strings.Contains(got, "another repository's working tree ("+foreign+")") || !strings.Contains(got, "This session works in "+dir) {
		t.Fatalf("fence reason: %q", got)
	}
	t.Setenv("CURSOR_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", dir)
	if want := claudeDenyReason(t, claudePayload("c-1", dir, "Edit", target, "")); got != want {
		t.Errorf("cursor fence reason\n%q\nclaude\n%q", got, want)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("CURSOR_PROJECT_DIR", dir)
	raw, doc = cursorAnswer(t, strip(cursorPayload(t, "pretooluse-shell", "c-1", dir, "", "echo x > "+target)), "gate")
	if got := cursorDeny(t, raw, doc); !strings.Contains(got, "another repository's working tree") {
		t.Errorf("shell write fence reason: %q", got)
	}
}

// Cursor's sessionStart gets the session id, its seats and the [hook] inject
// files as additional_context (sto_e51c7fb9 AC3).
func TestCursorContextCarriesSessionSeatsAndInject(t *testing.T) {
	dir := isolateCursor(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "principles.md"), []byte("nonce-5d1e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendSettings(t, dir, "\n[hook]\ninject = [\"principles.md\"]\n")
	id := createID(t, "story", "create", "--title", "seated work", "--workflow", "gated")
	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "seat", id, "--session", "c-ctx")
	raw, doc := cursorAnswer(t, cursorPayload(t, "sessionstart", "c-ctx", dir, "", ""), "context")
	text, _ := doc["additional_context"].(string)
	if len(doc) != 1 || text == "" {
		t.Fatalf("not a Cursor sessionStart answer: %s", raw)
	}
	for _, want := range []string{"satelle session: c-ctx", id + "  seated work", "nonce-5d1e"} {
		if !strings.Contains(text, want) {
			t.Errorf("context lacks %q:\n%s", want, text)
		}
	}
}

// Cursor's beforeSubmitPrompt runs the prompt hook: it settles the session's
// owner and, since that event has no context channel, answers only
// {"continue":true}. Cursor's stop runs the stop check, which answers {} (no
// followup_message, so Cursor does not continue the turn).
func TestCursorPromptAndStopAnswers(t *testing.T) {
	dir := isolateCursor(t)
	anc := procgroup.Ancestors(os.Getpid())
	must(t, "init")
	common := `{"conversation_id":"c-p","generation_id":"c-p","session_id":"c-p","cursor_version":"2026.11.05-86047df","workspace_roots":[` + jsonString(dir) + `]`
	start := common + `,"hook_event_name":"sessionStart"}`
	if raw, doc := cursorAnswer(t, start, "context"); doc["additional_context"] == nil {
		t.Fatalf("sessionStart: %s", raw)
	}
	prompt := common + `,"hook_event_name":"beforeSubmitPrompt","prompt":"hello","attachments":[]}`
	if raw, _ := cursorAnswer(t, prompt, "prompt"); raw != `{"continue":true}` {
		t.Errorf("beforeSubmitPrompt answer %q, want {\"continue\":true}", raw)
	}
	if len(anc) > 0 {
		rec, _ := seatlife.Load(config.GlobalDir(), "c-p")
		if rec.Owner == nil || *rec.Owner != anc[0] {
			t.Errorf("the prompt hook did not settle the owner: %+v, want %+v", rec.Owner, anc[0])
		}
	}
	stop := common + `,"hook_event_name":"stop","status":"completed","loop_count":0}`
	raw, doc := cursorAnswer(t, stop, "stopcheck")
	if raw != `{}` || len(doc) != 0 {
		t.Errorf("stop answer %q, want {}", raw)
	}
	if out := must(t, "hook", "stopcheck", "--harness", "claude"); strings.TrimSpace(out) != "" {
		t.Errorf("claude stop answer changed: %q", out)
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// Once .cursor/hooks.json holds satelle's entries, Cursor's run of the
// .claude/settings.json entries (its Claude Code compatibility; the payload
// carries cursor_version) answers nothing, so nothing is judged or injected
// twice; a plain claude payload still answers.
func TestClaudeHooksDeferToCursorInstall(t *testing.T) {
	dir := isolateCursor(t)
	must(t, "init")
	main := filepath.Join(dir, "main.go")
	compat := cursorPayload(t, "claudecompat-pretooluse", "c-d", dir, main, "")
	start := cursorPayload(t, "sessionstart", "c-d", dir, "", "")
	gate := func(p string) string {
		out, err := run(t, p, "hook", "gate", "--harness", "claude")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	ctx := func(p string) string {
		out, err := run(t, p, "hook", "context", "--harness", "claude")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	prompt := func(p string) string {
		out, err := run(t, p, "hook", "prompt", "--harness", "claude")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	if gate(compat) == "" || ctx(start) == "" || prompt(start) == "" {
		t.Fatal("without the cursor install, Cursor's run of the claude entries answers nothing")
	}
	must(t, "agents", "install", "cursor")
	if g, c, p := gate(compat), ctx(start), prompt(start); g != "" || c != "" || p != "" {
		t.Errorf("deferred claude entries answered: gate %q context %q prompt %q", g, c, p)
	}
	if gate(claudePayload("c-d", dir, "Edit", main, "")) == "" {
		t.Error("a claude session's gate stopped answering once cursor was installed")
	}
	t.Setenv("CURSOR_VERSION", "2026.11.05")
	if gate(claudePayload("c-d", dir, "Edit", main, "")) == "" {
		t.Error("CURSOR_VERSION in the environment silenced a claude payload")
	}
}
