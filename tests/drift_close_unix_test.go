//go:build integration && unix

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const driftReview = ".satelle/skills/drift-review.md"

// driftCloseSandbox is a drift story sandbox whose run-local directory
// (drift-review.md's rd) lives under a test temp dir.
type driftCloseSandbox struct {
	*driftSandbox
	tmp string
}

func newDriftCloseSandbox(t *testing.T) *driftCloseSandbox {
	t.Helper()
	d := newDriftStorySandbox(t)
	tmp := t.TempDir()
	d.env["TMPDIR"] = tmp
	return &driftCloseSandbox{driftSandbox: d, tmp: tmp}
}

// rd is drift-review.md's run-local directory of exe.
func (d *driftCloseSandbox) rd(exe string) string {
	return filepath.Join(d.tmp, "satelle-drift-"+exe)
}

// substrate writes step 1's output as drift-check would: its findings and
// exit line.
func (d *driftCloseSandbox) substrate(exe, findings, exit string) {
	d.t.Helper()
	if err := os.MkdirAll(d.rd(exe), 0o755); err != nil {
		d.t.Fatal(err)
	}
	writeFile(d.t, filepath.Join(d.rd(exe), "substrate.txt"), findings, 0o644)
	writeFile(d.t, filepath.Join(d.rd(exe), "substrate.exit"), "drift-check exit "+exit+"\n", 0o644)
}

// step runs drift-review.md's fence under heading verbatim, its task= line
// bound to the sandbox's drift task.
func (d *driftCloseSandbox) step(heading string) result {
	d.t.Helper()
	r := d.exec("bash", "", "-c", shBlock(d.t, driftReview, heading, d.drift))
	if r.code != 0 {
		d.t.Fatalf("drift-review %s: exit %d\n%s%s", heading, r.code, r.out, r.err)
	}
	return r
}

// report is the run's attached drift-report.
func (d *driftCloseSandbox) report(exe string) string {
	d.t.Helper()
	return d.must("execution", "attachment", exe, "drift-report")
}

// reportSection returns a report's lines under "## name", each split on tabs.
func reportSection(report, name string) [][]string {
	var out [][]string
	on := false
	for _, l := range strings.Split(report, "\n") {
		if strings.HasPrefix(l, "## ") {
			on = l == "## "+name
			continue
		}
		if on && strings.TrimSpace(l) != "" {
			out = append(out, strings.Split(l, "\t"))
		}
	}
	return out
}

// listed maps the first field of each section line to the rest.
func listed(report, name string) map[string][]string {
	m := map[string][]string{}
	for _, f := range reportSection(report, name) {
		m[f[0]] = f[1:]
	}
	return m
}

// raiseDrift raises one drift story per finding with scripts/drift-stories.sh
// and returns key -> story id.
func (d *driftCloseSandbox) raiseDrift(findings string) map[string]string {
	d.t.Helper()
	r := d.exec("bash", findings, "scripts/drift-stories.sh", "0.0.2")
	if r.code != 0 {
		d.t.Fatalf("drift-stories.sh: exit %d\n%s%s", r.code, r.out, r.err)
	}
	ids := map[string]string{}
	for key, f := range raised(d.t, r.out) {
		ids[key] = f[0]
	}
	return ids
}

