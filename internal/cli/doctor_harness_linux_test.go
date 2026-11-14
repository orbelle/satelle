//go:build linux

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
)

// harnessFindings are doctor's harness issues and harness notes.
func harnessFindings(t *testing.T) (issues []doctorIssue, notes []string) {
	t.Helper()
	rep, _, _ := doctorJSON(t)
	for _, is := range rep.Issues {
		if is.Check == checkHarnessName {
			issues = append(issues, is)
		}
	}
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, checkHarnessName+":") {
			notes = append(notes, n)
		}
	}
	return issues, notes
}

// satelle doctor's harness check (sto_f58ed5fa AC1): the calling harness
// unwired is an issue with its fix, the other unwired harnesses are one note,
// a cursor, pi or grok marker decides over claude's inherited one, with the
// caller unknown no harness wired at all is the issue, and an installed hook
// file an ignore rule hides from git is a note naming the rule.
func TestDoctorHarnessCheck(t *testing.T) {
	t.Run("claude calling, nothing wired, then wired", func(t *testing.T) {
		isolate(t)
		t.Setenv("CLAUDECODE", "1")
		must(t, "init")
		issues, notes := harnessFindings(t)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, "claude: .claude/settings.json is absent") || issues[0].Fix != "satelle agents install claude" || issues[0].Resolve != "" {
			t.Fatalf("unwired claude caller: issues %+v", issues)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "not wired: pi, grok, cursor") {
			t.Fatalf("unwired others: notes %q", notes)
		}
		must(t, "agents", "install", "claude")
		if issues, _ := harnessFindings(t); len(issues) != 0 {
			t.Fatalf("after agents install claude: issues %+v", issues)
		}
	})
	t.Run("hook file present without satelle hooks", func(t *testing.T) {
		dir := isolate(t)
		t.Setenv("CLAUDECODE", "1")
		must(t, "init")
		writeFile(t, filepath.Join(dir, ".claude", "settings.json"), `{"permissions":{"deny":["Read(~/.ssh/**)"]}}`)
		issues, _ := harnessFindings(t)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, ".claude/settings.json holds no satelle hooks") {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("cursor inside a claude shell, only claude wired", func(t *testing.T) {
		isolate(t)
		t.Setenv("CLAUDECODE", "1")
		t.Setenv("CURSOR_AGENT", "1")
		must(t, "init", "--hooks=claude")
		issues, _ := harnessFindings(t)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, "cursor: .cursor/hooks.json is absent") || issues[0].Fix != "satelle agents install cursor" {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("caller unknown, nothing wired", func(t *testing.T) {
		isolate(t)
		must(t, "init")
		issues, notes := harnessFindings(t)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, "no harness is wired (claude, pi, grok, cursor)") || issues[0].Fix != "satelle agents install <harness>" {
			t.Fatalf("issues %+v", issues)
		}
		if len(notes) != 0 {
			t.Fatalf("notes %q, want none beside the one issue", notes)
		}
	})
	t.Run("caller unknown, claude wired", func(t *testing.T) {
		isolate(t)
		must(t, "init", "--hooks=claude")
		issues, notes := harnessFindings(t)
		if len(issues) != 0 {
			t.Fatalf("issues %+v", issues)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "not wired: pi, grok, cursor") {
			t.Fatalf("notes %q", notes)
		}
	})
	t.Run("installed but hidden by an ignore rule", func(t *testing.T) {
		dir := isolate(t)
		t.Setenv("CLAUDECODE", "1")
		must(t, "init", "--hooks=claude")
		writeFile(t, filepath.Join(dir, ".gitignore"), ".claude/\n")
		issues, notes := harnessFindings(t)
		if len(issues) != 0 {
			t.Fatalf("issues %+v", issues)
		}
		want := "harness: claude is installed, but .gitignore:1: .claude/ hides .claude/settings.json from git, so a clone is not wired"
		if len(notes) != 2 || notes[0] != want {
			t.Fatalf("notes %q, want first %q", notes, want)
		}
	})
	t.Run("the hook's --harness decides, not the environment", func(t *testing.T) {
		dir := isolate(t)
		t.Setenv("CLAUDECODE", "1")
		must(t, "init", "--hooks=claude")
		out, err := run(t, fmt.Sprintf(`{"session_id":"s-pi","cwd":%q}`, dir), "hook", "context", "--harness", "pi")
		if err != nil {
			t.Fatalf("hook context: %v\n%s", err, out)
		}
		if !strings.Contains(out, "pi: .pi/extensions/satelle.ts is absent") || !strings.Contains(out, "satelle agents install pi") {
			t.Fatalf("hook context --harness pi did not report pi:\n%s", out)
		}
	})
}

// doctor's help lists the harness check and says which git queries doctor
// runs; it no longer says doctor runs no git command (sto_f58ed5fa AC1).
func TestDoctorHelpNamesTheHarnessCheckAndItsGitQueries(t *testing.T) {
	long := doctorCmd().Long
	for _, want := range []string{"  harness    ", "git check-ignore", "read-only queries"} {
		if !strings.Contains(long, want) {
			t.Errorf("doctor help lacks %q", want)
		}
	}
	if strings.Contains(long, "runs no git command") {
		t.Error("doctor help still says it runs no git command")
	}
}

// satelle status shows each harness's wiring, between the configuration lines
// and the item counts, and status --json carries it (sto_f58ed5fa AC2).
func TestStatusShowsHarnessWiring(t *testing.T) {
	dir := isolate(t)
	must(t, "init", "--hooks=claude,grok")
	writeFile(t, filepath.Join(dir, ".gitignore"), ".grok/\n")

	text := must(t, "status")
	wf, hs, items := strings.Index(text, "\nworkflows "), strings.Index(text, "\nharness\n"), strings.Index(text, "\nitems\n")
	if wf < 0 || hs < wf || items < hs {
		t.Fatalf("status: the harness block is not between workflows and items:\n%s", text)
	}
	for _, want := range []string{
		"claude   installed                  .claude/settings.json",
		"pi       not installed              .pi/extensions/satelle.ts",
		"grok     installed but git-ignored  .grok/hooks/satelle.json (.gitignore:1: .grok/)",
		"cursor   not installed              .cursor/hooks.json",
	} {
		if !strings.Contains(text, "  "+want+"\n") {
			t.Errorf("status lacks the line %q:\n%s", want, text)
		}
	}

	var rep statusReport
	if err := json.Unmarshal([]byte(must(t, "status", "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Harnesses) != len(hook.Harnesses) {
		t.Fatalf("status --json harnesses: %+v", rep.Harnesses)
	}
	states := map[string]string{hook.Claude: hook.WiringInstalled, hook.Pi: hook.WiringNotInstalled, hook.Grok: hook.WiringIgnored, hook.Cursor: hook.WiringNotInstalled}
	for i, w := range rep.Harnesses {
		if w.Harness != hook.Harnesses[i] || w.State != states[w.Harness] {
			t.Errorf("harnesses[%d] = %+v, want %s %s", i, w, hook.Harnesses[i], states[hook.Harnesses[i]])
		}
		if (w.Rule != "") != (w.Harness == hook.Grok) {
			t.Errorf("%s: rule %q", w.Harness, w.Rule)
		}
	}
}

// satelle init ends by naming the harnesses it left unwired and the command
// that wires one, and names none that --hooks wired (sto_f58ed5fa AC3).
func TestInitNamesTheHarnessesItLeftUnwired(t *testing.T) {
	const cmdLine = "  satelle agents install <harness>   # gives that harness's sessions satelle's context and the edit gate; commit the file it writes\n"
	isolate(t)
	out := must(t, "init")
	if !strings.HasSuffix(out, "harness hooks not installed: claude, pi, grok, cursor\n"+cmdLine) {
		t.Fatalf("init:\n%s", out)
	}
	isolate(t)
	out = must(t, "init", "--hooks=claude")
	if !strings.HasSuffix(out, "harness hooks not installed: pi, grok, cursor\n"+cmdLine) {
		t.Fatalf("init --hooks=claude:\n%s", out)
	}
	isolate(t)
	out = must(t, "init", "--hooks=claude,pi,grok,cursor")
	if strings.Contains(out, "not installed") || strings.Contains(out, "agents install") {
		t.Fatalf("init with every harness wired still names one:\n%s", out)
	}
}

// The README init writes tells an agent with no hooks what to run, names only
// commands this binary has, and doctor reports it when missing (sto_f58ed5fa
// AC4).
func TestInitWritesTheAgentReadme(t *testing.T) {
	dir := isolate(t)
	must(t, "init", "--hooks=claude")
	p := filepath.Join(dir, config.DefaultDataDir, "README.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"`satelle doctor --session <session>`", "`satelle help --session <session>`", "`satelle agents install <harness> --session <session>`", "`satelle status --session <session>`"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".satelle/README.md lacks %s", want)
		}
	}
	for _, f := range substrate.CheckText(".satelle/README.md", "md", string(b), substrateTree(NewRootCmd()), nil) {
		t.Error(f.String())
	}
	if plan, _ := kernel.InitPlan(filepath.Dir(p)); len(plan) != 0 {
		t.Fatalf("after init the plan still lists %v", plan)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	rep, _, _ := doctorJSON(t)
	found := false
	for _, is := range rep.Issues {
		if is.Check == "init" && is.Problem == "satelle init would add .satelle/README.md" && is.Fix == "satelle init" {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor did not report the missing README: %+v", rep.Issues)
	}
}

// doctor reports each harness in use whose hook file differs from satelle's
// current set, with the install fix, and says when update leaves the file
// (git tracks it, or the repo opts out); a current file with the user's own
// handlers, wrapped satelle calls included, is no issue (sto_6e3d4755 AC5,
// AC6).
func TestDoctorReportsInUseHarnessesThatDiffer(t *testing.T) {
	harnessIssuesOf := func(t *testing.T, h string) []doctorIssue {
		t.Helper()
		issues, _ := harnessFindings(t)
		var out []doctorIssue
		for _, is := range issues {
			if strings.HasPrefix(is.Problem, h+": ") {
				out = append(out, is)
			}
		}
		return out
	}
	settings := func(dir string) string { return filepath.Join(dir, ".claude", "settings.json") }
	t.Run("a satelle command this binary lacks", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		staleHarness(t, dir, hook.Claude)
		issues := harnessIssuesOf(t, hook.Claude)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, "runs `satelle reindex`, which this binary lacks (reindex is not a command of satelle)") ||
			issues[0].Fix != "satelle agents install claude" || issues[0].Resolve != "" {
			t.Fatalf("issues %+v", issues)
		}
		if strings.Contains(issues[0].Problem, "git tracks") || strings.Contains(issues[0].Problem, "refresh = false") {
			t.Errorf("an untracked, refreshing repo's issue says update leaves it: %s", issues[0].Problem)
		}
	})
	t.Run("agents.toml runs grok, no .grok", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		putFile(t, filepath.Join(dir, ".satelle", kernel.AgentsFile), "[agents.g]\ncommand = \"grok\"\n")
		issues := harnessIssuesOf(t, hook.Grok)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, ".grok/hooks/satelle.json is absent") || issues[0].Fix != "satelle agents install grok" {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run(".cursor with no satelle hooks", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		putFile(t, filepath.Join(dir, ".cursor", "hooks.json"), `{"version":1,"hooks":{"stop":[{"command":"echo mine"}]}}`)
		issues := harnessIssuesOf(t, hook.Cursor)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, ".cursor/hooks.json holds no satelle hooks") || issues[0].Fix != "satelle agents install cursor" {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("an older set", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init", "--hooks=claude")
		b := readString(t, settings(dir))
		putFile(t, settings(dir), strings.Replace(b, `"Edit|Write|MultiEdit|NotebookEdit|Bash"`, `"Edit|Write"`, 1))
		issues := harnessIssuesOf(t, hook.Claude)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, ".claude/settings.json's satelle hooks are not this binary's current set") {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("tracked", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		staleHarness(t, dir, hook.Claude)
		gittest.Git(t, dir, "add", ".claude")
		gittest.Git(t, dir, "commit", "-qm", "hooks")
		issues := harnessIssuesOf(t, hook.Claude)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, "git tracks this file") || !strings.Contains(issues[0].Problem, "commit the change in this repo") ||
			issues[0].Fix != "satelle agents install claude" {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("opted out", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		p := filepath.Join(dir, ".satelle", kernel.SettingsFile)
		putFile(t, p, readString(t, p)+"\n[hook]\nrefresh = false\n")
		staleHarness(t, dir, hook.Claude)
		issues := harnessIssuesOf(t, hook.Claude)
		if len(issues) != 1 || !strings.Contains(issues[0].Problem, "[hook] refresh = false, so satelle update leaves it") {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("current, with the user's handlers", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		putFile(t, settings(dir), `{"hooks":{"Stop":[{"hooks":[`+
			`{"type":"command","command":"satelle doctor"},{"type":"command","command":"sh -c \"satelle reindex\""},`+
			`{"type":"command","command":"echo satelle is great"},{"type":"command","command":"echo hi"}]}]}}`)
		must(t, "agents", "install", "claude")
		if issues := harnessIssuesOf(t, hook.Claude); len(issues) != 0 {
			t.Fatalf("issues %+v", issues)
		}
	})
	t.Run("not in use, not the caller", func(t *testing.T) {
		isolate(t)
		must(t, "init", "--hooks=claude")
		issues, notes := harnessFindings(t)
		if len(issues) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "not wired: pi, grok, cursor") {
			t.Fatalf("issues %+v, notes %q", issues, notes)
		}
	})
}
