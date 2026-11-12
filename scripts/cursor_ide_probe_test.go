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
