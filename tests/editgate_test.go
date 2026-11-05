package tests

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// gate presents one PreToolUse payload to `hook gate --harness claude` and
// reports whether it was denied, and why.
func (s *sandbox) gate(session, tool, file, command string) (deny bool, reason string) {
	s.t.Helper()
	input := map[string]string{}
	if file != "" {
		input["file_path"] = file
	}
	if command != "" {
		input["command"] = command
	}
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": session, "cwd": s.dir,
		"tool_name": tool, "tool_input": input,
	})
	r := s.exec(satelleBin, string(payload), "hook", "gate", "--harness", "claude")
	if r.code != 0 {
		s.t.Fatalf("hook gate: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if strings.TrimSpace(r.out) == "" {
		return false, ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		s.t.Fatalf("hook output %q: %v", r.out, err)
	}
	return doc.HookSpecificOutput.PermissionDecision == "deny", doc.HookSpecificOutput.PermissionDecisionReason
}

// The edit gate: a seat in a performing state is the only way to edit, except
// exempt paths; locked paths and another repo's tree are never editable.
func TestEditGate(t *testing.T) {
	t.Parallel()
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
	s.write(".satelle/satelle.toml", `[workflows]
default = "default"
[gate]
edit_exempt_paths = ["notes/**"]
locked_paths = ["vendor/**"]
`)
	s.must("validate")
	// Another repository's tree. It sits inside this one because paths under
	// the temp dir that are outside this repo are not the gate's concern.
	foreign := filepath.Join(s.dir, "ext", "other")
	s.git("init", "-q", foreign)
	in := func(rel string) string { return filepath.Join(s.dir, rel) }

	id := s.create("story", "--title", "edit me").ID
	cases := []struct {
		name, session, tool, file, command string
		deny                               bool
	}{
		{"no seat", "s-1", "Edit", in("main.go"), "", true},
		{"no seat, exempt path", "s-1", "Write", in("notes/a.md"), "", false},
		{"no seat, shell write", "s-1", "Bash", "", "echo hi > out.txt", true},
		{"no seat, read-only shell", "s-1", "Bash", "", "ls -la", false},
	}
	check := func(stage string) {
		t.Helper()
		for _, c := range cases {
			if deny, why := s.gate(c.session, c.tool, c.file, c.command); deny != c.deny {
				t.Errorf("%s: %s: deny = %v, want %v (%s)", stage, c.name, deny, c.deny, why)
			}
		}
	}
	check("unseated")

	s.must("story", "seat", id, "--session", "s-1")
	cases = []struct {
		name, session, tool, file, command string
		deny                               bool
	}{
		{"seat in a non-performing state", "s-1", "Edit", in("main.go"), "", true},
		{"seat in a non-performing state, shell", "s-1", "Bash", "", "touch x", true},
	}
	check("seated in todo")

	s.must("story", "set", id, "--status", "doing")
	cases = []struct {
		name, session, tool, file, command string
		deny                               bool
	}{
		{"seated edit", "s-1", "Edit", in("main.go"), "", false},
		{"seated shell write", "s-1", "Bash", "", "echo hi > out.txt", false},
		{"another session", "s-2", "Edit", in("main.go"), "", true},
		{"another session's shell write", "s-2", "Bash", "", "mv a.txt b.txt", true},
		{"locked path", "s-1", "Edit", in("vendor/lib.go"), "", true},
		{"locked path by shell", "s-1", "Bash", "", "rm vendor/lib.go", true},
		{"foreign tree", "s-1", "Edit", filepath.Join(foreign, "main.go"), "", true},
		{"foreign tree by shell", "s-1", "Bash", "", "touch " + filepath.Join(foreign, "x"), true},
	}
	check("seated in doing")

	deny, why := s.gate("s-2", "Edit", in("main.go"), "")
	if !deny || strings.Contains(why, "doing") || strings.Contains(why, "hands") {
		t.Errorf("the deny reason names the workflow's own names: %q", why)
	}

	s.must("story", "release", id, "--session", "s-1")
	if deny, _ := s.gate("s-1", "Edit", in("main.go"), ""); !deny {
		t.Error("an edit was allowed after release")
	}
}
