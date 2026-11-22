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
command = "/opt/bin/claude --foo"
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
	os.WriteFile(p, []byte("[agents.x]\ncommand = \"claude\"\ndenies = [\"Write\"]\n"), 0o644)
	if _, err := LoadCatalog(p); err == nil || !strings.Contains(err.Error(), "agents.x.denies") {
		t.Fatalf("misspelt deny: %v", err)
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
	shell := Catalog{"cs": {Name: "cs", Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}}}
	if errs := shell.Validate(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "agents.cs") || !strings.Contains(errs[0].Error(), "grant no Bash") {
		t.Fatalf("a Bash-granted cursor-agent binding: %v", errs)
	}
	piShell := Catalog{"pc": {Name: "pc", Command: "pi", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}}}
	if errs := piShell.Validate(); len(errs) != 0 {
		t.Fatalf("a Bash-granted pi binding: %v", errs)
	}
	if a, err := AdapterFor(Binding{Command: "/opt/x/cursor-agent"}); err != nil || a.Name() != "cursor-agent" {
		t.Fatalf("AdapterFor(/opt/x/cursor-agent) = %v, %v", a, err)
	}
	agyJudge := Catalog{"a": {Name: "a", Command: "agy", Model: "gemini-4-hydrogen", Effort: "high", Tools: []string{"Read", "Grep", "Glob"}}}
	if errs := agyJudge.Validate(); len(errs) != 0 {
		t.Fatalf("a read-only agy binding: %v", errs)
	}
	if err := CheckJudge(agyJudge["a"]); err != nil {
		t.Fatalf("a read-only agy binding may judge: %v", err)
	}
	if err := CheckJudge(Binding{Name: "e", Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write"}}); err == nil {
		t.Fatal("an Edit-granted agy binding may judge")
	}
	agyShell := Catalog{"as": {Name: "as", Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}}}
	if errs := agyShell.Validate(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "agents.as") || !strings.Contains(errs[0].Error(), "grant no Bash") {
		t.Fatalf("a Bash-granted agy binding: %v", errs)
	}
	if a, err := AdapterFor(Binding{Command: "/opt/x/agy"}); err != nil || a.Name() != "agy" {
		t.Fatalf("AdapterFor(/opt/x/agy) = %v, %v", a, err)
	}
}

func argvOf(t *testing.T, req Request) ([]string, error) {
	t.Helper()
	a, err := AdapterFor(req.Binding)
	if err != nil {
		t.Fatal(err)
	}
	return a.Argv(req)
}

func withCommand(req Request, command string) Request {
	req.Binding.Command = command
	return req
}

