//go:build integration && !windows

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeReader is a fake `claude` judge that reads the plan from the record
// folder its prompt names, not from the prompt: it saves the prompt and the
// path it found, and rejects when the file holds CONTRADICTS-AC1.
func fakeReader(t *testing.T, s *sandbox) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), `#!/bin/sh
cat > "$FAKE_DIR/prompt.$$"
path=$(grep -o '/[^ ]*/\.satelle/runs/[^ ]*/attachments/plan' "$FAKE_DIR/prompt.$$" | head -1)
printf '%s' "$path" > "$FAKE_DIR/path"
v=accept; n="the plan holds"
if [ -n "$path" ] && grep -q CONTRADICTS-AC1 "$path"; then v=reject; n="plan contradicts AC1"; fi
printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"verdict\\":\\"%s\\",\\"notes\\":\\"%s\\"}","usage":{"input_tokens":3,"output_tokens":2}}\n' "$v" "$n"
`, 0o755)
	s.path = append(s.path, dir)
	s.env["FAKE_DIR"] = dir
	return dir
}

const plannedWorkflow = `
[states.plan]
start = true
[states.in_progress]
terminal = true

[gates.plan-review]
skill = "judge"
agent = "reviewer"

[[transition]]
from = ["plan"]
to = "in_progress"
groups = [{ mode = "sequential", gates = ["plan-review"] }]
`

// A plan-review judge reads the attached plan from the item's record folder:
// a contradiction beyond the inline bound is found only there, the folder is
// gitignored and gone after the run, and each presentation is its own attempt.
func TestPlanReviewReadsThePlanFromTheRecord(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	fake := fakeReader(t, s)
	s.must("init")
	s.write(".satelle/agents.toml", "[agents.reviewer]\ncommand = \"claude\"\nmodel = \"rv-1\"\ntools = [\"Read\"]\n")
	s.write(".satelle/workflows/planned.toml", plannedWorkflow)
	s.write(".satelle/skills/judge.md", "---\nname: judge\n---\nJudge the plan against the criteria.\n")
	s.must("validate")
	if r := s.exec("git", "", "check-ignore", "-q", ".satelle/runs/sto_x-1/attachments/plan"); r.code != 0 {
		t.Fatalf(".satelle/runs is not gitignored: %s%s", r.out, r.err)
	}
	id := s.create("story", "--title", "planned", "--workflow", "planned", "--criteria", "AC1: keep the ledger").ID

	filler := strings.Repeat("a plan line that says nothing much.\n", 600) // ~21 KiB, past the 16 KiB inline bound
	if r := s.exec(satelleBin, filler+"CONTRADICTS-AC1\n", "story", "attach", id, "--name", "plan", "--file", "-"); r.code != 0 {
		t.Fatalf("attach: %s%s", r.out, r.err)
	}
	r := s.run("story", "set", id, "--status", "in_progress")
	if r.code == 0 || !strings.Contains(r.out+r.err, "plan contradicts AC1") {
		t.Fatalf("a contradicting plan was not rejected: code %d %s%s", r.code, r.out, r.err)
	}
	prompts, _ := filepath.Glob(filepath.Join(fake, "prompt.*"))
	if len(prompts) != 1 {
		t.Fatalf("judge runs = %d", len(prompts))
	}
	if b, _ := os.ReadFile(prompts[0]); strings.Contains(string(b), "CONTRADICTS-AC1") {
		t.Fatal("the contradiction was inlined; the judge did not need the folder")
	}
	path, _ := os.ReadFile(filepath.Join(fake, "path"))
	if len(path) == 0 {
		t.Fatal("the judge found no record path in its prompt")
	}
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Fatalf("the record folder %s outlived the run", path)
	}

	if r := s.exec(satelleBin, filler, "story", "attach", id, "--name", "plan", "--file", "-"); r.code != 0 {
		t.Fatalf("attach v2: %s%s", r.out, r.err)
	}
	s.must("story", "set", id, "--status", "in_progress")
	if got := s.get(id).Status; got != "in_progress" {
		t.Fatalf("status = %s", got)
	}
	attempts := map[string]bool{}
	for _, e := range s.ledger(id, "gate.run") {
		var g struct {
			Attempt string `json:"attempt"`
		}
		if err := json.Unmarshal(e.Payload, &g); err != nil || g.Attempt == "" {
			t.Fatalf("gate.run without an attempt: %s", e.Payload)
		}
		attempts[g.Attempt] = true
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %v, want two presentations", attempts)
	}
}
