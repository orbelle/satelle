//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// driftRule is the repo's satelle.toml rule that selects the drift workflow,
// as the repo writes it.
func driftRule(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\[\[workflows\.rule\]\]\nworkflow = "drift"\n(?:[a-z]+ = "[^"]*"\n)+`).FindString(repoText(t, ".satelle/satelle.toml"))
	if m == "" {
		t.Fatal(`.satelle/satelle.toml has no [[workflows.rule]] for workflow "drift"`)
	}
	return m
}

// driftStorySandbox is a drift sandbox that also carries the repo's drift
// workflow, its rule, its done gate (the task= line naming the sandbox's drift
// task) and cancel gate skills unchanged, scripts/drift-stories.sh, and the
// hygiene and constitution epics the script parents its stories on. No skill
// is stubbed, so satelle validate passes on the repo's own configuration
// before any case runs.
func newDriftStorySandbox(t *testing.T) *driftSandbox {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal("bash is required by scripts/drift-stories.sh and is not on PATH")
	}
	d := newDriftSandbox(t, nil)
	cfg := readSandbox(t, d.sandbox, ".satelle/satelle.toml")
	i := strings.Index(cfg, "[rules]")
	if i < 0 {
		t.Fatal("the drift sandbox's satelle.toml has no [rules]")
	}
	d.write(".satelle/satelle.toml", cfg[:i]+driftRule(t)+cfg[i:])
	d.write(".satelle/workflows/drift.toml", repoText(t, ".satelle/workflows/drift.toml"))
	d.write(".satelle/skills/satelle-story-cancel-review.md", repoText(t, ".satelle/skills/satelle-story-cancel-review.md"))
	check := regexp.MustCompile(driftTaskLine).ReplaceAllString(repoText(t, ".satelle/skills/satelle-drift-resolved-check.md"), "task="+d.drift)
	if !strings.Contains(check, "\ntask="+d.drift+"\n") {
		t.Fatal("satelle-drift-resolved-check.md has no task=<id> line")
	}
	d.write(".satelle/skills/satelle-drift-resolved-check.md", check)
	d.write("scripts/drift-stories.sh", repoText(t, "scripts/drift-stories.sh"))
	d.must("validate")
	d.create("story", "--title", "Epic: hygiene", "--tag", "epic", "--tag", "epic:hygiene")
	d.create("story", "--title", "Epic: constitution drift", "--tag", "epic", "--tag", "epic:constitution")
	d.git("add", "-A")
	d.git("commit", "-q", "-m", "drift workflow")
	return d
}

