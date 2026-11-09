package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// newRepo is a git repo with one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "init", "-q")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestEditGateTable(t *testing.T) {
	root := newRepo(t)
	foreign := newRepo(t)
	linked := filepath.Join(t.TempDir(), "wt")
	gittest.Git(t, root, "worktree", "add", "-q", linked)
	linked, _ = filepath.EvalSymlinks(linked)
	plain, _ := filepath.EvalSymlinks(t.TempDir()) // in no working tree
	temp, _ := filepath.EvalSymlinks(t.TempDir())  // the configured temp root

	seats := map[string]bool{}
	g := Gate{
		Root:      root,
		Exempt:    []string{"docs/**", "*.md"},
		Locked:    []string{"locked/**"},
		TempRoots: []string{temp},
		Seated:    func(tree string) bool { return seats[tree] },
	}
	type tc struct {
		name   string
		seated []string
		edit   string // absolute or relative to root
		bash   string // run in root when set
		allow  bool
	}
	for _, c := range []tc{
		{name: "unseated edit in tree", edit: "main.go", allow: false},
		{name: "seated edit in tree", seated: []string{root}, edit: "main.go", allow: true},
		{name: "seat at another tree does not count", seated: []string{linked}, edit: "main.go", allow: false},
		{name: "exempt glob", edit: "docs/a/b.txt", allow: true},
		{name: "exempt basename glob", edit: "sub/notes.md", allow: true},
		{name: "locked even when seated", seated: []string{root}, edit: "locked/x", allow: false},
		{name: "temp dir", edit: filepath.Join(temp, "draft.txt"), allow: true},
		{name: "no working tree", edit: filepath.Join(plain, "x.txt"), allow: true},
		{name: "foreign repository fenced even when seated", seated: []string{root}, edit: filepath.Join(foreign, "x.go"), allow: false},
		{name: "linked worktree needs its own seat", seated: []string{root}, edit: filepath.Join(linked, "x.go"), allow: false},
		{name: "linked worktree seated", seated: []string{linked}, edit: filepath.Join(linked, "x.go"), allow: true},
		{name: "bash read", bash: "ls -la && cat main.go | grep x", allow: true},
		{name: "bash redirect unseated", bash: "echo hi > out.txt", allow: false},
		{name: "bash redirect seated", seated: []string{root}, bash: "echo hi > out.txt", allow: true},
		{name: "bash redirect to exempt", bash: "echo hi >> docs/log.txt", allow: true},
		{name: "bash rm in foreign repo", seated: []string{root}, bash: "rm " + filepath.Join(foreign, "x.go"), allow: false},
		{name: "bash cp into temp", bash: "cp main.go " + filepath.Join(temp, "x"), allow: true},
		{name: "bash sed -i unseated", bash: "sed -i 's/a/b/' main.go", allow: false},
		{name: "bash inline interpreter unseated", bash: `python3 -c 'open("x","w").write("y")'`, allow: false},
		{name: "bash inline interpreter seated", seated: []string{root}, bash: `python3 -c 'print(1)'`, allow: true},
		{name: "bash git read", bash: "git -C " + foreign + " status", allow: true},
		{name: "bash git -C write foreign", seated: []string{root}, bash: "git -C " + foreign + " commit -m x", allow: false},
		{name: "bash sh -c program judged by its writes", bash: `sh -c 'echo hi > y.txt'`, allow: false},
		{name: "bash chmod scratch file unseated", bash: "chmod +x " + filepath.Join(temp, "x.sh"), allow: true},
		{name: "bash chmod repo file unseated", bash: "chmod +x main.go", allow: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			clear(seats)
			for _, s := range c.seated {
				seats[s] = true
			}
			var v Verdict
			if c.bash != "" {
				v = g.Bash(c.bash, root)
			} else {
				v = g.Edit(c.edit)
			}
			if v.Allow != c.allow {
				t.Fatalf("allow = %v, want %v (%s)", v.Allow, c.allow, v.Reason)
			}
			if !v.Allow && !strings.HasPrefix(v.Reason, "satelle") && !strings.HasPrefix(v.Reason, "an inline program") {
				t.Errorf("deny reason does not explain itself: %q", v.Reason)
			}
		})
	}
}

