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
		Grant:     func(tree string) Grant { return Grant{Tree: seats[tree]} },
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

// TestEditGateAttachmentsOnly pins that a seat on a state whose performer
// produces only attachments refuses every path of its tree, exempt and locked
// paths included and whatever other seat the session holds there, naming the
// state; a temp-dir write still passes.
func TestEditGateAttachmentsOnly(t *testing.T) {
	root := newRepo(t)
	temp, _ := filepath.EvalSymlinks(t.TempDir())
	var grant Grant
	g := Gate{
		Root:      root,
		Exempt:    []string{"docs/**", "*.md"},
		Locked:    []string{"secrets/**"},
		TempRoots: []string{temp},
		Grant:     func(string) Grant { return grant },
	}
	plan := Grant{AttachOnly: true, Item: "sto_1", State: "st-plan"}
	both := plan
	both.Tree = true
	unread := plan
	unread.Unread = "flow.toml: syntax error"
	type tc struct {
		name   string
		grant  Grant
		edit   string
		bash   string
		allow  bool
		want   []string
		reject []string
	}
	attach := []string{"st-plan", "sto_1", "produces an attachment", "`satelle <kind> attach <id> --name <name> --file <file> --session <session>`", "`satelle help edit-gate --session <session>`"}
	for _, c := range []tc{
		{name: "edit refused", grant: plan, edit: "main.go", want: attach},
		{name: "shell redirect refused", grant: plan, bash: "echo hi > out.txt", want: attach},
		{name: "exempt path refused", grant: plan, edit: "docs/a/b.txt", want: attach},
		{name: "exempt basename refused", grant: plan, edit: "sub/notes.md", want: attach},
		{name: "exempt shell redirect refused", grant: plan, bash: "echo hi >> docs/log.txt", want: attach},
		{name: "locked path refused as attachments-only", grant: plan, edit: "secrets/key", want: attach, reject: []string{"is a locked path"}},
		{name: "inline interpreter refused", grant: plan, bash: `python3 -c 'print(1)'`, want: []string{"st-plan"}},
		{name: "temp dir allowed", grant: plan, edit: filepath.Join(temp, "plan.md"), allow: true},
		{name: "shell write to temp allowed", grant: plan, bash: "cat > " + filepath.Join(temp, "plan.md"), allow: true},
		{name: "attach command allowed", grant: plan, bash: "satelle story attach sto_1 --name plan --file - --session s-1 < " + filepath.Join(temp, "plan.md"), allow: true},
		{name: "two seats at one tree still refused", grant: both, edit: "main.go", want: attach},
		{name: "unread scope refused", grant: unread, edit: "main.go", want: []string{"st-plan", "cannot be read", "syntax error", "produces an attachment"}},
		{name: "unread seats refused", grant: Grant{AttachOnly: true, Unread: "ledger locked"}, edit: ".satelle/satelle.toml", want: []string{"cannot be read", "ledger locked", "produces an attachment"}},
		{name: "tree seat allows", grant: Grant{Tree: true}, edit: "main.go", allow: true},
		{name: "tree seat locked reason", grant: Grant{Tree: true}, edit: "secrets/key", want: []string{"is a locked path"}},
		{name: "no seat exempt allowed", edit: "docs/a/b.txt", allow: true},
		{name: "no seat edit refused", edit: "main.go", want: []string{"performer may change the tree", "`satelle story seat <id> --session <session>`"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			grant = c.grant
			var v Verdict
			if c.bash != "" {
				v = g.Bash(c.bash, root)
			} else {
				v = g.Edit(c.edit)
			}
			if v.Allow != c.allow {
				t.Fatalf("allow = %v, want %v (%s)", v.Allow, c.allow, v.Reason)
			}
			if v.Allow {
				return
			}
			if v.AttachOnly != c.grant.AttachOnly {
				t.Errorf("AttachOnly = %v, want %v", v.AttachOnly, c.grant.AttachOnly)
			}
			for _, w := range c.want {
				if !strings.Contains(v.Reason, w) {
					t.Errorf("reason does not contain %q: %s", w, v.Reason)
				}
			}
			for _, w := range c.reject {
				if strings.Contains(v.Reason, w) {
					t.Errorf("reason contains %q: %s", w, v.Reason)
				}
			}
		})
	}
}

// TestGateChmodModeOperand pins that chmod's mode is not judged as an edit:
// from an unseated repo cwd a scratch-file chmod is allowed, and an in-repo
// chmod is refused naming the file, never the mode.
func TestGateChmodModeOperand(t *testing.T) {
	root := newRepo(t)
	g := Gate{Root: root, TempRoots: []string{"/tmp"}, Grant: func(string) Grant { return Grant{} }}
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
	if c := string(Context(Claude, EventSessionStart, "hi", "warn")); !strings.Contains(c, `"additionalContext":"hi"`) || !strings.Contains(c, `"systemMessage":"warn"`) {
		t.Errorf("context: %s", c)
	}
	if c := string(Context(Claude, EventSessionStart, "hi", "")); strings.Contains(c, "systemMessage") {
		t.Errorf("claude context with no notice carries a systemMessage: %s", c)
	}
	if c := string(Context(Pi, EventSessionStart, "hi", "warn")); !strings.Contains(c, `"systemMessage":"warn"`) {
		t.Errorf("pi context: %s", c)
	}
	if c := string(Context(Grok, EventSessionStart, "hi", "warn")); c != `{"systemMessage":"hi"}` {
		t.Errorf("grok session-start context: %s", c)
	}
	if c := Context(Grok, EventPrompt, "hi", ""); c != nil {
		t.Errorf("grok prompt context is not nil: %s", c)
	}
	if d := string(Deny(Cursor, "no")); d != `{"permission":"deny","user_message":"no","agent_message":"no"}` {
		t.Errorf("cursor deny: %s", d)
	}
	for _, h := range []string{Claude, Pi, Grok} {
		if StopAnswer(h) != nil {
			t.Errorf("%s has a stop answer", h)
		}
	}
	if c := string(Context(Cursor, EventSessionStart, "hi", "warn")); c != `{"additional_context":"hi"}` {
		t.Errorf("cursor sessionStart context: %s", c)
	}
	if c := string(Context(Cursor, EventPrompt, "hi", "")); c != `{"continue":true}` {
		t.Errorf("cursor beforeSubmitPrompt answer: %s", c)
	}
	if s := string(StopAnswer(Cursor)); s != `{}` {
		t.Errorf("cursor stop answer: %s", s)
	}
}

