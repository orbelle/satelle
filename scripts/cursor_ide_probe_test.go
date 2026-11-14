package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// probe runs cursor-ide-probe.sh with args, stdin and extra environment, in a
// throwaway HOME, and returns its stdout, stderr and exit code.
func probe(t *testing.T, stdin string, env []string, args ...string) (string, string, int) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed")
	}
	cmd := exec.Command("bash", append([]string{"cursor-ide-probe.sh"}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

type probeCapture struct {
	Label     string             `json:"label"`
	Payload   json.RawMessage    `json:"payload"`
	Stdout    string             `json:"stdout"`
	Exit      int                `json:"exit"`
	Ancestors []string           `json:"ancestors"`
	Env       map[string]*string `json:"env"`
}

func readCaptures(t *testing.T, dir string) []probeCapture {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []probeCapture
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var c probeCapture
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, c)
	}
	return out
}

// record passes a hook call through unchanged (stdout and exit code) and
// writes what went in and came out; an observer prints its fixed answer.
func TestCursorIDEProbeRecordPassesThrough(t *testing.T) {
	caps := t.TempDir()
	payload := `{"hook_event_name":"preToolUse","session_id":"s1","tool_name":"Write"}`
	out, _, code := probe(t, payload, []string{"SATELLE_SESSION=leak"}, "record", caps, "gate@preToolUse",
		"sh", "-c", `cat >/dev/null; printf '{"permission":"deny"}'; exit 3`)
	if out != `{"permission":"deny"}`+"\n" || code != 3 {
		t.Fatalf("record changed the exchange: stdout %q, exit %d", out, code)
	}
	got := readCaptures(t, caps)
	if len(got) != 1 {
		t.Fatalf("captures: %d, want 1", len(got))
	}
	c := got[0]
	var p struct {
		Session string `json:"session_id"`
	}
	if err := json.Unmarshal(c.Payload, &p); err != nil || p.Session != "s1" {
		t.Fatalf("payload not recorded as JSON: %v %s", err, c.Payload)
	}
	if c.Label != "gate@preToolUse" || c.Stdout != `{"permission":"deny"}` || c.Exit != 3 {
		t.Fatalf("capture %+v", c)
	}
	if v := c.Env["SATELLE_SESSION"]; v == nil || *v != "leak" {
		t.Fatalf("SATELLE_SESSION not recorded: %v", c.Env["SATELLE_SESSION"])
	}
	if v, ok := c.Env["CURSOR_AGENT"]; !ok || v != nil {
		t.Fatalf("an unset name must be recorded as unset: %v %v", ok, v)
	}

	obs := t.TempDir()
	for label, want := range map[string]string{"observe@beforeShellExecution": `{"permission":"allow"}`, "observe@postToolUse": `{}`} {
		out, _, code := probe(t, `{"command":"ls"}`, nil, "record", obs, label)
		if out != want+"\n" || code != 0 {
			t.Errorf("%s answered %q exit %d, want %q", label, out, code, want)
		}
	}
}

