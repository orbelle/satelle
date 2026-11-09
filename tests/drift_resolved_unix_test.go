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
// task) and cancel gate skills unchanged, and scripts/drift-fix.sh. No skill
// is stubbed, so satelle validate passes on the repo's own configuration
// before any case runs.
func newDriftStorySandbox(t *testing.T) *driftSandbox {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal("bash is required by scripts/drift-fix.sh and is not on PATH")
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
	// A stand-in for the principle it links, so validate's substrate check
	// finds the link's target.
	d.write(".satelle/principles/satelle-agent-goals.md", "---\nname: satelle-agent-goals\ntype: principle\ndescription: A stand-in.\n---\n")
	d.write(".satelle/skills/satelle-change-judged-check.md", repoText(t, ".satelle/skills/satelle-change-judged-check.md"))
	check := regexp.MustCompile(driftTaskLine).ReplaceAllString(repoText(t, ".satelle/skills/satelle-drift-resolved-check.md"), "task="+d.drift)
	if !strings.Contains(check, "\ntask="+d.drift+"\n") {
		t.Fatal("satelle-drift-resolved-check.md has no task=<id> line")
	}
	d.write(".satelle/skills/satelle-drift-resolved-check.md", check)
	d.write("scripts/drift-fix.sh", repoText(t, "scripts/drift-fix.sh"))
	d.must("validate")
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

// A drift story starts on the drift workflow, and its only way to done is the
// drift-resolved check: each blocking key carried by a fix item or resolved on
// the in-progress drift run's drift-report. Each present below leaves its
// gate.run on the story's ledger. Absence from the findings alone never closes
// a constitution key; a prose key never blocks. A cancelled present runs the
// cancel review on its bound reviewer.
func TestDriftStoryClosesOnTheRunsReport(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	cmdLine, conLine := lines[0], lines[1]
	const keyLess = "a drift story with no drift-key line"

	old := func(key, line string) string {
		t.Helper()
		return d.create("story", "--title", "Drift: "+key, "--tag", "drift", "--body", oldDriftBody(key, line, true)).ID
	}
	cmd, con, prose, flag := old(commandKey, cmdLine), old(constitutionKey, conLine), old(proseKey, lines[2]), old(flagKey, flagLine)
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

	// Before any drift run: nothing closes but a story whose only key is
	// prose, which never blocks.
	present(cmd, "reject", "no drift review run in progress")
	present(prose, "accept", "only warnings, which never block")

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

	// Constitution: absent from the findings and listed, but no RESOLVED line
	// from this run's judges.
	attach(exe, driftReport{checkExit: "0", gone: []string{proseKey, constitutionKey}})
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

// A key carried by an open fix item closes with no drift run: accepted when
// the fix item named by the drift-carried-by tag lists it; rejected naming the
// key when the fix item lacks it or is done; a keyless story is accepted when
// the fix item names it in a drift-warning line (sto_a1681009 AC3, AC4).
func TestDriftResolvedCarried(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	present := func(id, wantResult, wantNotes string) {
		t.Helper()
		d.run("story", "set", id, "--status", "done")
		g := latestRun(d.sandbox, id, "drift-resolved")
		if g.Result != wantResult || !strings.Contains(g.Notes, wantNotes) {
			t.Fatalf("%s done: drift-resolved = %s (%q), want %s naming %q", id, g.Result, g.Notes, wantResult, wantNotes)
		}
	}
	old := func(key, line, fix string) string {
		t.Helper()
		return d.create("story", "--title", "Drift: "+key, "--tag", "drift", "--tag", "drift-carried-by:"+fix, "--body", oldDriftBody(key, line, true)).ID
	}
	r := d.driftFix(lines[0]+"\n", "9.9.9")
	fix := parseFixed(t, r.out).id

	present(old(commandKey, lines[0], fix), "accept", "carried: "+commandKey+" is listed by the fix item "+fix)
	lacking := old(constitutionKey, lines[1], fix)
	present(lacking, "reject", constitutionKey)
	if g := latestRun(d.sandbox, lacking, "drift-resolved"); !strings.Contains(g.Notes, "does not list it") {
		t.Fatalf("the reject does not say the fix item lacks the key: %q", g.Notes)
	}

	bare := d.create("story", "--title", "no key", "--tag", "drift", "--tag", "drift-carried-by:"+fix, "--body", "no key here\n").ID
	present(bare, "reject", "no drift-key")
	r = d.driftFix(lines[0]+"\n"+bare+":1: prose: no key — carried from "+bare+"\n", "9.9.9")
	if f := parseFixed(t, r.out); f.id != fix || f.warnings != "1 warnings" {
		t.Fatalf("carrying the keyless story: %+v", f)
	}
	present(bare, "accept", "carried by "+fix+" as a warning")

	// A done fix item carries nothing.
	other := parseFixed(t, d.driftFix(lines[2]+"\n", "9.9.10").out).id
	byDone := old(constitutionKey, lines[1], other)
	d.must("story", "set", other, "--status", "done")
	present(byDone, "reject", "is not open on the drift workflow")
}

// Warnings never block: an item with only drift-warning lines, a fix item
// whose drift-key is resolved while a drift-warning is not, and an old story
// whose only key is a prose drift-key each reach done (sto_a1681009 AC8).
func TestDriftResolvedIgnoresWarnings(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	done := func(id, want string) {
		t.Helper()
		if r := d.run("story", "set", id, "--status", "done"); r.code != 0 {
			t.Fatalf("%s done: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
		if g := latestRun(d.sandbox, id, "drift-resolved"); g.Result != "accept" || !strings.Contains(g.Notes, want) {
			t.Fatalf("%s: drift-resolved = %s (%q), want accept naming %q", id, g.Result, g.Notes, want)
		}
	}
	// Only warnings, no drift run in progress.
	warnOnly := parseFixed(t, d.driftFix(lines[2]+"\n", "9.9.9").out).id
	done(warnOnly, "only warnings, which never block")
	// An old story whose only key is prose.
	proseStory := d.create("story", "--title", "Drift: prose", "--tag", "drift", "--body", oldDriftBody(proseKey, lines[2], true)).ID
	done(proseStory, "only warnings, which never block")

	// A fix item with a resolved drift-key and an unresolved drift-warning.
	fix := parseFixed(t, d.driftFix(lines[0]+"\n"+lines[2]+"\n", "9.9.10").out).id
	exe := d.start(d.drift, stepSeat(t))
	rep := driftReport{checkExit: "0", gone: []string{commandKey + "\t" + fix}}
	if r := d.exec(satelleBin, rep.String(), "execution", "attach", exe, "--name", "drift-report", "--type", "text/markdown", "--file", "-"); r.code != 0 {
		t.Fatalf("attach drift-report: exit %d\n%s%s", r.code, r.out, r.err)
	}
	done(fix, commandKey+" is no longer found by the drift run "+exe)
}