func TestStopBlockEnvelopes(t *testing.T) {
	for _, h := range []string{Claude, Pi, Grok} {
		if b := string(StopBlock(h, "r")); b != `{"decision":"block","reason":"r"}` {
			t.Errorf("%s stop block: %s", h, b)
		}
	}
	if b := string(StopBlock(Cursor, "r")); b != `{"followup_message":"r"}` {
		t.Errorf("cursor stop block: %s", b)
	}
}

func TestParseInputGrokFixtures(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	start := ParseInput(read("grok_session_start.json"))
	if start.Session != "grok-session-0001" || start.Event != "SessionStart" || start.Cwd != "/work/repo" {
		t.Errorf("grok SessionStart: %+v", start)
	}
	stop := ParseInput(read("grok_stop.json"))
	if stop.Session != "grok-session-0001" || stop.Event != "Stop" || stop.StopReason != TurnEndReason || !stop.TurnEnd() {
		t.Errorf("grok Stop: %+v", stop)
	}
	for reason, want := range map[string]bool{"end_turn": true, "": true, "channel_closed": false, "shutdown": false} {
		in := ParseInput([]byte(`{"hook_event_name":"Stop","sessionId":"s","reason":"` + reason + `"}`))
		if in.TurnEnd() != want {
			t.Errorf("reason %q: TurnEnd %v, want %v", reason, in.TurnEnd(), want)
		}
	}
	if !ParseInput([]byte(`{"hook_event_name":"Stop","session_id":"s"}`)).TurnEnd() {
		t.Error("a Stop with no reason does not end a turn")
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
		if _, _, err := Install(root, Cursor, nil); err != nil {
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
	if _, _, err := Install(fresh, Cursor, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(fresh, ".cursor", "hooks.json"))
	if !strings.Contains(string(b), `"version": 1`) {
		t.Errorf("a new file lacks version 1:\n%s", b)
	}
	if _, _, err := Install(fresh, Claude, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(filepath.Join(fresh, ".cursor", "hooks.json")); string(c) != string(b) {
		t.Error("installing claude touched .cursor/hooks.json")
	}
}

// Defers holds only for Cursor's run of another harness's entries, and only
// once Cursor's own are installed; the root is resolved only for that call.
func TestDefersToCursorInstall(t *testing.T) {
	root := t.TempDir()
	at := func() (string, bool) { return root, true }
	fromCursor := Input{Cursor: true}
	if Defers(Claude, fromCursor, at) {
		t.Error("defers with no cursor install")
	}
	if _, _, err := Install(root, Cursor, nil); err != nil {
		t.Fatal(err)
	}
	if !Defers(Claude, fromCursor, at) {
		t.Error("Cursor's call into the claude entries does not defer")
	}
	never := func() (string, bool) {
		t.Error("root resolved for a call that cannot defer")
		return root, true
	}
	if Defers(Cursor, fromCursor, never) || Defers(Claude, Input{}, never) {
		t.Error("cursor's own call, or claude's, defers")
	}
	if Defers(Claude, fromCursor, func() (string, bool) { return root, false }) {
		t.Error("defers when the root is not found")
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
		if _, _, err := Install(root, Claude, nil); err != nil {
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

	if _, _, err := Install(root, Pi, nil); err != nil {
		t.Fatal(err)
	}
	pi, _ := os.ReadFile(filepath.Join(root, ".pi", "extensions", "satelle.ts"))
	if !strings.Contains(string(pi), piOwnedMarker) || strings.Contains(string(pi), "__SATELLE_") {
		t.Fatalf("pi extension not rendered:\n%s", pi)
	}
	if i, j := strings.Index(string(pi), `pi.on("session_start"`), strings.Index(string(pi), `pi.on("before_agent_start"`); i < 0 || j < i {
		t.Errorf("pi extension handlers not found in order:\n%s", pi)
	} else if h := string(pi)[i:j]; !strings.Contains(h, "noticeOf(") || !strings.Contains(h, "ui?.notify?.(") {
		t.Errorf("pi session_start does not notify the user of the hook's systemMessage:\n%s", h)
	}
	if _, err := Remove(root, Pi); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".pi", "extensions", "satelle.ts")); !os.IsNotExist(err) {
		t.Error("pi extension survived remove")
	}
	if _, _, err := Install(root, Grok, nil); err != nil {
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
	g := Gate{Root: root, Grant: func(string) Grant { return Grant{Tree: true} }}
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