// The drift review closes what the run no longer finds. drift-review.md's
// step 3 and step 4 fences run verbatim, back to back, over three drift
// stories: one substrate finding the run no longer finds (fixed), one it still
// finds, and one prose finding the claims judge has not yet resolved. Step 3's
// report carries the sections the drift route's done gate (D1) reads, and the
// gate closes the fixed story on it; the others stay open, each with its
// reason. When the claims judge then states a RESOLVED line for the prose
// finding, steps 3 and 4 again close it too. The run's done move checks the
// closed and stayed-open lists against the kernel: a report omitting an open
// story, or claiming a close that did not happen, is refused, and the correct
// report is accepted.
func TestDriftReviewClosesWhatTheRunNoLongerFinds(t *testing.T) {
	t.Parallel()
	d := newDriftCloseSandbox(t)
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	cmdLine, proseLine := lines[0], lines[2]
	const flagLine = `.satelle/skills/b.md:4: flag: satelle story list --nope — no such flag`
	const flagKey = "flag:.satelle/skills/b.md:satelle story list --nope"
	ids := d.raiseDrift(cmdLine + "\n" + flagLine + "\n" + proseLine + "\n")
	fixed, present, prose := ids[commandKey], ids[flagKey], ids[proseKey]
	if fixed == "" || present == "" || prose == "" {
		t.Fatalf("drift-stories.sh did not raise all three stories: %v", ids)
	}

	exe := d.start(d.drift, stepSeat(t))
	// Step 1 as drift-check reports it: the flag finding is still there, the
	// command finding is gone; the claims judge carried the prose finding.
	d.substrate(exe, flagLine+"\n", "1")
	d.step("## 2.")
	d.step("## 3.")

	// Step 3's report, as attached before step 4 presents anything, carries
	// the sections D1's gate reads.
	rep := d.report(exe)
	gone := listed(rep, "No longer found")
	if len(gone) != 2 || gone[commandKey] == nil || gone[commandKey][0] != fixed || gone[proseKey] == nil || gone[flagKey] != nil {
		t.Fatalf("step 3's No longer found = %v, want %s and %s, not %s:\n%s", gone, commandKey, proseKey, flagKey, rep)
	}
	if r := reportSection(rep, "Resolved by the judges"); len(r) != 0 {
		t.Fatalf("step 3 listed RESOLVED lines no judge stated: %v", r)
	}
	if !strings.Contains(rep, "- scripts/drift-check.sh: drift-check exit 1\n") {
		t.Fatalf("step 3's report has no drift-check exit line:\n%s", rep)
	}
	for _, id := range []string{fixed, present, prose} {
		if runs := d.gateRuns(id); len(runs) != 0 {
			t.Fatalf("%s was presented before step 4: %v", id, runs)
		}
	}

	d.step("## 4.")
	if g := latestRun(d.sandbox, fixed, "drift-resolved"); g.Result != "accept" || !strings.Contains(g.Notes, exe) {
		t.Fatalf("the fixed story's drift-resolved = %s (%q), want accept on run %s", g.Result, g.Notes, exe)
	}
	if st := d.get(fixed).Status; st != "done" {
		t.Fatalf("the fixed story %s is %s, want done", fixed, st)
	}
	if g := latestRun(d.sandbox, prose, "drift-resolved"); g.Result != "reject" || !strings.Contains(g.Notes, "no RESOLVED line") {
		t.Fatalf("the prose story's drift-resolved = %s (%q), want reject: no RESOLVED line", g.Result, g.Notes)
	}
	if runs := d.gateRuns(present); len(runs) != 0 {
		t.Fatalf("the still-found story %s was presented: %v", present, runs)
	}
	for _, id := range []string{present, prose} {
		if st := d.get(id).Status; st != "open" {
			t.Fatalf("%s is %s, want open", id, st)
		}
	}
	rep = d.report(exe)
	closed, stayed := listed(rep, "Closed"), listed(rep, "Stayed open")
	if len(closed) != 1 || closed[fixed] == nil || closed[fixed][0] != commandKey {
		t.Fatalf("Closed = %v, want only %s:\n%s", closed, fixed, rep)
	}
	if len(stayed) != 2 || len(stayed[present]) != 2 || stayed[present][1] != "still found by this run" ||
		len(stayed[prose]) != 2 || !strings.Contains(stayed[prose][1], "no RESOLVED line") {
		t.Fatalf("Stayed open = %v, want %s still found and %s refused for no RESOLVED line:\n%s", stayed, present, prose, rep)
	}

	// The claims judge re-checks the prose story and finds it fixed: its
	// RESOLVED line rejects the run's done move, so the run stays in progress.
	resolved := "RESOLVED " + proseKey + " — the statement is gone from .satelle/principles/p.md"
	d.setFake("verdict.claims", "reject")
	d.setFake("notes.claims", resolved)
	d.step("## 5.")
	if g := latestRun(d.sandbox, exe, "drift-claims"); g.Result != "reject" || !strings.Contains(g.Notes, resolved) {
		t.Fatalf("drift-claims = %s (%q), want reject stating %q", g.Result, g.Notes, resolved)
	}
	if st := d.status(exe); st != "in_progress" {
		t.Fatalf("run is %s after the RESOLVED reject, want in_progress", st)
	}
	sys, err := os.ReadFile(filepath.Join(d.fake, "system.claims"))
	if err != nil || !strings.Contains(string(sys), "RESOLVED <drift-key> — <evidence>") {
		t.Fatalf("the claims judge's prompt does not carry the RESOLVED form (%v)", err)
	}

	d.step("## 3.")
	if r := reportSection(d.report(exe), "Resolved by the judges"); len(r) != 1 || r[0][0] != resolved {
		t.Fatalf("step 3's Resolved by the judges = %v, want %q", r, resolved)
	}
	d.step("## 4.")
	if g := latestRun(d.sandbox, prose, "drift-resolved"); g.Result != "accept" || !strings.Contains(g.Notes, "RESOLVED "+proseKey) {
		t.Fatalf("the prose story's drift-resolved = %s (%q), want accept on its RESOLVED line", g.Result, g.Notes)
	}
	if st := d.get(prose).Status; st != "done" {
		t.Fatalf("the prose story %s is %s, want done", prose, st)
	}
	good := d.report(exe)
	closed, stayed = listed(good, "Closed"), listed(good, "Stayed open")
	if len(closed) != 2 || closed[fixed] == nil || closed[prose] == nil || len(stayed) != 1 || stayed[present] == nil {
		t.Fatalf("after the RESOLVED line: Closed %v, Stayed open %v; want %s and %s closed, %s open:\n%s", closed, stayed, fixed, prose, present, good)
	}

	// The run's done move checks the lists against the kernel.
	d.setFake("verdict.claims", "")
	d.setFake("notes.claims", "")
	attach := func(body string) {
		t.Helper()
		if r := d.exec(satelleBin, body, "execution", "attach", exe, "--name", "drift-report", "--type", "text/markdown", "--file", "-"); r.code != 0 {
			t.Fatalf("attach drift-report: exit %d\n%s%s", r.code, r.out, r.err)
		}
	}
	presentRun := func(wantResult, wantNotes string) {
		t.Helper()
		d.run("execution", "set", exe, "--status", "done")
		g := latestRun(d.sandbox, exe, "drift-closures")
		if g.Result != wantResult || !strings.Contains(g.Notes, wantNotes) {
			t.Fatalf("drift-closures = %s (%q), want %s naming %q", g.Result, g.Notes, wantResult, wantNotes)
		}
	}
	stayedLine := present + "\t" + flagKey + "\tstill found by this run\n"
	omitting := strings.Replace(good, stayedLine, "", 1)
	if omitting == good {
		t.Fatalf("the report has no stayed-open line %q:\n%s", stayedLine, good)
	}
	attach(omitting)
	presentRun("reject", "missing from Stayed open: the open drift story "+present)
	if st := d.status(exe); st != "in_progress" {
		t.Fatalf("run is %s after a report omitting %s, want in_progress", st, present)
	}
	attach(strings.Replace(good, "## Closed\n", "## Closed\n"+present+"\t"+flagKey+"\n", 1))
	presentRun("reject", "listed closed but not done: "+present+" is open")

	attach(good)
	presentRun("accept", "2 closed and done, 1 open drift stories")
	if st := d.status(exe); st != "done" {
		t.Fatalf("run is %s after the correct report, want done", st)
	}
}

