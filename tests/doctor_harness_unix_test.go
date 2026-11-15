//go:build integration && unix

package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

func harnessIssues(t *testing.T, s *sandbox) (int, []struct{ Check, Problem, Fix string }) {
	t.Helper()
	r := s.run("doctor", "--json")
	var rep struct {
		Issues []struct{ Check, Problem, Fix string } `json:"issues"`
	}
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s\n%s", err, r.out, r.err)
	}
	out := rep.Issues[:0]
	for _, is := range rep.Issues {
		if is.Check == "harness" {
			out = append(out, is)
		}
	}
	return r.code, out
}

func TestDoctorNamesTheUnwiredHarness(t *testing.T) {
	t.Run("claude calling", func(t *testing.T) {
		s := newSandbox(t)
		s.env["CLAUDECODE"] = "1"
		s.must("init")
		code, issues := harnessIssues(t, s)
		if code != 1 || len(issues) != 1 || !strings.Contains(issues[0].Problem, "claude") || issues[0].Fix != "satelle agents install claude" {
			t.Fatalf("after init alone: exit %d, harness issues %+v", code, issues)
		}
		s.must("agents", "install", "claude")
		if _, issues := harnessIssues(t, s); len(issues) != 0 {
			t.Fatalf("after agents install claude: harness issues %+v", issues)
		}
	})
	t.Run("no calling harness", func(t *testing.T) {
		s := newSandbox(t)
		s.must("init")
		code, issues := harnessIssues(t, s)
		if code != 1 || len(issues) != 1 || !strings.Contains(issues[0].Problem, "no harness is wired") {
			t.Fatalf("after init alone: exit %d, harness issues %+v", code, issues)
		}
		s.must("agents", "install", "claude")
		if _, issues := harnessIssues(t, s); len(issues) != 0 {
			t.Fatalf("after agents install claude: harness issues %+v", issues)
		}
	})
}
