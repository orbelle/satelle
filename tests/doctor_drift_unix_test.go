//go:build integration && unix

package tests

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const driftOwnershipCheck = "check:satelle-drift-ownership-check"

func driftOwnershipSandbox(t *testing.T) *sandbox {
	t.Helper()
	s := driftCheckSandbox(t)
	s.write(".satelle/doctor.toml", repoText(t, ".satelle/doctor.toml"))
	s.write(".satelle/skills/satelle-drift-ownership-check.md", repoText(t, ".satelle/skills/satelle-drift-ownership-check.md"))
	s.write(".satelle/workflows/default.toml", `name = "default"

[states.backlog]
start = true

[states.done]
terminal = true

[states.cancelled]
terminal = true

[[transition]]
from = ["backlog"]
to = "done"

[[transition]]
from = ["backlog"]
to = "cancelled"
`)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "doctor drift ownership")
	s.mustConfigValid()
	return s
}

func checkIssues(rep doctorReportJSON, check string) []doctorIssueJSON {
	var out []doctorIssueJSON
	for _, is := range rep.Issues {
		if is.Check == check {
			out = append(out, is)
		}
	}
	return out
}

func checkNotes(rep doctorReportJSON, prefix string) []string {
	var out []string
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

func hookContextClaude(t *testing.T, s *sandbox) (ctx, msg string) {
	t.Helper()
	payload := fmt.Sprintf(`{"session_id":"s-hook","cwd":%q}`, s.dir)
	r := s.exec(satelleBin, payload, "hook", "context", "--harness", "claude")
	if r.code != 0 {
		t.Fatalf("hook context --harness claude: exit %d\n%s%s", r.code, r.out, r.err)
	}
	var env struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		Message string `json:"systemMessage"`
	}
	if err := json.Unmarshal([]byte(r.out), &env); err != nil {
		t.Fatalf("hook context output: %v\n%s", err, r.out)
	}
	return env.Hook.Context, env.Message
}

func TestDoctorRunsThisRepoDriftChecks(t *testing.T) {
	s := driftCheckSandbox(t)
	s.write(".satelle/doctor.toml", repoText(t, ".satelle/doctor.toml"))
	s.write(".satelle/skills/satelle-drift-ownership-check.md", repoText(t, ".satelle/skills/satelle-drift-ownership-check.md"))
	s.write(".satelle/skills/stale.md", "---\nname: stale\ntype: skill\ndescription: Names a removed command.\n---\n\nRun `satelle story docs <id>`.\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "doctor substrate")
	id := s.create("story", "--title", "a drift finding", "--criteria", "c", "--tag", "drift").ID

	r := s.run("doctor", "--json")
	if r.code != 1 {
		t.Fatalf("doctor exit %d, want 1\n%s%s", r.code, r.out, r.err)
	}
	var rep doctorReportJSON
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, r.out)
	}
	var drift, open bool
	for _, is := range rep.Issues {
		if is.Check == "substrate" && strings.HasPrefix(is.Problem, ".satelle/skills/stale.md:7: command: satelle story docs — ") {
			drift = true
		}
		if is.Check == driftOwnershipCheck &&
			strings.Contains(is.Problem, id) &&
			strings.Contains(is.Problem, "(no drift-key)") &&
			strings.Contains(is.Fix, "satelle story get <id>") &&
			strings.Contains(is.Fix, "drift-owns:<drift-key>") {
			open = true
		}
	}
	if !drift || !open {
		t.Fatalf("substrate found %v, open drift found %v in:\n%s", drift, open, r.out)
	}
}

