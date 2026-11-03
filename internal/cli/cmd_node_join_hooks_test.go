package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// joinWithBinding brings a rig's node up with its one binding running command,
// whose executable is a stub on PATH so the binding resolves, ends it, and
// returns the node's join log. The node is a fresh machine with no harness
// session open, so the suite's own session markers are cleared: under them init
// installs a session's hooks lazily, which a node never does.
func joinWithBinding(t *testing.T, r *nodeRig, executable, command string) string {
	t.Helper()
	plainCaller(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, executable), []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r.bundle["workflows/agents.toml"] = "[worker]\nrole = \"agent\"\ncommand = \"" + command + "\"\ntools = \"read_file\"\n"
	run, _, joined, _ := r.open(t)
	if rep := run.Report; rep.Error != "" {
		t.Fatalf("report error = %q\nnode log:\n%s", rep.Error, r.log)
	}
	if err := run.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	return r.log.String()
}

// A node whose definition binds claude installs satelle's claude hooks into the
// node repo's .claude/settings.json, as `satelle init --harness claude` does, and
// its join log names them instead of reporting no process hooks (sty_a9530d39).
func TestNodeJoinInstallsTheDefinitionsHarnessHooks(t *testing.T) {
	r := newNodeRig(t)
	log := joinWithBinding(t, r, "claude", "claude -p --system-prompt {system} --allowedTools {tools}")

	settings, err := os.ReadFile(filepath.Join(r.nodeRoot, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("join installed no .claude/settings.json: %v\nnode log:\n%s", err, log)
	}
	if !strings.Contains(string(settings), "satelle-hook.sh gate claude") {
		t.Errorf(".claude/settings.json lacks the PreToolUse gate hook:\n%s", settings)
	}
	if _, err := os.Stat(filepath.Join(r.nodeRoot, ".grok", "hooks", "satelle.json")); err == nil {
		t.Error("join installed grok hooks for a definition that binds only claude")
	}
	if !strings.Contains(log, "process hooks installed: claude") {
		t.Errorf("join log does not name the installed claude hooks:\n%s", log)
	}
	if strings.Contains(log, "process hooks: none") {
		t.Errorf("join log still reports no process hooks:\n%s", log)
	}
}

// A node whose definition binds no harness satelle installs hooks for (pi) joins
// as before: the hook scripts are written, no harness settings are, and the log
// says no process hooks were installed.
func TestNodeJoinWithNoHookHarnessInstallsNone(t *testing.T) {
	r := newNodeRig(t)
	log := joinWithBinding(t, r, "pi", "pi --print {system}")

	if _, err := os.Stat(filepath.Join(r.nodeRoot, ".claude", "settings.json")); err == nil {
		t.Error("join installed claude hooks for a definition that binds only pi")
	}
	if _, err := os.Stat(filepath.Join(r.nodeRoot, ".satelle", "hooks", "satelle-hook.sh")); err != nil {
		t.Errorf("join wrote no hook script: %v", err)
	}
	if !strings.Contains(log, "process hooks: none") {
		t.Errorf("join log does not say no process hooks were installed:\n%s", log)
	}
	if strings.Contains(log, "process hooks installed") {
		t.Errorf("join log claims hooks were installed:\n%s", log)
	}
}
