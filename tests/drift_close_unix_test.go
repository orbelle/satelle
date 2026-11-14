//go:build integration && unix

package tests

import (
	"os"
	"path/filepath"
	"slices"
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

// The drift review carries or closes every older drift item. A v0.0.1 fix
// item holds a command key the run no longer finds (fixed), a flag key it
// still finds, and a constitution key no judge resolved. drift-review.md's
// steps 2, 3 and 4 run verbatim: step 2 raises the v0.0.2 fix item carrying
// the flag and constitution keys; step 3's report lists the command key under
// No longer found and names the fix item; step 4 closes the v0.0.1 item (the
// command key resolved on the report, the other two carried), tagged
// drift-carried-by:<v0.0.2 id>, and lists the v0.0.2 item as stayed open. The
// run's done move checks those lists against the kernel (sto_a1681009 AC3).
func TestDriftReviewClosesWhatTheRunNoLongerFinds(t *testing.T) {
	t.Parallel()
	d := newDriftCloseSandbox(t)
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	cmdLine, conLine := lines[0], lines[1]
	r := d.driftFix(cmdLine+"\n"+flagLine+"\n"+conLine+"\n", "0.0.1")
	if r.code != 0 {
		t.Fatalf("drift-fix.sh 0.0.1: exit %d\n%s%s", r.code, r.out, r.err)
	}
	older := parseFixed(t, r.out).id

	exe := d.start(d.drift, stepSeat(t))
	// Step 1 as drift-check reports it: the flag finding is still there, the
	// command finding is gone.
	d.substrate(exe, flagLine+"\n", "1")
	out := d.step("## 2.")
	fix := parseFixed(t, out.out).id
	if fix == older {
		t.Fatalf("step 2 rewrote the v0.0.1 item %s instead of raising v0.0.2's", older)
	}
	it := d.get(fix)
	if !slices.Contains(it.Tags, "release:v0.0.2") || !slices.Contains(it.Tags, "drift-fix") {
		t.Fatalf("step 2's item %s tags %v, want drift-fix and release:v0.0.2", fix, it.Tags)
	}
	if got := bodyLines(it.Body, "drift-key: "); !slices.Equal(got, []string{flagKey, constitutionKey}) {
		t.Fatalf("v0.0.2's fix item keys %q, want the flag key and the carried constitution key:\n%s", got, it.Body)
	}

	d.step("## 3.")
	rep := d.report(exe)
	gone := listed(rep, "No longer found")
	if len(gone) != 1 || gone[commandKey] == nil || gone[commandKey][0] != older {
		t.Fatalf("step 3's No longer found = %v, want only %s of %s:\n%s", gone, commandKey, older, rep)
	}
	if fi := reportSection(rep, "Fix item"); len(fi) != 3 || fi[0][0] != fix {
		t.Fatalf("step 3's Fix item = %v, want %s and its two keys:\n%s", fi, fix, rep)
	}
	if !strings.Contains(rep, "- scripts/drift-check.sh: drift-check exit 1\n") {
		t.Fatalf("step 3's report has no drift-check exit line:\n%s", rep)
	}
	if runs := d.gateRuns(older); len(runs) != 0 {
		t.Fatalf("%s was presented before step 4: %v", older, runs)
	}

	d.step("## 4.")
	g := latestRun(d.sandbox, older, "drift-resolved")
	if g.Result != "accept" || !strings.Contains(g.Notes, commandKey+" is no longer found by the drift run "+exe) ||
		!strings.Contains(g.Notes, "carried: "+flagKey) || !strings.Contains(g.Notes, "carried: "+constitutionKey) {
		t.Fatalf("v0.0.1's drift-resolved = %s (%q), want accept: the command key resolved, the others carried", g.Result, g.Notes)
	}
	if it := d.get(older); it.Status != "done" || !slices.Contains(it.Tags, "drift-carried-by:"+fix) {
		t.Fatalf("v0.0.1's item is %s with tags %v, want done tagged drift-carried-by:%s", it.Status, it.Tags, fix)
	}
	rep = d.report(exe)
	closed, stayed := listed(rep, "Closed"), listed(rep, "Stayed open")
	if len(closed) != 1 || closed[older] == nil || closed[older][0] != commandKey {
		t.Fatalf("Closed = %v, want only %s:\n%s", closed, older, rep)
	}
	if len(stayed) != 1 || len(stayed[fix]) != 2 || stayed[fix][1] != "the fix item of v0.0.2" {
		t.Fatalf("Stayed open = %v, want only %s, the fix item of v0.0.2:\n%s", stayed, fix, rep)
	}

	// The run's done move checks the lists against the kernel.
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
	good := rep
	stayedLine := fix + "\t" + flagKey + "\tthe fix item of v0.0.2\n"
	omitting := strings.Replace(good, stayedLine, "", 1)
	if omitting == good {
		t.Fatalf("the report has no stayed-open line %q:\n%s", stayedLine, good)
	}
	attach(omitting)
	presentRun("reject", "missing from Stayed open: the open drift item "+fix)
	attach(good)
	d.citeReport(exe)
	presentRun("accept", "1 closed and done, 1 open drift items")
	if st := d.status(exe); st != "done" {
		t.Fatalf("run is %s after the correct report, want done", st)
	}
}

// The claims judge's findings are warnings: it accepts with a WARN line, step
// 5 brings the run to done, and step 6 records the warning in the release's
// fix item as a drift-warning line with the finding quoted (sto_a1681009 AC7).
func TestDriftReviewRecordsWarnings(t *testing.T) {
	t.Parallel()
	d := newDriftCloseSandbox(t)
	exe := d.start(d.drift, stepSeat(t))
	d.substrate(exe, "", "0")
	d.step("## 2.")
	d.step("## 3.")
	d.step("## 4.")
	d.citeReport(exe)
	const warn = `WARN .satelle/principles/p.md:6 "queue entries live in the story chain" — internal/kernel/queue.go:62`
	d.setFake("notes.claims", "checked .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/: 1 contradicted statements\n"+warn)
	d.step("## 5.")
	if g := latestRun(d.sandbox, exe, "drift-claims"); g.Result != "accept" || !strings.Contains(g.Notes, warn) {
		t.Fatalf("drift-claims = %s (%q), want accept with the WARN line", g.Result, g.Notes)
	}
	if st := d.status(exe); st != "done" {
		t.Fatalf("run is %s, want done: a warning never blocks", st)
	}
	out := d.step("## 6.")
	fix := parseFixed(t, out.out)
	if fix.findings != "0 findings" || fix.warnings != "1 warnings" {
		t.Fatalf("step 6 = %+v, want one warning", fix)
	}
	body := d.get(fix.id).Body
	want := "drift-warning: " + proseKey + "\n> .satelle/principles/p.md:6: prose: queue entries live in the story chain — internal/kernel/queue.go:62\n"
	if !strings.Contains(body, want) || strings.Contains(body, "drift-key: prose:") {
		t.Fatalf("the fix item lacks %q, or has a prose drift-key:\n%s", want, body)
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
	for _, want := range []string{`bash scripts/drift-fix.sh --close "$fix"`, "## Stayed open"} {
		if !strings.Contains(four, want) {
			t.Errorf("drift-review.md step 4 lacks %q", want)
		}
	}
}
