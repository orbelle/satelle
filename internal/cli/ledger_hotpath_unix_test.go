//go:build !windows

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

// hotPath runs every hook, gate, story and ledger read and write a session
// makes, each of which must succeed without the store.
func hotPath(t *testing.T, dir string) {
	t.Helper()
	session := `{"session_id":"hot-1","cwd":"` + dir + `"}`
	for _, verb := range []string{"context", "prompt"} {
		if out, err := run(t, session, "hook", verb); err != nil {
			t.Fatalf("hook %s: %v\n%s", verb, err, out)
		}
	}
	edit, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "hot-1", "cwd": dir,
		"tool_name": "Edit", "tool_input": map[string]string{"file_path": filepath.Join(dir, "notes", "a.md")}})
	if out, err := run(t, string(edit), "hook", "gate", "--harness", "claude"); err != nil {
		t.Fatalf("hook gate: %v\n%s", err, out)
	}
	id := createID(t, "story", "create", "--title", "hot path", "--workflow", "gated")
	must(t, "story", "list")
	must(t, "story", "get", id)
	must(t, "story", "set", id, "--status", "st-two")
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "set", id, "--status", "st-three") // runs and accepts the check gate
	must(t, "ledger", "list")
	must(t, "ledger", "list", id, "--json")
	must(t, "ledger", "verify")
	must(t, "ledger", "rebuild")
	if err := os.Remove(filepath.Join(dir, "ok.txt")); err != nil {
		t.Fatal(err)
	}
}

// Only satelle ledger push, pull and status resolve or contact the store:
// hooks, gates, and story and ledger reads complete with an unreachable
// store configured and make no store call.
func TestHotPathMakesNoStoreCall(t *testing.T) {
	t.Run("store command", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		writeGatedWorkflow(t, dir)
		work := t.TempDir()
		calls := filepath.Join(work, "calls.log")
		rec := filepath.Join(work, "rec.sh")
		if err := os.WriteFile(rec, []byte("echo \"$@\" >> '"+calls+"'\necho 'error: unavailable: down' >&2\nexit 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		configureLedger(t, dir, `store = { command = "sh", args = ['`+rec+`'] }`)
		must(t, "validate")
		hotPath(t, dir)
		if _, err := os.Stat(calls); !os.IsNotExist(err) {
			b, _ := os.ReadFile(calls)
			t.Fatalf("the hot path called the store:\n%s", b)
		}
		// Control: push does call it, and shows its error.
		out, err := run(t, "", "ledger", "push")
		if ExitCodeOf(err) != 1 || !strings.Contains(out+err.Error(), "error: unavailable: down") {
			t.Fatalf("push through a down store: %v\n%s", err, out)
		}
		if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "list chains/") {
			t.Fatalf("push made no list call: %q", b)
		}
	})
	t.Run("satelle.dev", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		writeGatedWorkflow(t, m.dir)
		must(t, "validate")
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		before := len(srv.Calls())
		hotPath(t, m.dir)
		if got := calledAny(srv, before, storeServices...); len(got) != 0 {
			t.Fatalf("the hot path called the store: %v", got)
		}
		srv.Stop()
		hotPath(t, m.dir) // and completes with the server unreachable
		if _, err := run(t, "", "ledger", "push"); ExitCodeOf(err) != 1 {
			t.Fatalf("push to an unreachable server: %v", err)
		}
	})
}