// Closing presents only stories already on the drift route. A drift story on
// another workflow whose finding the run no longer finds is listed under No
// longer found but never presented: it stays open with the reason that it is
// not on the drift route. A drift story with no drift-key line stays open with
// that reason.
func TestDriftClosingSkipsStoriesOffTheDriftRoute(t *testing.T) {
	t.Parallel()
	d := newDriftCloseSandbox(t)
	// A chore-like route whose done gate records a refusal, as chore's and
	// delivery's release gates would for a record with no change.
	d.write(".satelle/workflows/chore.toml", `name = "chore"
[states.open]
start = true
[states.done]
terminal = true
[gates.released]
skill = "fixture-refuse"
[[transition]]
from = ["open"]
to = "done"
groups = [{ mode = "sequential", gates = ["released"] }]
`)
	d.write(".satelle/skills/fixture-refuse.md", "---\nname: fixture-refuse\ntype: check\n---\n\n```sh\necho \"no release carries this record\"\nexit 1\n```\n")
	d.must("validate")
	d.git("add", "-A")
	d.git("commit", "-q", "-m", "chore route")
	const offKey = "command:.satelle/skills/c.md:satelle story gone"
	off := d.create("story", "--title", "Drift: satelle story gone (.satelle/skills/c.md)", "--tag", "drift",
		"--workflow", "chore", "--body", "drift-key: "+offKey+"\n").ID
	bare := d.create("story", "--title", "a drift story with no drift-key line", "--tag", "drift", "--body", "no key here\n").ID
	if it := d.get(off); it.Workflow != "chore" {
		t.Fatalf("%s is on %s, want chore", off, it.Workflow)
	}
	if it := d.get(bare); it.Workflow != "drift" {
		t.Fatalf("%s is on %s, want drift", bare, it.Workflow)
	}

	exe := d.start(d.drift, stepSeat(t))
	d.substrate(exe, "", "0")
	d.step("## 3.")
	if gone := listed(d.report(exe), "No longer found"); len(gone) != 1 || gone[offKey] == nil {
		t.Fatalf("No longer found = %v, want only %s:\n%s", gone, offKey, d.report(exe))
	}
	before := d.get(off).Status
	d.step("## 4.")
	if runs := d.gateRuns(off); len(runs) != 0 {
		t.Fatalf("the story off the drift route %s was presented: %v", off, runs)
	}
	if runs := d.gateRuns(bare); len(runs) != 0 {
		t.Fatalf("the keyless story %s was presented: %v", bare, runs)
	}
	if st := d.get(off).Status; st != before {
		t.Fatalf("%s moved from %s to %s", off, before, st)
	}
	rep := d.report(exe)
	if c := reportSection(rep, "Closed"); len(c) != 0 {
		t.Fatalf("Closed = %v, want none:\n%s", c, rep)
	}
	stayed := listed(rep, "Stayed open")
	if len(stayed[off]) != 2 || stayed[off][0] != offKey || !strings.Contains(stayed[off][1], "on the chore workflow, not the drift route") {
		t.Fatalf("Stayed open for %s = %v, want the reason it is not on the drift route:\n%s", off, stayed[off], rep)
	}
	if len(stayed[bare]) != 2 || stayed[bare][0] != "-" || !strings.Contains(stayed[bare][1], "no drift-key line") {
		t.Fatalf("Stayed open for %s = %v, want the reason it has no drift-key line:\n%s", bare, stayed[bare], rep)
	}
}