func TestArgvExpressesGrantsAndDenies(t *testing.T) {
	performer := Request{Binding: Binding{Name: "p", Command: "claude --x", Model: "m", Tools: []string{"Read", "Bash(go test:*)"}, Deny: []string{"Write"}}, Deny: PerformerDenies, System: "sys"}
	argv, err := argvOf(t, performer)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	for _, want := range []string{"claude --x -p --output-format json", "--model m", "--tools Read,Bash", "--allowedTools Read,Bash(go test:*)", "--disallowedTools Write,Bash(satelle story set:*)", "--append-system-prompt sys"} {
		if !strings.Contains(got, want) {
			t.Fatalf("claude argv lacks %q: %s", want, got)
		}
	}
	if _, err := argvOf(t, withCommand(performer, "grok")); err == nil {
		t.Fatal("grok expressed a scoped deny")
	}
	if _, err := argvOf(t, withCommand(performer, "pi")); err == nil {
		t.Fatal("pi expressed a scoped deny")
	}
	editor := Request{Binding: Binding{Command: "pi", Tools: []string{"Read", "Edit", "Glob"}, Deny: []string{"Edit"}}, Deny: PerformerDenies}
	argv, err = argvOf(t, editor)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--tools read,find,ls") || !strings.Contains(got, "--no-session") {
		t.Fatalf("pi argv %s", got)
	}
	argv, err = argvOf(t, Request{Binding: Binding{Command: "cursor-agent", Model: "m", Tools: []string{"Read", "Grep", "Glob"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "cursor-agent -p --output-format stream-json --trust --model m --mode ask") || strings.Contains(got, "--force") {
		t.Fatalf("cursor judge argv %s", got)
	}
	writer := []string{"Read", "Grep", "Glob", "Edit", "Write"}
	argv, err = argvOf(t, Request{Binding: Binding{Command: "cursor-agent", Tools: writer}, Deny: PerformerDenies})
	if err != nil || !slices.Contains(argv, "--force") || slices.Contains(argv, "--mode") {
		t.Fatalf("cursor performer argv %v %v", argv, err)
	}
	for name, tools := range map[string][]string{
		"Bash with Edit":    {"Read", "Grep", "Glob", "Edit", "Write", "Bash"},
		"Bash without Edit": {"Read", "Grep", "Glob", "Bash"},
		"scoped Bash":       {"Read", "Grep", "Glob", "Edit", "Write", "Bash(go test:*)"},
	} {
		if _, err := argvOf(t, Request{Binding: Binding{Command: "cursor-agent", Tools: tools}, Deny: PerformerDenies}); err == nil || !strings.Contains(err.Error(), "grant no Bash") {
			t.Errorf("cursor %s: %v", name, err)
		}
	}
	for name, b := range map[string]Binding{
		"only Edit":    {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Edit"}},
		"missing Glob": {Command: "cursor-agent", Tools: []string{"Read", "Grep"}},
		"NotebookEdit": {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "NotebookEdit"}},
		"effort":       {Command: "cursor-agent", Tools: []string{"Read", "Grep", "Glob"}, Effort: "high"},
	} {
		if _, err := argvOf(t, Request{Binding: b}); err == nil {
			t.Errorf("cursor expressed %s", name)
		}
	}
	argv, err = argvOf(t, Request{Binding: Binding{Command: "agy", Model: "gemini-4-hydrogen", Effort: "high", Tools: []string{"Read", "Grep", "Glob"}}, Prompt: "do", System: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); got != "agy --input-format stream-json --output-format stream-json --disable-slash-commands --settings <settings file> --add-dir <run dir> --add-dir <guard dir> --model gemini-4-hydrogen --effort high" {
		t.Fatalf("agy judge argv %s", got)
	}
	if _, err := argvOf(t, Request{Binding: Binding{Command: "agy", Tools: writer}, Deny: PerformerDenies}); err != nil {
		t.Fatalf("agy performer: %v", err)
	}
	for name, b := range map[string]Binding{
		"Bash":              {Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}},
		"Bash without Edit": {Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Bash"}},
		"scoped Bash":       {Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash(go test:*)"}},
		"NotebookEdit":      {Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write", "NotebookEdit"}},
		"only Edit":         {Command: "agy", Tools: []string{"Read", "Grep", "Glob", "Edit"}},
		"missing Glob":      {Command: "agy", Tools: []string{"Read", "Grep"}},
		"unknown effort":    {Command: "agy", Tools: []string{"Read", "Grep", "Glob"}, Effort: "extreme"},
		"effort twice":      {Command: "agy", Tools: []string{"Read", "Grep", "Glob"}, Model: "gemini-4-hydrogen-high", Effort: "low"},
	} {
		_, err := argvOf(t, Request{Binding: b, Deny: PerformerDenies})
		if err == nil || (strings.HasPrefix(name, "Bash") || name == "scoped Bash") && !strings.Contains(err.Error(), "grant no Bash") {
			t.Errorf("agy %s: %v", name, err)
		}
	}
	argv, err = argvOf(t, Request{Binding: Binding{Command: "grok", Tools: []string{"Read", "Grep"}, Deny: []string{"Write"}}, Prompt: "do"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--prompt-file <prompt file>") || strings.Contains(got, " do ") || !strings.Contains(got, "--tools read_file,grep") || !strings.Contains(got, "--deny write_file") {
		t.Fatalf("grok argv %s", got)
	}
}

func TestNoAdapterPutsThePromptInArgv(t *testing.T) {
	const prompt = "UNIQUE-PROMPT-TEXT"
	for _, cmd := range []string{"claude", "pi", "grok", "cursor-agent", "agy"} {
		argv, err := argvOf(t, Request{Binding: Binding{Command: cmd, Tools: []string{"Read", "Grep", "Glob"}}, Prompt: prompt, System: "skill"})
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if strings.Contains(strings.Join(argv, "\x00"), prompt) {
			t.Errorf("%s puts the prompt in argv: %q", cmd, argv)
		}
	}
}

func TestCatalogDecodesBrief(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agents.toml")
	if err := os.WriteFile(p, []byte(`
[agents.single]
command = "claude"
model = "m1"
tools = ["Read", "Grep", "Glob"]
brief = "Read only the diff and files it touches."
[agents.multi]
command = "cursor-agent"
model = "m2"
tools = ["Read", "Grep", "Glob"]
brief = """
Read the changed files first.
Do not explore unrelated packages.
"""
[agents.plain]
command = "claude"
model = "m3"
tools = ["Read", "Grep", "Glob"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Validate(); len(errs) > 0 {
		t.Fatalf("Validate: %v", errs)
	}
	for _, name := range []string{"single", "multi", "plain"} {
		if err := CheckJudge(c[name]); err != nil {
			t.Fatalf("CheckJudge(%s): %v", name, err)
		}
	}
	if got := c["single"].Brief; got != "Read only the diff and files it touches." {
		t.Errorf("single brief = %q", got)
	}
	if got := c["multi"].Brief; got != "Read the changed files first.\nDo not explore unrelated packages.\n" {
		t.Errorf("multi brief = %q", got)
	}
	if got := c["plain"].Brief; got != "" {
		t.Errorf("plain brief = %q, want empty", got)
	}
}