// TestGateChmodModeOperand pins that chmod's mode is not judged as an edit:
// from an unseated repo cwd a scratch-file chmod is allowed, and an in-repo
// chmod is refused naming the file, never the mode.
func TestGateChmodModeOperand(t *testing.T) {
	root := newRepo(t)
	g := Gate{Root: root, TempRoots: []string{"/tmp"}, Seated: func(string) bool { return false }}
	for _, cmd := range []string{"chmod +x /tmp/x.sh", "chmod u+w /tmp/x.sh", "chmod 755 /tmp/x.sh", "chmod a=r /tmp/x.sh"} {
		if v := g.Bash(cmd, root); !v.Allow {
			t.Errorf("%q: denied (%s), want allowed", cmd, v.Reason)
		}
	}
	for _, cmd := range []string{"chmod +x sub/run.sh", "chmod 755 sub/run.sh"} {
		v := g.Bash(cmd, root)
		if v.Allow {
			t.Fatalf("%q: allowed, want refused", cmd)
		}
		if v.Path != filepath.Join(root, "sub/run.sh") {
			t.Errorf("%q: refused path %q, want %q", cmd, v.Path, filepath.Join(root, "sub/run.sh"))
		}
		if !strings.Contains(v.Reason, "edit to sub/run.sh blocked") || strings.Contains(v.Reason, "+x") {
			t.Errorf("%q: reason does not name the file alone: %q", cmd, v.Reason)
		}
	}
	// A mode-shaped word after the first positional is a file chmod writes.
	if v := g.Bash("chmod /tmp/x.sh +x", root); v.Allow || v.Path != filepath.Join(root, "+x") {
		t.Errorf("chmod /tmp/x.sh +x: allow %v path %q, want refused at %q", v.Allow, v.Path, filepath.Join(root, "+x"))
	}
}

