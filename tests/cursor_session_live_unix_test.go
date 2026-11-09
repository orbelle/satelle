//go:build integration && !windows

package tests

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
)

// cursorStream is what a cursor-agent stream-json run reports: its session,
// final text, and each completed edit and shell call's rejection reason ("" and
// ok when the call succeeded).
type cursorStream struct {
	Session string
	Result  string
	Calls   []cursorCall
}

type cursorCall struct {
	Kind     string // editToolCall, deleteToolCall, shellToolCall
	Target   string // path or command
	Rejected bool
	Reason   string
}

func readCursorStream(t *testing.T, out string) cursorStream {
	t.Helper()
	var st cursorStream
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type     string                     `json:"type"`
			Subtype  string                     `json:"subtype"`
			Session  string                     `json:"session_id"`
			Result   string                     `json:"result"`
			ToolCall map[string]json.RawMessage `json:"tool_call"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "system" && ev.Subtype == "init":
			st.Session = ev.Session
		case ev.Type == "result":
			st.Result = ev.Result
		case ev.Type == "tool_call" && ev.Subtype == "completed":
			for kind, raw := range ev.ToolCall {
				var c struct {
					Args struct {
						Path    string `json:"path"`
						Command string `json:"command"`
					} `json:"args"`
					Result struct {
						Rejected *struct {
							Reason string `json:"reason"`
						} `json:"rejected"`
						Error *struct {
							Error string `json:"error"`
						} `json:"error"`
					} `json:"result"`
				}
				if json.Unmarshal(raw, &c) != nil {
					continue
				}
				call := cursorCall{Kind: kind, Target: c.Args.Path + c.Args.Command}
				switch {
				case c.Result.Rejected != nil:
					call.Rejected, call.Reason = true, c.Result.Rejected.Reason
				case c.Result.Error != nil:
					call.Rejected, call.Reason = true, c.Result.Error.Error
				}
				st.Calls = append(st.Calls, call)
			}
		}
	}
	return st
}

// A cursor-agent session in a repo with `satelle agents install cursor` runs
// under satelle's hooks (sto_e51c7fb9 AC3, AC4): its session-start context
// carries the session id and the injected principles; an unseated edit is
// refused before it is written with the claude gate's reason; the
// operator-only verb is refused by the gate; once the session is seated in a
// performing state, the resumed session's edit is written, while an edit and a
// shell write into another repository's working tree are refused with the
// claude gate's fence reason. cursor-agent runs in print mode without --force.
// Opt-in: SATELLE_LIVE_CURSOR=1 and cursor-agent on PATH, signed in;
// SATELLE_LIVE_CURSOR_MODEL picks the model.
func TestCursorSessionRunsUnderSatelleHooks(t *testing.T) {
	if os.Getenv("SATELLE_LIVE_CURSOR") != "1" {
		t.Skip("set SATELLE_LIVE_CURSOR=1 to run cursor-agent live")
	}
	agentBin, err := exec.LookPath("cursor-agent")
	if err != nil {
		t.Skip("cursor-agent is not on PATH")
	}
	model := os.Getenv("SATELLE_LIVE_CURSOR_MODEL")
	if model == "" {
		model = "gemini-3.8-flash"
	}
	realHome, _ := os.UserHomeDir()
	auth := filepath.Join(realHome, ".config", "cursor", "auth.json")
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		auth = filepath.Join(v, "cursor", "auth.json")
	}
	if _, err := os.Stat(auth); err != nil {
		t.Skip("cursor-agent is not signed in on this machine")
	}

	s := newSandbox(t)
	s.must("init")
	s.write(".satelle/agents.toml", "[agents.hands]\ncommand = \"session\"\n")
	s.write(".satelle/skills/work.md", "Work.\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.todo]
start = true
[states.doing]
performer = { agent = "hands", skill = "work" }
[states.finished]
terminal = true
[[transition]]
from = ["todo"]
to = "doing"
[[transition]]
from = ["doing"]
to = "finished"
`)
	s.write("principles.md", "principle-nonce-8c2f\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[hook]\ninject = [\"principles.md\"]\n")
	s.must("validate")
	s.must("agents", "install", "cursor")
	// Cursor runs hooks under the user's login PATH, so a bare `satelle` would
	// be the machine's installed one: point the installed commands at the
	// binary under test.
	hooksFile := filepath.Join(s.dir, ".cursor", "hooks.json")
	b, err := os.ReadFile(hooksFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksFile, []byte(strings.ReplaceAll(string(b), `"satelle hook `, `"`+satelleBin+` hook `)), 0o644); err != nil {
		t.Fatal(err)
	}
	id := s.create("story", "--title", "cursor live").ID

	// Another repository, outside the temp roots: the gate allows any path
	// outside the session's root under the temp dir, so a foreign repo there
	// would never reach the fence.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.MkdirTemp(cache, "satelle-live-fence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(foreign) })
	if foreign, err = filepath.EvalSymlinks(foreign); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, foreign, "init", "-q")
	gittest.Git(t, foreign, "commit", "-q", "--allow-empty", "-m", "init")
	fencedEdit := filepath.Join(foreign, "fenced.txt")
	fencedShell := filepath.Join(foreign, "fenced-sh.txt")
	shellWrite := "echo x > " + fencedShell

	cfg := t.TempDir()
	if err := os.Symlink(auth, filepath.Join(cfg, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "cli-config.json"), []byte(`{"version":1,"permissions":{"allow":[],"deny":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.env["CURSOR_CONFIG_DIR"] = cfg
	agent := func(prompt string, args ...string) cursorStream {
		t.Helper()
		// cursor-agent signs in from the real home and session (as every
		// cursor-agent run satelle dispatches does); satelle's own state stays
		// in the sandbox through SATELLE_HOME.
		saved := map[string]string{}
		for k, v := range map[string]string{"HOME": realHome, "XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"), "XDG_RUNTIME_DIR": os.Getenv("XDG_RUNTIME_DIR")} {
			saved[k] = s.env[k]
			s.env[k] = v
		}
		defer func() {
			for k, v := range saved {
				s.env[k] = v
			}
		}()
		r := s.exec(agentBin, prompt, append([]string{"-p", "--output-format", "stream-json", "--trust", "--model", model}, args...)...)
		if r.code != 0 {
			t.Fatalf("cursor-agent: exit %d\n%s\n%s", r.code, r.out, r.err)
		}
		return readCursorStream(t, r.out)
	}

	record := "satelle story record " + id + " --file note.md"
	first := agent("You are testing a repo's hooks. Do each step, continuing even if one is refused, and read no files: " +
		"1) Quote verbatim the line of your context that starts with 'satelle session:' and the line holding 'principle-nonce'. " +
		"2) With your file write tool (not the shell), create unseated.txt containing x. " +
		"3) With your shell, run exactly: " + record + " " +
		"Report each outcome and any refusal reason verbatim.\n")
	if first.Session == "" {
		t.Fatalf("no session in the stream: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "unseated.txt")); !os.IsNotExist(err) {
		t.Errorf("the unseated edit was written: %v", err)
	}
	_, want := s.gate(first.Session, "Edit", filepath.Join(s.dir, "unseated.txt"), "")
	var editRefused, recordRefused bool
	for _, c := range first.Calls {
		switch {
		case c.Kind == "editToolCall" && strings.HasSuffix(c.Target, "unseated.txt"):
			editRefused = c.Rejected && strings.HasPrefix(c.Reason, want)
			if !editRefused {
				t.Errorf("unseated edit: rejected %v reason %q, want the claude gate's %q", c.Rejected, c.Reason, want)
			}
		case c.Kind == "shellToolCall" && strings.Contains(c.Target, "story record"):
			recordRefused = c.Rejected && strings.HasPrefix(c.Reason, hook.OperatorRecordDenied)
			if !recordRefused {
				t.Errorf("operator verb: rejected %v reason %q", c.Rejected, c.Reason)
			}
		}
	}
	if !editRefused || !recordRefused {
		t.Fatalf("inconclusive or failed: calls %+v", first.Calls)
	}
	for _, w := range []string{"satelle session: " + first.Session, "principle-nonce-8c2f"} {
		if !strings.Contains(first.Result, w) {
			t.Errorf("the session did not report %q from its context:\n%s", w, first.Result)
		}
	}

	s.must("story", "set", id, "--status", "doing")
	s.must("story", "seat", id, "--session", first.Session)
	second := agent("Do each step, continuing even if one is refused, and read no files: "+
		"1) With your file write tool (not the shell), create seated.txt containing ok. "+
		"2) With your file write tool (not the shell), create "+fencedEdit+" containing x. "+
		"3) With your shell, run exactly: "+shellWrite+" "+
		"Report each outcome and any refusal reason verbatim.\n", "--resume", first.Session)
	if second.Session != first.Session {
		t.Errorf("resumed session %q, want %q", second.Session, first.Session)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "seated.txt")); err != nil {
		t.Errorf("the seated edit was not written: %v; calls %+v", err, second.Calls)
	}
	for _, f := range []string{fencedEdit, fencedShell} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("a write into another repository's tree happened: %s: %v", f, err)
		}
	}
	_, wantEdit := s.gate(first.Session, "Edit", fencedEdit, "")
	_, wantShell := s.gate(first.Session, "Bash", "", shellWrite)
	for _, w := range []string{wantEdit, wantShell} {
		if !strings.Contains(w, "another repository's working tree") {
			t.Fatalf("the claude gate's reason is not the fence: %q", w)
		}
	}
	var editFenced, shellFenced bool
	for _, c := range second.Calls {
		switch {
		case c.Kind == "editToolCall" && strings.HasSuffix(c.Target, "fenced.txt"):
			editFenced = c.Rejected && strings.HasPrefix(c.Reason, wantEdit)
			if !editFenced {
				t.Errorf("foreign edit: rejected %v reason %q, want the claude gate's %q", c.Rejected, c.Reason, wantEdit)
			}
		case c.Kind == "shellToolCall" && strings.Contains(c.Target, "fenced-sh.txt"):
			shellFenced = c.Rejected && strings.HasPrefix(c.Reason, wantShell)
			if !shellFenced {
				t.Errorf("foreign shell write: rejected %v reason %q, want the claude gate's %q", c.Rejected, c.Reason, wantShell)
			}
		}
	}
	if !editFenced || !shellFenced {
		t.Fatalf("fence inconclusive or failed: calls %+v", second.Calls)
	}
}
