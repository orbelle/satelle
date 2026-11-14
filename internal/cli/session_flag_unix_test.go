//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sto_2a41b9e4 AC1: a gate's check script inherits the session named by
// --session, so a check's own satelle calls act as that session (the E1
// exception of the docs scanner relies on it).
func TestACheckInheritsTheSessionFlag(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	// The check prints the session it runs as, and fails so its notes are kept.
	check := "---\ntype: check\n---\n```sh\necho session=$SATELLE_SESSION\nexit 1\n```\n"
	if err := os.WriteFile(filepath.Join(dir, ".satelle/skills/has-ok.md"), []byte(check), 0o644); err != nil {
		t.Fatal(err)
	}
	id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")
	must(t, "story", "set", id, "--status", "st-two")
	t.Setenv("SATELLE_SESSION", "")
	if _, err := run(t, "", "story", "set", id, "--status", "st-three", "--session", "s-flag"); err == nil {
		t.Fatal("the failing check let the move through")
	}
	var runs []struct {
		Payload struct {
			Gate  string `json:"gate"`
			Notes string `json:"notes"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(must(t, "ledger", "list", id, "--kind", "gate.run", "--json")), &runs); err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Payload.Gate == "g1" && strings.Contains(r.Payload.Notes, "session=s-flag") {
			return
		}
	}
	t.Errorf("no g1 run printed session=s-flag: %+v", runs)
}
