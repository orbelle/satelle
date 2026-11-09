package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCatalogAndGrants(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agents.toml")
	os.WriteFile(p, []byte(`
[agents.r]
command = "claude"
model = "m1"
tools = ["Read", "Grep", "Glob"]
[agents.w]
command = "/opt/bin/pi --foo"
tools = ["Read", "Edit", "Bash(go test:*)"]
placement = "remote:n1"
[agents.me]
command = "session"
`), 0o644)
	c, err := LoadCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	if c["r"].Name != "r" || !c.Has("w") || strings.Join(c.Names(), ",") != "me,r,w" {
		t.Fatalf("catalog %+v", c)
	}
	if !IsReadOnly(c["r"].Tools) || IsReadOnly(c["w"].Tools) || !IsReadOnly(nil) {
		t.Fatal("IsReadOnly")
	}
	if err := CheckJudge(c["r"]); err != nil {
		t.Fatal(err)
	}
	if err := CheckJudge(c["w"]); err == nil {
		t.Fatal("writer may judge")
	}
	if err := CheckJudge(c["me"]); err == nil {
		t.Fatal("session may judge")
	}
	if !IsSession(c["me"]) || IsSession(c["r"]) {
		t.Fatal("IsSession")
	}

	os.WriteFile(p, []byte("[agents.x]\ncommand = \"claude\"\ncolour = 1\n"), 0o644)
	if _, err := LoadCatalog(p); err == nil || !strings.Contains(err.Error(), "agents.x.colour") {
		t.Fatalf("strict: %v", err)
	}
	bad := Catalog{"x": {Name: "x", Command: "aider", Tools: []string{"Delete"}, Placement: "moon"}}
	if errs := bad.Validate(); len(errs) != 3 {
		t.Fatalf("want 3 problems, got %v", errs)
	}
	good := Catalog{"c": {Name: "c", Command: "cursor-agent", Model: "m", Tools: []string{"Read", "Grep", "Glob"}}}
	if errs := good.Validate(); len(errs) != 0 {
		t.Fatalf("a read-only cursor-agent binding: %v", errs)
	}
	if err := CheckJudge(good["c"]); err != nil {
		t.Fatalf("a read-only cursor-agent binding may judge: %v", err)
	}
	if err := CheckJudge(Binding{Name: "e", Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write"}}); err == nil {
		t.Fatal("an Edit-granted cursor-agent binding may judge")
	}
	if a, err := AdapterFor(Binding{Command: "/opt/x/cursor-agent"}); err != nil || a.Name() != PresetCursor {
		t.Fatalf("AdapterFor(/opt/x/cursor-agent) = %v, %v", a, err)
	}
}

func TestArgvExpressesGrantsAndDenies(t *testing.T) {
	performer := Request{Binding: Binding{Name: "p", Command: "claude --x", Model: "m", Tools: []string{"Read", "Bash(go test:*)"}, Deny: []string{"Write"}}, Deny: PerformerDenies, System: "sys"}
	argv, err := claude{}.Argv(performer)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	for _, want := range []string{"claude --x -p --output-format json", "--model m", "--tools Read,Bash", "--allowedTools Read,Bash(go test:*)", "--disallowedTools Write,Bash(satelle story set:*)", "--append-system-prompt sys"} {
		if !strings.Contains(got, want) {
			t.Fatalf("claude argv lacks %q: %s", want, got)
		}
	}
	if _, err := (grok{}).Argv(performer); err == nil {
		t.Fatal("grok expressed a scoped deny")
	}
	if _, err := (pi{}).Argv(performer); err == nil {
		t.Fatal("pi expressed a scoped deny")
	}
	// Without a shell in the grant the scoped denies have nothing to deny.
	editor := Request{Binding: Binding{Command: "pi", Tools: []string{"Read", "Edit", "Glob"}, Deny: []string{"Edit"}}, Deny: PerformerDenies}
	argv, err = pi{}.Argv(editor)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--tools read,find,ls") || !strings.Contains(got, "--no-session") {
		t.Fatalf("pi argv %s", got)
	}
	// cursor-agent: a read-only judge runs ask mode; a performer runs force
	// with one text glob per denied command word.
	argv, err = cursor{}.Argv(Request{Binding: Binding{Command: "cursor-agent", Model: "m", Tools: []string{"Read", "Grep", "Glob"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "cursor-agent -p --output-format stream-json --trust --model m --mode ask") || strings.Contains(got, "--force") {
		t.Fatalf("cursor judge argv %s", got)
	}
	full := []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}
	argv, err = cursor{}.Argv(Request{Binding: Binding{Command: "cursor-agent", Tools: full}, Deny: PerformerDenies})
	if err != nil || !slices.Contains(argv, "--force") || slices.Contains(argv, "ask") {
		t.Fatalf("cursor performer argv %v %v", argv, err)
	}
	if rules, err := CursorDenies(full, PerformerDenies); err != nil || !slices.Equal(rules, []string{"Shell(**satelle**)"}) {
		t.Fatalf("CursorDenies(full) = %v %v", rules, err)
	}
	if rules, err := CursorDenies([]string{"Read", "Grep", "Glob", "Edit", "Write"}, PerformerDenies); err != nil || !slices.Equal(rules, []string{"Shell(**)"}) {
		t.Fatalf("CursorDenies(no Bash) = %v %v", rules, err)
	}
	for name, b := range map[string]Binding{
		"only Edit":         {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Edit"}},
		"Bash without Edit": {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Bash"}},
		"scoped Bash":       {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash(go test:*)"}},
		"missing Glob":      {Command: "cursor-agent", Tools: []string{"Read", "Grep"}},
		"glob in a deny":    {Command: "cursor-agent", Tools: full, Deny: []string{"Bash(rm*:*)"}},
		"effort":            {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob"}, Effort: "high"},
	} {
		if _, err := (cursor{}).Argv(Request{Binding: b}); err == nil {
			t.Errorf("cursor expressed %s", name)
		}
	}
	argv, err = grok{}.Argv(Request{Binding: Binding{Command: "grok", Tools: []string{"Read", "Grep"}, Deny: []string{"Write"}}, Prompt: "do"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--prompt-file <prompt file>") || strings.Contains(got, " do ") || !strings.Contains(got, "--tools read_file,grep") || !strings.Contains(got, "--deny write_file") {
		t.Fatalf("grok argv %s", got)
	}
}

func TestParseEnvelopes(t *testing.T) {
	res, err := parseClaude([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":22,"cache_creation_input_tokens":11000,"cache_read_input_tokens":2500,"output_tokens":300},"modelUsage":{"claude-x":{"outputTokens":300}}}`))
	if err != nil || res.Text != "ok" || res.TokensIn != 13522 || res.TokensOut != 300 || res.Model != "claude-x" {
		t.Fatalf("claude %+v %v", res, err)
	}
	if _, err := parseClaude([]byte(`{"subtype":"error_max_turns","is_error":true}`)); err == nil {
		t.Fatal("error envelope accepted")
	}
	res, err = parseGrok([]byte(`{"text":"hi","usage":{"input_tokens":6435,"cache_read_input_tokens":512,"output_tokens":24},"modelUsage":{"grok-b":{"outputTokens":24}}}`))
	if err != nil || res.Text != "hi" || res.TokensIn != 6947 || res.TokensOut != 24 || res.Model != "grok-b" {
		t.Fatalf("grok %+v %v", res, err)
	}
	// A real stream, captured from cursor-agent 2026.10.22 with the prompt on
	// stdin (testdata/cursor-stream.jsonl).
	stream, err := os.ReadFile("testdata/cursor-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	res, err = parseCursor(stream)
	if err != nil || res.Text != "OK" || res.TokensIn != 4+9854+10126 || res.TokensOut != 4 || res.Model != "Claude Opus 5.5 300K Medium" {
		t.Fatalf("cursor %+v %v", res, err)
	}
}

// No adapter carries the prompt in argv: claude reads it on stdin, pi over
// RPC, grok from a file. (One argv element is capped at 128 KiB on Linux.)
func TestNoAdapterPutsThePromptInArgv(t *testing.T) {
	const prompt = "UNIQUE-PROMPT-TEXT"
	for _, c := range []struct {
		name string
		a    interface {
			Argv(Request) ([]string, error)
		}
		cmd string
	}{{"claude", claude{}, "claude"}, {"pi", pi{}, "pi"}, {"grok", grok{}, "grok"}, {"cursor-agent", cursor{}, "cursor-agent"}} {
		argv, err := c.a.Argv(Request{Binding: Binding{Command: c.cmd, Tools: []string{"Read", "Grep", "Glob"}}, Prompt: prompt, System: "skill"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if strings.Contains(strings.Join(argv, "\x00"), prompt) {
			t.Errorf("%s puts the prompt in argv: %q", c.name, argv)
		}
	}
}