// drift-constitution runs when a release edits only a principle (that is how a
// constitution finding gets fixed) and skips a release that edits only
// .version; drift-claims runs on both. The constitution judge's prompt carries
// the principle hunk and the RESOLVED form.
func TestDriftConstitutionRunsOnAPrincipleEdit(t *testing.T) {
	t.Parallel()
	const unchanged = "package x\n\nfunc Foo() {}\n" // internal/x.go as v0.0.1 has it
	cases := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"principle only", map[string]string{"internal/x.go": unchanged, ".satelle/principles/p.md": "# P\n\nQueue entries live in the queue file.\n"}, "accept"},
		{"constitution only", map[string]string{"internal/x.go": unchanged, ".satelle/constitution.md": "# Constitution\n\nAmended.\n"}, "accept"},
		{".version only", map[string]string{"internal/x.go": unchanged}, "skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := newDriftSandbox(t, c.extra)
			exe := d.start(d.drift, stepSeat(t))
			d.run("execution", "set", exe, "--status", "done")
			g := latestRun(d.sandbox, exe, "drift-constitution")
			if g.Result != c.want {
				t.Fatalf("drift-constitution = %s (%q), want %s", g.Result, g.Notes, c.want)
			}
			if c.want == "skipped" && !strings.Contains(g.Notes, "no changed path matches") {
				t.Fatalf("drift-constitution skipped for %q, want no changed path matches", g.Notes)
			}
			if g := latestRun(d.sandbox, exe, "drift-claims"); g.Result != "accept" {
				t.Fatalf("drift-claims = %s (%q), want accept", g.Result, g.Notes)
			}
			ps := prompts(t, d.fake, "constitution")
			if c.want == "skipped" {
				if len(ps) != 0 {
					t.Fatalf("a .version-only release dispatched the constitution judge %d time(s)", len(ps))
				}
				return
			}
			var edited string
			for p := range c.extra {
				if p != "internal/x.go" {
					edited = p
				}
			}
			if len(ps) != 1 || !strings.Contains(ps[0], edited) || strings.Contains(ps[0], "satelle.version: 0.0.2") {
				t.Fatalf("the constitution prompt does not carry the %s hunk alone:\n%s", edited, strings.Join(ps, "\n----\n"))
			}
			sys, err := os.ReadFile(filepath.Join(d.fake, "system.constitution"))
			if err != nil || !strings.Contains(string(sys), "RESOLVED <drift-key> — <evidence>") {
				t.Fatalf("the constitution judge's prompt does not carry the RESOLVED form (%v)", err)
			}
		})
	}
}

// drift-review.md's step 4 is its own fence, straight after step 3's: between
// step 3's closing fence and step 4's opening fence stands only step 4's
// heading, so no prose instruction between them is load-bearing.
func TestDriftReviewStep4FollowsStep3Fence(t *testing.T) {
	md := repoText(t, driftReview)
	i := strings.Index(md, "\n## 3.")
	j := strings.Index(md, "\n## 4.")
	k := strings.Index(md, "\n## 5.")
	if i < 0 || j < i || k < j {
		t.Fatalf("drift-review.md: want ## 3., ## 4. and ## 5. in order (at %d, %d, %d)", i, j, k)
	}
	three := md[i:j]
	if strings.Count(three, "```sh\n") != 1 || !strings.HasSuffix(strings.TrimRight(three, "\n"), "```") {
		t.Fatalf("drift-review.md step 3 does not end with its one sh fence:\n%s", three)
	}
	head, _, ok := strings.Cut(md[j+1:], "```sh\n")
	if !ok || strings.Count(strings.TrimSpace(head), "\n") != 0 {
		t.Fatalf("drift-review.md: step 4's fence does not follow its heading directly:\n%s", head)
	}
	four := md[j:k]
	for _, want := range []string{"satelle story set \"$id\" --status done", "[ \"$wf\" = drift ] || continue"} {
		if !strings.Contains(four, want) {
			t.Errorf("drift-review.md step 4 lacks %q", want)
		}
	}
}