func readSandbox(t *testing.T, s *sandbox, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// driftReport is a hand-written drift-report in the sections the done gate
// reads; each argument is that section's lines.
type driftReport struct {
	checkExit                                       string // the drift-check exit line's code; "" leaves the line out
	substrate, constitution, claims, gone, resolved []string
}

func (r driftReport) String() string {
	var b strings.Builder
	b.WriteString("# Drift review of v0.0.2\n\n## Checks\n")
	if r.checkExit != "" {
		b.WriteString("- scripts/drift-check.sh: drift-check exit " + r.checkExit + "\n")
	}
	for _, sec := range []struct {
		name  string
		lines []string
	}{
		{"Substrate findings", r.substrate},
		{"Constitution findings", r.constitution},
		{"Claim findings", r.claims},
		{"No longer found", r.gone},
		{"Resolved by the judges", r.resolved},
	} {
		b.WriteString("\n## " + sec.name + "\n")
		for _, l := range sec.lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// A drift story raised from now on starts on the drift workflow, and its only
// way to done is the drift-resolved check against the in-progress drift run's
// drift-report: each present below leaves its gate.run on the story's ledger.
// Absence from the findings alone never closes a prose or constitution story.
// A cancelled present runs the cancel review on its bound reviewer.
func TestDriftStoryClosesOnTheRunsReport(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	cmdLine, conLine := lines[0], lines[1]
	const flagLine = `.satelle/skills/b.md:4: flag: satelle story list --nope — no such flag`
	const keyLess = "a drift story with no drift-key line"

	r := d.exec("bash", driftFindings+flagLine+"\n", "scripts/drift-stories.sh", "0.0.2")
	if r.code != 0 {
		t.Fatalf("drift-stories.sh: exit %d\n%s%s", r.code, r.out, r.err)
	}
	ids := map[string]string{}
	for key, f := range raised(t, r.out) {
		ids[key] = f[0]
	}
	cmd, con, prose, flag := ids[commandKey], ids[constitutionKey], ids[proseKey], ids["flag:.satelle/skills/b.md:satelle story list --nope"]
	if cmd == "" || con == "" || prose == "" || flag == "" {
		t.Fatalf("drift-stories.sh did not raise all four stories:\n%s", r.out)
	}
	bare := d.create("story", "--title", keyLess, "--tag", "drift", "--body", "no key here\n").ID
	for _, id := range []string{cmd, con, prose, flag, bare} {
		if it := d.get(id); it.Workflow != "drift" || it.Status != "open" {
			t.Fatalf("drift story %s is at %s/%s, want drift/open", id, it.Workflow, it.Status)
		}
	}

	present := func(id, wantResult, wantNotes string) {
		t.Helper()
		r := d.run("story", "set", id, "--status", "done")
		g := latestRun(d.sandbox, id, "drift-resolved")
		if g.Result != wantResult || !strings.Contains(g.Notes, wantNotes) {
			t.Fatalf("%s done: drift-resolved = %s (%q), want %s naming %q", id, g.Result, g.Notes, wantResult, wantNotes)
		}
		want := "open"
		if wantResult == "accept" {
			want = "done"
		}
		if st := d.get(id).Status; st != want || (r.code == 0) != (want == "done") {
			t.Fatalf("%s done: exit %d, status %s; want %s\n%s%s", id, r.code, st, want, r.out, r.err)
		}
	}
	attach := func(exe string, rep driftReport) {
		t.Helper()
		if r := d.exec(satelleBin, rep.String(), "execution", "attach", exe, "--name", "drift-report", "--type", "text/markdown", "--file", "-"); r.code != 0 {
			t.Fatalf("attach drift-report: exit %d\n%s%s", r.code, r.out, r.err)
		}
	}

	// Before any drift run: nothing closes.
	present(cmd, "reject", "no drift review run in progress")

	exe := d.start(d.drift, stepSeat(t))
	present(cmd, "reject", "no drift-report")
	present(bare, "reject", "no drift-key")

	// Substrate: listed, but drift-check still prints it.
	attach(exe, driftReport{checkExit: "1", substrate: []string{cmdLine}, gone: []string{commandKey + "\t" + cmd + "\tgone"}})
	present(cmd, "reject", "still found")
	// Substrate: absent, but drift-check could not check (exit 2).
	attach(exe, driftReport{checkExit: "2", gone: []string{commandKey}})
	present(cmd, "reject", "drift-check failed")
	// Substrate: absent and drift-check ran, but the report does not list it.
	attach(exe, driftReport{checkExit: "0"})
	present(cmd, "reject", "key not listed")

	// Prose and constitution: absent from the findings and listed, but no
	// RESOLVED line from this run's judges.
	attach(exe, driftReport{checkExit: "0", gone: []string{proseKey, constitutionKey}})
	present(prose, "reject", "no RESOLVED line")
	present(con, "reject", "no RESOLVED line")
	// Constitution: a RESOLVED line, but the judge still raises it.
	attach(exe, driftReport{checkExit: "0", constitution: []string{conLine}, gone: []string{constitutionKey},
		resolved: []string{"RESOLVED " + constitutionKey + " — internal/x.go:3 no longer serves REST"}})
	present(con, "reject", "still found")

	// Accepts: substrate absent with drift-check exit 1 on another finding;
	// prose and constitution each absent with its RESOLVED line.
	attach(exe, driftReport{
		checkExit: "1", substrate: []string{flagLine},
		gone: []string{commandKey + "\t" + cmd + "\tgone", proseKey + "\t" + prose + "\tgone", constitutionKey},
		resolved: []string{
			"RESOLVED " + proseKey + " — statement removed",
			"RESOLVED " + constitutionKey,
		},
	})
	present(cmd, "accept", exe)
	present(prose, "accept", "RESOLVED "+proseKey)
	present(con, "accept", "RESOLVED "+constitutionKey)
	present(flag, "reject", "key not listed")

	// Cancel: the cancel review runs on its bound reviewer; a reject keeps the
	// story open, an accept cancels it.
	d.setFake("verdict.other", "reject")
	if r := d.run("story", "set", flag, "--status", "cancelled"); r.code == 0 {
		t.Fatalf("a rejected cancel review cancelled %s:\n%s", flag, r.out)
	}
	if g := latestRun(d.sandbox, flag, "cancel-review"); g.Result != "reject" || g.Binding != "reviewer-alt" {
		t.Fatalf("cancel-review = %s by %q, want reject by reviewer-alt", g.Result, g.Binding)
	}
	if st := d.get(flag).Status; st != "open" {
		t.Fatalf("%s is %s after a rejected cancel, want open", flag, st)
	}
	d.setFake("verdict.other", "")
	if r := d.run("story", "set", flag, "--status", "cancelled"); r.code != 0 {
		t.Fatalf("an accepted cancel review did not cancel %s: exit %d\n%s%s", flag, r.code, r.out, r.err)
	}
	if g := latestRun(d.sandbox, flag, "cancel-review"); g.Result != "accept" || g.Binding != "reviewer-alt" {
		t.Fatalf("cancel-review = %s by %q, want accept by reviewer-alt", g.Result, g.Binding)
	}
}

// --keys maps each distinct finding to the drift-key the raise writes, in
// order, and refuses a line that is not a finding.
func TestDriftStoriesKeys(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	script, err := filepath.Abs("../scripts/drift-stories.sh")
	if err != nil {
		t.Fatal(err)
	}
	r := s.exec("bash", driftFindings+strings.Replace(driftFindings, "a.md:3:", "a.md:30:", 1), script, "--keys")
	if want := commandKey + "\n" + constitutionKey + "\n" + proseKey + "\n"; r.code != 0 || r.out != want {
		t.Fatalf("--keys: exit %d\n%s%s\nwant\n%s", r.code, r.out, r.err, want)
	}
	if r := s.exec("bash", "## Substrate findings\n", script, "--keys"); r.code != 2 {
		t.Fatalf("--keys on a non-finding line: exit %d, want 2\n%s%s", r.code, r.out, r.err)
	}
}
