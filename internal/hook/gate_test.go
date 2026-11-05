package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo is a git repo with one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestEditGateTable(t *testing.T) {
	root := newRepo(t)
	foreign := newRepo(t)
	linked := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", linked)
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
	if (ParseInput([]byte(`not json`)) != Input{}) {
		t.Error("unreadable input is not the zero Input")
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
	if c := string(Context("SessionStart", "hi")); !strings.Contains(c, `"additionalContext":"hi"`) {
		t.Errorf("context: %s", c)
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
