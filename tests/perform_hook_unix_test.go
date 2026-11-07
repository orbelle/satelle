//go:build integration && !windows

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const handworkWorkflow = `name = "handwork"
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
`

// A performer satelle dispatches here edits the repo through the real
// installed hook: Perform seats the run at local:<tree> under a session of its
// own, hands it to the performer, and releases it when the run ends, also when
// the run fails. The stand-in claude runs the hooks .claude/settings.json
// names, with a session id of its own in the payload, so only the session
// Perform hands it can let an edit through; an unseated driver session in the
// environment never does.
func TestLocalPerformerEditsThroughTheInstalledHook(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	fake := t.TempDir()
	b, err := os.ReadFile(hookHarnessBin)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fake, "claude"), string(b), 0o755)
	s.path = append(s.path, fake)
	s.env["FAKE_DIR"] = fake

	s.must("init", "--hooks=claude")
	if _, err := os.Stat(filepath.Join(s.dir, ".claude", "settings.json")); err != nil {
		t.Fatalf("init installed no claude hooks: %v", err)
	}
	s.write(".satelle/workflows/handwork.toml", handworkWorkflow)
	s.write(".satelle/agents.toml", "[agents.hands]\ncommand = \"claude\"\ntools = [\"Read\", \"Edit\", \"Write\"]\n")
	s.write(".satelle/skills/work.md", "---\nname: work\n---\nEdit the repo.\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")
	s.env["SATELLE_SESSION"] = "driver"

	id := s.create("story", "--title", "hands on", "--workflow", "handwork").ID
	s.must("story", "set", id, "--status", "doing")

	calls := func() string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(fake, "calls"))
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(fake, "calls"))
		return string(b)
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(s.dir, name))
		return err == nil
	}
	sessionOf := func(e event) (session, location string) {
		t.Helper()
		var p struct {
			Session  string `json:"session"`
			Location string `json:"location"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p.Session, p.Location
	}

	// Control: the stand-in run directly, not dispatched by satelle, is
	// refused by the installed hook, so it does not bypass hooks.
	if r := s.exec(hookHarnessBin, "edit"); r.code != 0 {
		t.Fatalf("stand-in run directly: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := calls(); !strings.Contains(got, "deny Edit") || !strings.Contains(got, "deny Write") {
		t.Fatalf("undispatched calls = %q, want both denied", got)
	}
	if exists("perform-edit.txt") || exists("perform-write.txt") {
		t.Fatal("a denied call wrote its file")
	}

	// Perform: the dispatched performer's Edit and Write pass the gate.
	s.must("story", "perform", id)
	if got := calls(); !strings.Contains(got, "allow Edit") || !strings.Contains(got, "allow Write") {
		t.Fatalf("performer calls = %q, want both allowed", got)
	}
	if !exists("perform-edit.txt") || !exists("perform-write.txt") {
		t.Fatal("the performer's allowed calls wrote nothing")
	}
	bound := s.ledger(id, "seat.bound")
	if len(bound) != 1 {
		t.Fatalf("seat.bound = %d events", len(bound))
	}
	session, location := sessionOf(bound[0])
	if !strings.HasPrefix(session, "perform_") || location != "local:"+s.dir {
		t.Fatalf("performer seat = %q at %q", session, location)
	}
	released := s.ledger(id, "seat.released")
	if len(released) != 1 {
		t.Fatalf("seat.released = %d events", len(released))
	}
	if got, _ := sessionOf(released[0]); got != session {
		t.Fatalf("released %q, bound %q", got, session)
	}
	if n := len(s.ledger(id, "performer.run")); n != 1 {
		t.Fatalf("performer.run = %d events", n)
	}

	// A failing run: its edits still pass, and its seat is released.
	writeFile(t, filepath.Join(fake, "fail"), "", 0o644)
	if r := s.run("story", "perform", id); r.code == 0 {
		t.Fatalf("failing perform exited 0\n%s%s", r.out, r.err)
	}
	if got := calls(); !strings.Contains(got, "allow Edit") || !strings.Contains(got, "allow Write") {
		t.Fatalf("failing performer calls = %q, want both allowed", got)
	}
	bound = s.ledger(id, "seat.bound")
	if len(bound) != 2 {
		t.Fatalf("seat.bound = %d events after the failing run", len(bound))
	}
	second, _ := sessionOf(bound[1])
	if second == session {
		t.Fatalf("the failing run reused session %q", second)
	}
	released = s.ledger(id, "seat.released")
	if len(released) != 2 {
		t.Fatalf("seat.released = %d events after the failing run", len(released))
	}
	if got, _ := sessionOf(released[1]); got != second {
		t.Fatalf("released %q, bound %q", got, second)
	}
	runs := s.ledger(id, "performer.run")
	if len(runs) != 2 {
		t.Fatalf("performer.run = %d events", len(runs))
	}
	var run struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(runs[1].Payload, &run); err != nil || run.Error == "" {
		t.Fatalf("failing performer.run = %s (%v)", runs[1].Payload, err)
	}
}