// writeCapture puts one capture in <dir>/captures/wired.
func writeCapture(t *testing.T, dir, name string, c map[string]any) {
	t.Helper()
	p := filepath.Join(dir, "captures", "wired", name+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// collect refuses evidence that is not the IDE's own session and repo, and
// marks a shell command no satelle gate entry saw.
func TestCursorIDEProbeCollect(t *testing.T) {
	ide := "/opt/test-cursor"
	clean := map[string]any{"SATELLE_SESSION": nil, "SATELLE_PROJECT_DIR": nil}
	ideChain := []string{"/usr/bin/bash", ide + "/cursor"}

	t.Run("refuses an inherited satelle session", func(t *testing.T) {
		dir := t.TempDir()
		writeCapture(t, dir, "1", map[string]any{"label": "context@sessionStart", "payload": map[string]any{}, "stdout": "", "exit": 0,
			"ancestors": ideChain, "env": map[string]any{"SATELLE_SESSION": "abc", "SATELLE_PROJECT_DIR": nil}})
		_, errOut, code := probe(t, "", []string{"CURSOR_IDE_DIR=" + ide}, "collect", dir)
		if code == 0 || !strings.Contains(errOut, "SATELLE_SESSION or SATELLE_PROJECT_DIR was set") {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		if _, err := os.Stat(filepath.Join(dir, "cursor-ide-run.md")); err == nil {
			t.Fatal("collect wrote the evidence after refusing")
		}
	})
	t.Run("refuses a cursor-agent ancestor", func(t *testing.T) {
		dir := t.TempDir()
		writeCapture(t, dir, "1", map[string]any{"label": "context@sessionStart", "payload": map[string]any{}, "stdout": "", "exit": 0,
			"ancestors": append(ideChain, "/home/u/.local/share/cursor-agent/versions/x/cursor-agent"), "env": clean})
		_, errOut, code := probe(t, "", []string{"CURSOR_IDE_DIR=" + ide}, "collect", dir)
		if code == 0 || !strings.Contains(errOut, "cursor-agent process is among the hook's ancestors") {
			t.Fatalf("exit %d: %s", code, errOut)
		}
	})
	t.Run("refuses captures with no IDE ancestor", func(t *testing.T) {
		dir := t.TempDir()
		writeCapture(t, dir, "1", map[string]any{"label": "context@sessionStart", "payload": map[string]any{}, "stdout": "", "exit": 0,
			"ancestors": []string{"/usr/bin/bash"}, "env": clean})
		if _, errOut, code := probe(t, "", []string{"CURSOR_IDE_DIR=" + ide}, "collect", dir); code == 0 || !strings.Contains(errOut, "no process of the Cursor IDE") {
			t.Fatalf("exit %d: %s", code, errOut)
		}
	})
	t.Run("accepts the runtime a script launcher execs, only with the IDE's version", func(t *testing.T) {
		ideDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(ideDir, "resources", "app"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ideDir, "resources", "app", "product.json"), []byte(`{"version":"9.9.9"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		launcher := "#!/bin/bash\nname=electron99\nELECTRON_RUN_AS_NODE=1 exec /usr/lib/${name}/electron " + ideDir + "/resources/app/out/cli.js \"$@\"\n"
		if err := os.WriteFile(filepath.Join(ideDir, "cursor"), []byte(launcher), 0o755); err != nil {
			t.Fatal(err)
		}
		chain := []string{"/usr/bin/bash", "/usr/lib/electron99/electron"}
		for _, tc := range []struct {
			version string
			ok      bool
		}{{"9.9.9", true}, {"1.0.0", false}} {
			dir := t.TempDir()
			writeCapture(t, dir, "1", map[string]any{"label": "context@sessionStart", "payload": map[string]any{}, "stdout": "", "exit": 0,
				"ancestors": chain, "env": map[string]any{"SATELLE_SESSION": nil, "SATELLE_PROJECT_DIR": nil, "CURSOR_VERSION": tc.version}})
			_, errOut, code := probe(t, "", []string{"CURSOR_IDE_DIR=" + ideDir}, "collect", dir)
			if (code == 0) != tc.ok {
				t.Errorf("CURSOR_VERSION %s: exit %d: %s", tc.version, code, errOut)
			}
		}
	})
	t.Run("marks a shell command no gate saw", func(t *testing.T) {
		dir := t.TempDir()
		writeCapture(t, dir, "1", map[string]any{"label": "observe@beforeShellExecution", "payload": map[string]any{"command": "echo x > unseated-shell.txt"},
			"stdout": `{"permission":"allow"}`, "exit": 0, "ancestors": ideChain, "env": clean})
		writeCapture(t, dir, "2", map[string]any{"label": "gate@preToolUse", "payload": map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": "satelle story set s --status doing"}},
			"stdout": "", "exit": 0, "ancestors": ideChain, "env": clean})
		if _, errOut, code := probe(t, "", []string{"CURSOR_IDE_DIR=" + ide}, "collect", dir); code != 0 {
			t.Fatalf("collect: exit %d: %s", code, errOut)
		}
		b, err := os.ReadFile(filepath.Join(dir, "cursor-ide-run.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"| `echo x > unseated-shell.txt` | observe@beforeShellExecution | NO GATE CAPTURE |",
			"| `satelle story set s --status doing` | gate@preToolUse | (empty: allow) |",
			"## Differences",
		} {
			if !strings.Contains(string(b), want) {
				t.Errorf("evidence lacks %q:\n%s", want, b)
			}
		}
	})
}

// record also captures every set name a harness may set for its session
// (the caller prefixes), storing a credential-like value as redacted.
func TestCursorIDEProbeRecordCallerNames(t *testing.T) {
	caps := t.TempDir()
	if _, errOut, code := probe(t, "", []string{"GROK_PROBE_ONLY=7", "CLAUDE_CODE_OAUTH_TOKEN=s3cret", "UNRELATED_NAME=x"},
		"record", caps, "shell@tool"); code != 0 {
		t.Fatalf("record: exit %d: %s", code, errOut)
	}
	got := readCaptures(t, caps)
	if len(got) != 1 {
		t.Fatalf("captures: %d, want 1", len(got))
	}
	env := got[0].Env
	if v := env["GROK_PROBE_ONLY"]; v == nil || *v != "7" {
		t.Errorf("GROK_PROBE_ONLY not recorded: %v", v)
	}
	if v := env["CLAUDE_CODE_OAUTH_TOKEN"]; v == nil || *v != "redacted" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN not redacted: %v", v)
	}
	if _, ok := env["UNRELATED_NAME"]; ok {
		t.Error("a name outside the caller prefixes was recorded")
	}
}

// writeCallerCapture puts one capture in <dir>/captures/<harness>.
func writeCallerCapture(t *testing.T, dir, harness, name, label string, env map[string]any) {
	t.Helper()
	p := filepath.Join(dir, "captures", harness, name+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"label": label, "payload": "", "stdout": "{}", "exit": 0,
		"ancestors": []string{"/usr/bin/bash", "/opt/" + harness}, "env": env})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// caller-collect writes one fixture per harness from its first gate capture
// and its shell capture, and refuses a harness without either, or a capture
// made inside a satelle session or a Claude Code session.
func TestCursorIDEProbeCallerCollect(t *testing.T) {
	fake := t.TempDir()
	for cli, v := range map[string]string{"cursor-agent": "2026.1.1-abc", "pi": "9.9.9", "grok": "grok 1.2.3 (x) [stable]"} {
		if err := os.WriteFile(filepath.Join(fake, cli), []byte("#!/bin/sh\necho '"+v+"'\necho second line\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := []string{"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}
	full := func(t *testing.T) string {
		dir := t.TempDir()
		for _, h := range []string{"cursor", "pi", "grok"} {
			writeCallerCapture(t, dir, h, "1", "context@start", map[string]any{"GROK_HOOK_EVENT": "start"})
			writeCallerCapture(t, dir, h, "2", "gate@pre", map[string]any{"SATELLE_SESSION": nil, "CLAUDECODE": nil,
				"PATH": "/bin", "GROK_SESSION_ID": "g1", "CURSOR_INVOKED_AS": "cursor-agent"})
			writeCallerCapture(t, dir, h, "3", "gate@pre", map[string]any{"GROK_SESSION_ID": "later"})
			writeCallerCapture(t, dir, h, "4", "shell@tool", map[string]any{"SATELLE_SESSION": nil, "PI_CODING_AGENT": "true", "CURSOR_AGENT": "1"})
		}
		return dir
	}

	t.Run("writes the fixtures", func(t *testing.T) {
		dir := full(t)
		if _, errOut, code := probe(t, "", path, "caller-collect", dir); code != 0 {
			t.Fatalf("caller-collect: exit %d: %s", code, errOut)
		}
		b, err := os.ReadFile(filepath.Join(dir, "fixtures", "caller-grok.json"))
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Harness, CLI, Version, Captured string
			Hook                            struct {
				Label     string
				Ancestors []string
				Env       []string
			}
			Shell struct {
				Ancestors []string
				Env       []string
			}
		}
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		if f.Harness != "grok" || f.CLI != "grok" || f.Version != "grok 1.2.3 (x) [stable]" || f.Captured == "" {
			t.Errorf("fixture header: %s", b)
		}
		if f.Hook.Label != "gate@pre" || strings.Join(f.Hook.Env, " ") != "CURSOR_INVOKED_AS=cursor-agent GROK_SESSION_ID=g1" {
			t.Errorf("hook: not the first gate capture's caller names: %s", b)
		}
		if strings.Join(f.Shell.Env, " ") != "CURSOR_AGENT=1 PI_CODING_AGENT=true" || len(f.Shell.Ancestors) != 2 {
			t.Errorf("shell: %s", b)
		}
		if _, err := os.Stat(filepath.Join(dir, "fixtures", "caller-cursor.json")); err != nil {
			t.Error(err)
		}
	})
	for _, c := range []struct {
		name, want string
		spoil      func(t *testing.T, dir string)
	}{
		{"refuses a missing gate capture", "no gate@ capture", func(t *testing.T, dir string) {
			for _, n := range []string{"2", "3"} {
				if err := os.Remove(filepath.Join(dir, "captures", "pi", n+".json")); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"refuses a missing shell capture", "no shell@tool capture", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "captures", "grok", "4.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"refuses a capture inside claude", "CLAUDECODE was set", func(t *testing.T, dir string) {
			writeCallerCapture(t, dir, "cursor", "4", "shell@tool", map[string]any{"CLAUDECODE": "1", "CURSOR_AGENT": "1"})
		}},
		{"refuses a capture inside a satelle session", "SATELLE_SESSION was set", func(t *testing.T, dir string) {
			writeCallerCapture(t, dir, "cursor", "2", "gate@pre", map[string]any{"SATELLE_SESSION": "s"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := full(t)
			c.spoil(t, dir)
			_, errOut, code := probe(t, "", path, "caller-collect", dir)
			if code == 0 || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if _, err := os.Stat(filepath.Join(dir, "fixtures")); err == nil {
				t.Error("caller-collect wrote fixtures after refusing")
			}
		})
	}
}
