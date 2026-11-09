//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This repo's doctor.toml runs the drift check and lists open drift items
// through the real binary: a skill that names a command the binary lacks is a
// check:satelle-drift-substrate-check issue with the configured fix, and an
// open story tagged drift is an open:drift issue with its id in the fix
// (sto_3cf973ae AC1).
func TestDoctorRunsThisRepoDriftChecks(t *testing.T) {
	s := driftCheckSandbox(t)
	for _, rel := range []string{".satelle/doctor.toml", ".satelle/skills/satelle-drift-substrate-check.md", "scripts/drift-check.sh"} {
		b, err := os.ReadFile(filepath.Join("..", rel))
		if err != nil {
			t.Fatal(err)
		}
		s.write(rel, string(b))
	}
	s.write(".satelle/skills/stale.md", "---\nname: stale\ntype: skill\ndescription: Names a removed command.\n---\n\nRun `satelle story docs <id>`.\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "doctor substrate")
	id := s.create("story", "--title", "a drift finding", "--criteria", "c", "--tag", "drift").ID

	r := s.run("doctor", "--json")
	if r.code != 1 {
		t.Fatalf("doctor exit %d, want 1\n%s%s", r.code, r.out, r.err)
	}
	var rep struct {
		Issues []struct{ Check, Problem, Fix string } `json:"issues"`
	}
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, r.out)
	}
	var drift, open bool
	for _, is := range rep.Issues {
		if is.Check == "check:satelle-drift-substrate-check" && strings.Contains(is.Problem, ".satelle/skills/stale.md") &&
			strings.Contains(is.Problem, "satelle story docs") && strings.Contains(is.Fix, "drift-stories.sh") {
			drift = true
		}
		if is.Check == "open:drift" && strings.Contains(is.Problem, id) && strings.Contains(is.Fix, "satelle story get "+id) {
			open = true
		}
	}
	if !drift || !open {
		t.Fatalf("drift check found %v, open drift found %v in:\n%s", drift, open, r.out)
	}
}