func TestDoctorDriftOwnershipCheck(t *testing.T) {
	t.Run("all_keys_owned", func(t *testing.T) {
		s := driftOwnershipSandbox(t)
		const (
			key1    = "constitution:internal/cli/http.go:cli to satelled http"
			key2    = "command:.satelle/skills/helper.md:satelle old"
			warnKey = "prose:sto_00000001:no drift-key"
		)
		body := "## Findings\n\ndrift-key: " + key1 + "\ndrift-key: " + key2 + "\n\n## Warnings\n\ndrift-warning: " + warnKey + "\n"
		driftID := s.create("story", "--title", "Drift fix", "--body", body, "--criteria", "c", "--tag", "drift", "--tag", "drift-fix").ID
		owner1 := s.create("story", "--title", "fix http", "--criteria", "c", "--tag", "drift-owns:"+key1).ID
		owner2 := s.create("story", "--title", "fix old cmd A", "--criteria", "c", "--tag", "drift-owns:"+key2).ID
		owner3 := s.create("story", "--title", "fix old cmd B", "--criteria", "c").ID
		s.must("story", "set", owner3, "--add-tag", "drift-owns:"+key2)

		rep := doctorJSON(t, s, satelleBin)
		if len(rep.Unchecked) != 0 {
			t.Fatalf("unchecked = %v, want none", rep.Unchecked)
		}
		for _, bad := range []string{driftOwnershipCheck, "open:drift", "config", "substrate"} {
			if got := checkIssues(rep, bad); len(got) != 0 {
				t.Errorf("%s issues = %+v, want none", bad, got)
			}
		}
		pair := []string{owner2 + " (backlog)", owner3 + " (backlog)"}
		slices.Sort(pair)
		wantNotes := []string{
			driftOwnershipCheck + ": " + key1 + " (" + driftID + ") owned by " + owner1 + " (backlog)",
			driftOwnershipCheck + ": " + key2 + " (" + driftID + ") owned by " + strings.Join(pair, ", "),
		}
		if got := checkNotes(rep, driftOwnershipCheck+":"); !slices.Equal(got, wantNotes) {
			t.Fatalf("notes = %q, want %q", got, wantNotes)
		}

		text := s.run("doctor").out
		for _, w := range wantNotes {
			if !strings.Contains(text, "note: "+w+"\n") {
				t.Errorf("doctor text lacks %q:\n%s", "note: "+w, text)
			}
		}
		if strings.Contains(text, warnKey) {
			t.Errorf("doctor text unexpectedly reported drift-warning %q:\n%s", warnKey, text)
		}

		ctx, msg := hookContextClaude(t, s)
		for _, bad := range []string{driftOwnershipCheck, "open:drift"} {
			if strings.Contains(ctx, bad) {
				t.Errorf("hook context additionalContext mentions %q:\n%s", bad, ctx)
			}
			if strings.Contains(msg, bad) {
				t.Errorf("hook context systemMessage mentions %q:\n%s", bad, msg)
			}
		}
	})

	t.Run("unowned_keys", func(t *testing.T) {
		s := driftOwnershipSandbox(t)
		const (
			ownedKey = "constitution:internal/cli/a.go:owned finding"
			unowned1 = "constitution:internal/cli/b.go:first unowned"
			unowned2 = "flag:.satelle/skills/helper.md:satelle story list --old"
		)
		body := "## Findings\n\ndrift-key: " + ownedKey + "\ndrift-key: " + unowned1 + "\ndrift-key: " + unowned2 + "\n"
		driftID := s.create("story", "--title", "Drift fix", "--body", body, "--criteria", "c", "--tag", "drift", "--tag", "drift-fix", "--tag", "drift-owns:"+unowned1).ID
		s.create("story", "--title", "owns one", "--criteria", "c", "--tag", "drift-owns:"+ownedKey)

		rep := doctorJSON(t, s, satelleBin)
		if len(rep.Unchecked) != 0 {
			t.Fatalf("unchecked = %v, want none", rep.Unchecked)
		}
		for _, bad := range []string{"open:drift", "config", "substrate"} {
			if got := checkIssues(rep, bad); len(got) != 0 {
				t.Errorf("%s issues = %+v, want none", bad, got)
			}
		}
		if got := checkNotes(rep, driftOwnershipCheck+":"); len(got) != 0 {
			t.Errorf("failing check emitted notes %q, want none", got)
		}
		issues := checkIssues(rep, driftOwnershipCheck)
		if len(issues) != 1 {
			t.Fatalf("drift issues = %+v, want 1", issues)
		}
		prob := issues[0].Problem
		for _, want := range []string{driftID, unowned1, unowned2} {
			if !strings.Contains(prob, want) {
				t.Errorf("issue problem %q lacks %q", prob, want)
			}
		}
		if strings.Contains(prob, ownedKey) {
			t.Errorf("issue problem %q unexpectedly names owned key %q", prob, ownedKey)
		}
		if !strings.Contains(issues[0].Fix, "satelle story get <id>") {
			t.Errorf("issue fix %q lacks guidance", issues[0].Fix)
		}

		ctx, msg := hookContextClaude(t, s)
		if !strings.Contains(ctx, driftOwnershipCheck) || !strings.Contains(ctx, driftID) {
			t.Errorf("hook context additionalContext lacks drift issue for %s:\n%s", driftID, ctx)
		}
		if !strings.Contains(msg, driftOwnershipCheck) {
			t.Errorf("hook context systemMessage lacks %s:\n%s", driftOwnershipCheck, msg)
		}
	})

	t.Run("owner_closed", func(t *testing.T) {
		for _, terminal := range []string{"done", "cancelled"} {
			t.Run(terminal, func(t *testing.T) {
				s := driftOwnershipSandbox(t)
				const key = "constitution:internal/cli/http.go:cli to satelled http"
				driftID := s.create("story", "--title", "Drift fix", "--body", "## Findings\n\ndrift-key: "+key+"\n", "--criteria", "c", "--tag", "drift", "--tag", "drift-fix").ID
				ownerID := s.create("story", "--title", "fix http", "--criteria", "c", "--tag", "drift-owns:"+key).ID

				before := doctorJSON(t, s, satelleBin)
				if len(before.Unchecked) != 0 {
					t.Fatalf("before close: unchecked %v", before.Unchecked)
				}
				if got := checkIssues(before, driftOwnershipCheck); len(got) != 0 {
					t.Fatalf("before close: drift issues = %+v, want none", got)
				}
				wantNote := driftOwnershipCheck + ": " + key + " (" + driftID + ") owned by " + ownerID + " (backlog)"
				if got := checkNotes(before, driftOwnershipCheck+":"); !slices.Equal(got, []string{wantNote}) {
					t.Fatalf("before close: notes = %q, want [%q]", got, wantNote)
				}

				s.must("story", "set", ownerID, "--status", terminal)

				after := doctorJSON(t, s, satelleBin)
				if len(after.Unchecked) != 0 {
					t.Fatalf("after %s: unchecked %v", terminal, after.Unchecked)
				}
				if got := checkNotes(after, driftOwnershipCheck+":"); len(got) != 0 {
					t.Fatalf("after %s: notes = %q, want none", terminal, got)
				}
				issues := checkIssues(after, driftOwnershipCheck)
				if len(issues) != 1 || !strings.Contains(issues[0].Problem, driftID) || !strings.Contains(issues[0].Problem, key) {
					t.Fatalf("after %s: drift issues = %+v, want one issue naming %s and %q", terminal, issues, driftID, key)
				}
			})
		}
	})
}
