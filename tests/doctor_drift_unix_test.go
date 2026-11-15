//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorRunsThisRepoDriftChecks(t *testing.T) {
	s := driftCheckSandbox(t)
	b, err := os.ReadFile(filepath.Join("..", ".satelle/doctor.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/doctor.toml", string(b))
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
		if is.Check == "substrate" && strings.HasPrefix(is.Problem, ".satelle/skills/stale.md:7: command: satelle story docs — ") {
			drift = true
		}
		if is.Check == "open:drift" && strings.Contains(is.Problem, id) && strings.Contains(is.Fix, "satelle story get "+id) {
			open = true
		}
	}
	if !drift || !open {
		t.Fatalf("substrate found %v, open drift found %v in:\n%s", drift, open, r.out)
	}
}