func TestParseInputAcrossHarnesses(t *testing.T) {
	claude := ParseInput([]byte(`{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/r","tool_name":"Edit","tool_input":{"file_path":"/r/a.go"}}`))
	if claude.Session != "s1" || claude.Path != "/r/a.go" || !claude.IsEdit() || claude.IsShell() {
		t.Errorf("claude edit: %+v", claude)
	}
	bash := ParseInput([]byte(`{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	if !bash.IsShell() || bash.Command != "ls" {
		t.Errorf("claude bash: %+v", bash)
	}
	grok := ParseInput([]byte(`{"hookEventName":"PreToolUse","sessionId":"g1","toolName":"search_replace","toolInput":{"filePath":"a.go"}}`))
	if grok.Session != "g1" || grok.Path != "a.go" || !grok.IsEdit() {
		t.Errorf("grok edit: %+v", grok)
	}
	grokShell := ParseInput([]byte(`{"toolName":"run_terminal_command","toolInput":{"command":"rm x"}}`))
	if !grokShell.IsShell() {
		t.Errorf("grok shell: %+v", grokShell)
	}
	if !reflect.DeepEqual(ParseInput([]byte(`not json`)), Input{}) {
		t.Error("unreadable input is not the zero Input")
	}
}

// cursorFixture reads a Cursor hook payload captured by the sto_e51c7fb9
// probe (cursor-agent 2026.11.05; user_email and transcript_path removed, the
// scratch repo's path replaced by /work/repo).
func cursorFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cursor-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseInputCursorFixtures(t *testing.T) {
	start := ParseInput(cursorFixture(t, "sessionstart"))
	if start.Session == "" || !start.Cursor || start.Event != "sessionStart" || !slices.Equal(start.Roots, []string{"/work/repo"}) {
		t.Fatalf("sessionStart: %+v", start)
	}
	write := ParseInput(cursorFixture(t, "pretooluse-write"))
	if !write.IsEdit() || write.IsShell() || write.Path != "/work/repo/ok.txt" || write.Session != start.Session || !write.Cursor {
		t.Errorf("Write: %+v (session start %q)", write, start.Session)
	}
	del := ParseInput(cursorFixture(t, "pretooluse-delete"))
	if !del.IsEdit() || del.Path != "/work/repo/deny-me-del.txt" || del.Session != start.Session {
		t.Errorf("Delete: %+v", del)
	}
	sh := ParseInput(cursorFixture(t, "pretooluse-shell"))
	if !sh.IsShell() || sh.IsEdit() || sh.Command != "echo deny-me" || sh.Cwd != "" || sh.Session != start.Session {
		t.Errorf("Shell: %+v", sh)
	}
	compat := ParseInput(cursorFixture(t, "claudecompat-pretooluse"))
	if !compat.Cursor || !compat.IsEdit() {
		t.Errorf("Cursor's run of a .claude/settings.json hook does not mark Cursor: %+v", compat)
	}
	conv := ParseInput([]byte(`{"conversation_id":"c1","cursor_version":"x"}`))
	if conv.Session != "c1" {
		t.Errorf("conversation_id is not the session fallback: %+v", conv)
	}
	if ParseInput([]byte(`{"session_id":"s1","tool_name":"Edit","tool_input":{"file_path":"/a"}}`)).Cursor {
		t.Error("a claude payload reads as Cursor's")
	}
}

func TestDenyEnvelopes(t *testing.T) {
	if d := string(Deny(Claude, "no")); !strings.Contains(d, `"permissionDecision":"deny"`) || !strings.Contains(d, `"permissionDecisionReason":"no"`) {
		t.Errorf("claude deny: %s", d)
	}
	if d := string(Deny(Pi, "no")); !strings.Contains(d, `"permissionDecision":"deny"`) {
		t.Errorf("pi deny: %s", d)
	}
	if d := string(Deny(Grok, "no")); d != `{"decision":"deny","reason":"no"}` {
		t.Errorf("grok deny: %s", d)
	}
	if c := string(Context(Claude, EventSessionStart, "hi")); !strings.Contains(c, `"additionalContext":"hi"`) {
		t.Errorf("context: %s", c)
	}
	if d := string(Deny(Cursor, "no")); d != `{"permission":"deny","user_message":"no","agent_message":"no"}` {
		t.Errorf("cursor deny: %s", d)
	}
	if a := string(Allow(Cursor)); a != `{"permission":"allow"}` {
		t.Errorf("cursor allow: %s", a)
	}
	for _, h := range []string{Claude, Pi, Grok} {
		if Allow(h) != nil || StopAnswer(h) != nil {
			t.Errorf("%s has an allow or stop answer", h)
		}
	}
	if c := string(Context(Cursor, EventSessionStart, "hi")); c != `{"additional_context":"hi"}` {
		t.Errorf("cursor sessionStart context: %s", c)
	}
	if c := string(Context(Cursor, EventPrompt, "hi")); c != `{"continue":true}` {
		t.Errorf("cursor beforeSubmitPrompt answer: %s", c)
	}
	if s := string(StopAnswer(Cursor)); s != `{}` {
		t.Errorf("cursor stop answer: %s", s)
	}
}

// agents install cursor writes flat entries into .cursor/hooks.json beside the
// user's own, and remove strips only satelle's (sto_e51c7fb9 AC2).
func TestInstallCursorKeepsUserEntriesAndRemoveStripsOurs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := `{"version":1,"hooks":{"sessionStart":[{"command":"./mine.sh"}],"afterFileEdit":[{"command":"fmt.sh"}]}}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if _, err := Install(root, Cursor); err != nil {
			t.Fatal(err)
		}
	}
	var doc struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command string `json:"command"`
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("installed file does not decode: %v\n%s", err, b)
	}
	if doc.Version != 1 {
		t.Errorf("version %d", doc.Version)
	}
	for event, verb := range map[string]string{"preToolUse": VerbGate, "sessionStart": VerbContext, "beforeSubmitPrompt": VerbPrompt, "stop": VerbStopcheck} {
		n := 0
		for _, e := range doc.Hooks[event] {
			if e.Command == Command(verb, Cursor) {
				n++
				if verb == VerbGate && e.Matcher != "Write|Delete|Shell" {
					t.Errorf("preToolUse matcher %q", e.Matcher)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %d satelle entries, want 1\n%s", event, n, b)
		}
	}
	if !strings.Contains(string(b), "./mine.sh") || !strings.Contains(string(b), "fmt.sh") {
		t.Fatalf("user entries lost:\n%s", b)
	}
	if !Installed(root, Cursor) || Installed(root, Claude) {
		t.Error("Installed misreports")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !os.IsNotExist(err) {
		t.Error("installing cursor touched .claude")
	}
	if _, err := Remove(root, Cursor); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	var got, want any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(user), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after remove:\n%s\nwant %s", b, user)
	}
	if Installed(root, Cursor) {
		t.Error("Installed after remove")
	}

	fresh := t.TempDir()
	if _, err := Remove(fresh, Cursor); err != nil {
		t.Errorf("remove with no file: %v", err)
	}
	if _, err := Install(fresh, Cursor); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(fresh, ".cursor", "hooks.json"))
	if !strings.Contains(string(b), `"version": 1`) {
		t.Errorf("a new file lacks version 1:\n%s", b)
	}
	if _, err := Install(fresh, Claude); err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(filepath.Join(fresh, ".cursor", "hooks.json")); string(c) != string(b) {
		t.Error("installing claude touched .cursor/hooks.json")
	}
}

// Defers holds only for Cursor's run of another harness's entries, and only
// once Cursor's own are installed.
func TestDefersToCursorInstall(t *testing.T) {
	root := t.TempDir()
	fromCursor := Input{Cursor: true}
	if Defers(root, Claude, fromCursor) {
		t.Error("defers with no cursor install")
	}
	if _, err := Install(root, Cursor); err != nil {
		t.Fatal(err)
	}
	if !Defers(root, Claude, fromCursor) {
		t.Error("Cursor's call into the claude entries does not defer")
	}
	if Defers(root, Cursor, fromCursor) || Defers(root, Claude, Input{}) {
		t.Error("cursor's own call, or claude's, defers")
	}
}

func TestInstallKeepsUserHooksAndRemoveStripsOurs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := `{"model":"x","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"mine.sh"}]}]}}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if _, err := Install(root, Claude); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Count(s, Command(VerbGate, Claude)) != 1 || !strings.Contains(s, "mine.sh") || !strings.Contains(s, `"model"`) {
		t.Fatalf("installed settings:\n%s", s)
	}
	for _, verb := range []string{VerbContext, VerbPrompt, VerbStopcheck} {
		if !strings.Contains(s, Command(verb, Claude)) {
			t.Errorf("missing %s hook", verb)
		}
	}
	if _, err := Remove(root, Claude); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "satelle hook") || !strings.Contains(string(b), "mine.sh") {
		t.Fatalf("after remove:\n%s", b)
	}

	if _, err := Install(root, Pi); err != nil {
		t.Fatal(err)
	}
	pi, _ := os.ReadFile(filepath.Join(root, ".pi", "extensions", "satelle.ts"))
	if !strings.Contains(string(pi), piOwnedMarker) || strings.Contains(string(pi), "__SATELLE_") {
		t.Fatalf("pi extension not rendered:\n%s", pi)
	}
	if _, err := Remove(root, Pi); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".pi", "extensions", "satelle.ts")); !os.IsNotExist(err) {
		t.Error("pi extension survived remove")
	}
	if _, err := Install(root, Grok); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(root, Grok); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".grok", "hooks", "satelle.json")); !os.IsNotExist(err) {
		t.Error("grok hook file left with nothing in it")
	}
}

// The gate refuses the operator record verb in any agent tool call, before
// paths and seats: a seat that allows every edit does not allow it. Prose that
// names the verb only as data is allowed.
func TestGateRefusesOperatorRecord(t *testing.T) {
	root := newRepo(t)
	g := Gate{Root: root, Seated: func(string) bool { return true }}
	for _, c := range append(slices.Clone(operatorRecordCases), operatorRecordCodeCases...) {
		if v := g.Bash(c, root); v.Allow || v.Reason != OperatorRecordDenied {
			t.Errorf("%q: %+v", c, v)
		}
	}
	for _, c := range append(slices.Clone(operatorRecordDataCases), notOperatorRecordCases...) {
		if v := g.Bash(c, root); !v.Allow {
			t.Errorf("%q denied: %s", c, v.Reason)
		}
	}
}
