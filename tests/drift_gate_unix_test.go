//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The drift review's task id as the repo's substrate writes it, and the files
// that carry it on a literal task= line (release.md in its step 11).
const driftTaskLine = `(?m)^task=(tas_[A-Za-z0-9_]+)$`

var driftTaskFiles = []string{
	".satelle/skills/release.md",
	".satelle/skills/drift-review.md",
	".satelle/skills/satelle-drift-run-trigger.md",
}

// repoText reads a file of this repo.
func repoText(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// driftTaskID is the one task id release.md, drift-review.md and the trigger
// skill name.
func driftTaskID(t *testing.T) string {
	t.Helper()
	re := regexp.MustCompile(driftTaskLine)
	id := ""
	for _, f := range driftTaskFiles {
		text := repoText(t, f)
		if f == ".satelle/skills/release.md" {
			text = releaseStep(t, "## 11.") // step 0 names the release task
		}
		ms := re.FindAllStringSubmatch(text, -1)
		if len(ms) == 0 {
			t.Fatalf("%s has no task=<id> line", f)
		}
		for _, m := range ms {
			if id == "" {
				id = m[1]
			}
			if m[1] != id {
				t.Errorf("%s names task %s, another file names %s", f, m[1], id)
			}
		}
	}
	return id
}

// releaseStep returns release.md's section under the heading that starts
// with prefix, up to the next ## heading.
func releaseStep(t *testing.T, prefix string) string {
	t.Helper()
	md := repoText(t, ".satelle/skills/release.md")
	i := strings.Index(md, "\n"+prefix)
	if i < 0 {
		t.Fatalf("release.md has no %q heading", prefix)
	}
	sec := md[i+1:]
	if j := strings.Index(sec[3:], "\n## "); j >= 0 {
		sec = sec[:j+3]
	}
	return sec
}

// lineWith returns the first line of text that starts with prefix.
func lineWith(t *testing.T, text, prefix, where string) string {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("%s has no line starting %q", where, prefix)
	return ""
}

// A release ends with the drift review: release.md step 11 follows step 10,
// starts the drift task with the previous tag as its base and seats on the
// same base; drift-review.md re-seats on the run's base; one task id in all
// three files; and the epic drive runs a release through step 11.
func TestReleaseRunsDriftReviewAfterCleanup(t *testing.T) {
	id := driftTaskID(t)
	md := repoText(t, ".satelle/skills/release.md")
	ten, eleven := strings.Index(md, "\n## 10."), strings.Index(md, "\n## 11.")
	if ten < 0 || eleven < 0 || eleven < ten {
		t.Fatalf("release.md: ## 11. must follow ## 10. (at %d and %d)", ten, eleven)
	}
	step := releaseStep(t, "## 11.")
	for _, want := range []string{`satelle task run "$task" --base "v$prev"`, "drift-review.md", "task=" + id} {
		if !strings.Contains(step, want) {
			t.Errorf("release.md step 11 lacks %q", want)
		}
	}
	if seat := lineWith(t, step, "satelle execution seat", "release.md step 11"); !strings.Contains(seat, `--base "v$prev"`) {
		t.Errorf("release.md step 11 seats without --base \"v$prev\": %s", seat)
	}
	review := repoText(t, ".satelle/skills/drift-review.md")
	_, step0, _ := strings.Cut(review, "## 0.")
	step0, _, _ = strings.Cut(step0, "\n## 1.")
	if seat := lineWith(t, step0, "satelle execution seat", "drift-review.md step 0"); !strings.Contains(seat, `--base "$base"`) {
		t.Errorf("drift-review.md step 0 seats without --base \"$base\": %s", seat)
	}

	goals := repoText(t, ".satelle/principles/satelle-agent-goals.md")
	if !strings.Contains(goals, "through step 11") {
		t.Error("satelle-agent-goals.md: the epic drive does not run release.md through step 11")
	}
	if strings.Contains(goals, "through step 10") {
		t.Error("satelle-agent-goals.md still runs release.md only through step 10")
	}
}

// fakeDriftJudge puts a fake `claude` first on PATH that tells the two drift
// judges and any other judge apart by their system prompt: "constitution"
// (it defines the DRIFT line), "claims" (the CLAIM line), else "other". Each
// records its prompt in prompt.<gate>.<pid> and answers with verdict.<gate>
// (default accept) and notes.<gate>.
func fakeDriftJudge(t *testing.T, s *sandbox) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), `#!/bin/sh
sys=; prev=
for a in "$@"; do [ "$prev" = "--append-system-prompt" ] && sys=$a; prev=$a; done
g=other
case $sys in
  *'DRIFT <principle>'*) g=constitution ;;
  *'CLAIM <file>'*) g=claims ;;
esac
cat > "$FAKE_DIR/prompt.$g.$$"
printf '%s' "$sys" > "$FAKE_DIR/system.$g"
v=accept; [ -f "$FAKE_DIR/verdict.$g" ] && v=$(cat "$FAKE_DIR/verdict.$g")
n="$v by $g"; [ -f "$FAKE_DIR/notes.$g" ] && n=$(cat "$FAKE_DIR/notes.$g")
inner=$(jq -cn --arg v "$v" --arg n "$n" '{verdict:$v,notes:$n}')
jq -cn --arg r "$inner" '{type:"result",subtype:"success",is_error:false,result:$r,usage:{input_tokens:3,output_tokens:2}}'
`, 0o755)
	s.path = append(s.path, dir)
	s.env["FAKE_DIR"] = dir
	return dir
}

// prompts returns and removes the recorded prompts of gate.
func prompts(t *testing.T, dir, gate string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "prompt."+gate+".*"))
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
		os.Remove(f)
	}
	return out
}

// driftSandbox is a repo carrying this repo's runbook and drift skills, a
// drift task and another task, and two releases: v0.0.1 and v0.0.2 (HEAD),
// which changes internal/x.go and .version (plus extra, path to body).
type driftSandbox struct {
	*sandbox
	fake         string
	drift, other string // task ids
}

func newDriftSandbox(t *testing.T, extra map[string]string) *driftSandbox {
	t.Helper()
	for _, tool := range []string{"sh", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the drift skills and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	d := &driftSandbox{sandbox: s}
	d.fake = fakeDriftJudge(t, s)
	s.env["SATELLE_SESSION"] = "drift-it"
	s.git("branch", "-M", "main")
	s.must("init")
	s.write(".satelle/satelle.toml", `[workflows]
default = "standing"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
[[workflows.rule]]
workflow = "standing"
kind = "task"
[rules]
reviewer_differs_from_performer = true
`)
	s.write(".satelle/workflows/standing.toml", `name = "standing"
[states.defined]
start = true
[states.retired]
terminal = true
[[transition]]
from = ["defined"]
to = "retired"
`)
	s.write(".satelle/agents.toml", `[agents.executor]
command = "session"
[agents.reviewer]
command = "claude"
model = "rv-1"
tools = ["Read", "Grep", "Glob"]
[agents.reviewer-alt]
command = "claude"
model = "ra-1"
tools = ["Read", "Grep", "Glob"]
`)
	s.write(".satelle/workflows/runbook.toml", repoText(t, ".satelle/workflows/runbook.toml"))
	for _, name := range []string{"task-run", "satelle-task-validate-before-review", "satelle-task-validate-after-review",
		"satelle-drift-constitution-review", "satelle-drift-claims-review", "satelle-drift-diff-covered"} {
		s.write(".satelle/skills/"+name+".md", repoText(t, ".satelle/skills/"+name+".md"))
	}
	// The trigger as the repo has it until the drift task exists; then its id.
	s.write(".satelle/skills/satelle-drift-run-trigger.md", repoText(t, ".satelle/skills/satelle-drift-run-trigger.md"))
	d.drift = s.create("task", "--title", "Drift review after a release", "--input-kind", "story", "--input-tag", "drift").ID
	d.other = s.create("task", "--title", "Remove finished worktrees", "--input-kind", "story").ID
	trigger := regexp.MustCompile(driftTaskLine).ReplaceAllString(repoText(t, ".satelle/skills/satelle-drift-run-trigger.md"), "task="+d.drift)
	if !strings.Contains(trigger, "\ntask="+d.drift+"\n") {
		t.Fatal("satelle-drift-run-trigger.md has no task=<id> line")
	}
	s.write(".satelle/skills/satelle-drift-run-trigger.md", trigger)
	s.must("validate")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")

	s.write("internal/x.go", "package x\n\nfunc Foo() {}\n")
	s.write(".version", "satelle.version: 0.0.1\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "v0.0.1")
	s.git("tag", "v0.0.1")
	s.write("internal/x.go", "package x\n\nfunc Foo() { serveREST() }\n\nfunc serveREST() {}\n")
	s.write(".version", "satelle.version: 0.0.2\n")
	for p, body := range extra {
		s.write(p, body)
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "v0.0.2")
	s.git("tag", "v0.0.2")
	return d
}

// start runs task with base v0.0.1, moves its run to in_progress and seats it
// with seat, a shell line run with exe and prev in its environment.
func (d *driftSandbox) start(task, seat string) string {
	d.t.Helper()
	var run struct {
		ID string `json:"id"`
	}
	d.mustJSON(&run, "task", "run", task, "--base", "v0.0.1")
	d.must("execution", "set", run.ID, "--status", "in_progress")
	d.env["exe"], d.env["prev"] = run.ID, "0.0.1"
	r := d.exec("sh", "", "-c", seat)
	delete(d.env, "exe")
	delete(d.env, "prev")
	if r.code != 0 {
		d.t.Fatalf("seat line %q: exit %d\n%s%s", seat, r.code, r.out, r.err)
	}
	return run.ID
}

// stepSeat is release.md step 11's seat line, run as the procedure runs it.
func stepSeat(t *testing.T) string {
	return lineWith(t, releaseStep(t, "## 11."), "satelle execution seat", "release.md step 11")
}

// assertJudgedFromPrev asserts the run's diff is judged from v0.0.1 and
// holds internal/x.go.
func (d *driftSandbox) assertJudgedFromPrev(exe string) {
	d.t.Helper()
	var diff struct {
		Base  string   `json:"base"`
		Paths []string `json:"paths"`
	}
	d.mustJSON(&diff, "execution", "diff", exe)
	if want := strings.TrimSpace(d.git("rev-parse", "v0.0.1^{commit}")); diff.Base != want || !slices.Contains(diff.Paths, "internal/x.go") {
		d.t.Fatalf("run %s is judged from %s over %v; want base %s (v0.0.1) with internal/x.go", exe, diff.Base, diff.Paths, want)
	}
}

func (d *driftSandbox) status(exe string) string {
	d.t.Helper()
	var it struct {
		Status string `json:"status"`
	}
	d.mustJSON(&it, "execution", "get", exe)
	return it.Status
}

func (d *driftSandbox) setFake(name, body string) {
	d.t.Helper()
	p := filepath.Join(d.fake, name)
	if body == "" {
		os.Remove(p)
		return
	}
	writeFile(d.t, p, body, 0o644)
}

// The drift-constitution gate judges only drift runs: another task's run over
// the same diff skips it by the trigger; a drift run seated by release.md
// step 11 is judged by the reviewer binding over its internal/ diff with the
// skill's pointers, and a DRIFT reject keeps it in_progress. A run seated
// without --base has an empty diff and skips the gate on paths.
func TestDriftGateJudgesOnlyDriftRuns(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	seat := stepSeat(t)
	drift := d.start(d.drift, seat)
	other := d.start(d.other, seat)
	d.assertJudgedFromPrev(drift)
	d.assertJudgedFromPrev(other)
	prompts(t, d.fake, "constitution")

	d.must("execution", "set", other, "--status", "done")
	g := latestRun(d.sandbox, other, "drift-constitution")
	if g.Result != "skipped" || !strings.Contains(g.Notes, "trigger satelle-drift-run-trigger") || strings.Contains(g.Notes, "no changed path") {
		t.Fatalf("other run's drift-constitution = %s (%q), want skipped by the trigger", g.Result, g.Notes)
	}
	if ps := prompts(t, d.fake, "constitution"); len(ps) != 0 {
		t.Fatalf("the other run dispatched the constitution judge %d time(s)", len(ps))
	}

	const drifted = "DRIFT satelle-operator-rulings internal/x.go:3 Foo — REST handler"
	d.setFake("verdict.constitution", "reject")
	d.setFake("notes.constitution", drifted)
	if r := d.run("execution", "set", drift, "--status", "done"); r.code == 0 {
		t.Fatalf("a DRIFT reject let the run close:\n%s", r.out)
	}
	if st := d.status(drift); st != "in_progress" {
		t.Fatalf("drift run is %s after a DRIFT reject, want in_progress", st)
	}
	g = latestRun(d.sandbox, drift, "drift-constitution")
	if g.Result != "reject" || g.Binding != "reviewer" {
		t.Fatalf("drift run's drift-constitution = %s by %q, want reject by reviewer", g.Result, g.Binding)
	}
	ps := prompts(t, d.fake, "constitution")
	if len(ps) != 1 || !strings.Contains(ps[0], "+func Foo() { serveREST() }") || strings.Contains(ps[0], "satelle.version: 0.0.2") {
		t.Fatalf("constitution prompt does not carry exactly the internal/x.go hunk:\n%s", strings.Join(ps, "\n----\n"))
	}
	sys, err := os.ReadFile(filepath.Join(d.fake, "system.constitution"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".satelle/constitution.md", ".satelle/principles/"} {
		if !strings.Contains(string(sys), want) {
			t.Errorf("constitution judge's skill lacks the pointer %q", want)
		}
	}
	if route := d.must("execution", "route", drift); !strings.Contains(route, drifted) {
		t.Errorf("execution route does not show the DRIFT line:\n%s", route)
	}

	// Control: a bare seat records HEAD (v0.0.2) as the base, so the diff is
	// empty and the gate is skipped on paths before the trigger runs.
	bare := d.start(d.drift, `satelle execution seat "$exe"`)
	d.setFake("verdict.constitution", "")
	d.setFake("notes.constitution", "")
	d.run("execution", "set", bare, "--status", "done")
	if g := latestRun(d.sandbox, bare, "drift-constitution"); g.Result != "skipped" || !strings.Contains(g.Notes, "no changed path matches") {
		t.Fatalf("bare-seated run's drift-constitution = %s (%q), want skipped: no changed path matches", g.Result, g.Notes)
	}
}

// A diff longer than the gate prompt's cut: an accept stands only when the
// notes carry a READ line for every changed file the cut hid.
func TestDriftGateTruncatedDiffNeedsReads(t *testing.T) {
	t.Parallel()
	var big strings.Builder
	big.WriteString("package x\n\nvar Big = []string{\n")
	for big.Len() < 210_000 {
		big.WriteString("\t\"0123456789012345678901234567890123456789012345678901234567890123456789\",\n")
	}
	big.WriteString("}\n")
	d := newDriftSandbox(t, map[string]string{
		"internal/big.go": big.String(),
		"internal/z.go":   "package x\n\nfunc Z() {}\n",
	})
	drift := d.start(d.drift, stepSeat(t))
	d.assertJudgedFromPrev(drift)

	d.setFake("notes.constitution", "no drift")
	if r := d.run("execution", "set", drift, "--status", "done"); r.code == 0 {
		t.Fatal("an accept with no READ line closed a run whose diff was cut")
	}
	g := latestRun(d.sandbox, drift, "drift-constitution")
	if g.Result != "reject" || !strings.Contains(g.Notes, "diff truncated at 200000 bytes") || !strings.Contains(g.Notes, "internal/z.go") {
		t.Fatalf("drift-constitution = %s (%q), want a reject naming internal/z.go", g.Result, g.Notes)
	}
	if st := d.status(drift); st != "in_progress" {
		t.Fatalf("run is %s, want in_progress", st)
	}

	d.setFake("notes.constitution", "no drift\nREAD internal/big.go\nREAD internal/x.go\nREAD internal/z.go")
	d.run("execution", "set", drift, "--status", "done")
	if g := latestRun(d.sandbox, drift, "drift-constitution"); g.Result != "accept" {
		t.Fatalf("drift-constitution with every READ line = %s (%q), want accept", g.Result, g.Notes)
	}

	// Under the cut an accept with no READ line stands.
	small := newDriftSandbox(t, nil)
	exe := small.start(small.drift, stepSeat(t))
	small.setFake("notes.constitution", "no drift")
	small.run("execution", "set", exe, "--status", "done")
	if g := latestRun(small.sandbox, exe, "drift-constitution"); g.Result != "accept" {
		t.Fatalf("drift-constitution under the cut = %s (%q), want accept", g.Result, g.Notes)
	}
}

// The after check restates the gate prompt's cut and the gate's paths; both
// must match what the binary and runbook.toml hold.
func TestDriftAfterCheckMirrorsGate(t *testing.T) {
	after := repoText(t, ".satelle/skills/satelle-drift-diff-covered.md")
	limit := regexp.MustCompile(`(?m)^limit=(\d+)$`).FindStringSubmatch(after)
	cut := regexp.MustCompile(`const max = ([0-9_]+)`).FindStringSubmatch(repoText(t, "internal/gate/gate.go"))
	if limit == nil || cut == nil {
		t.Fatalf("no limit= line in the after check (%v) or const max in gate.go (%v)", limit, cut)
	}
	if want := strings.ReplaceAll(cut[1], "_", ""); limit[1] != want {
		t.Errorf("after check limit=%s, gate prompt cuts at %s", limit[1], want)
	}
	paths := regexp.MustCompile(`(?m)^paths='([^']*)'$`).FindStringSubmatch(after)
	if paths == nil {
		t.Fatal("the after check has no paths='...' line")
	}
	runbook := repoText(t, ".satelle/workflows/runbook.toml")
	_, sec, ok := strings.Cut(runbook, "[gates.drift-constitution]")
	if !ok {
		t.Fatal("runbook.toml has no [gates.drift-constitution]")
	}
	sec, _, _ = strings.Cut(sec, "\n[")
	gp := regexp.MustCompile(`(?m)^paths = (\[.*\])$`).FindStringSubmatch(sec)
	if gp == nil {
		t.Fatal("drift-constitution has no paths")
	}
	var gatePaths []string
	if err := json.Unmarshal([]byte(gp[1]), &gatePaths); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(paths[1]); !slices.Equal(got, gatePaths) {
		t.Errorf("after check paths %v, drift-constitution paths %v", got, gatePaths)
	}

	judge := repoText(t, ".satelle/skills/satelle-drift-constitution-review.md")
	for _, want := range []string{"`DRIFT <principle> <file>:<line> <symbol> — <what>`", "READ <path>", "already carried in `drift-report`"} {
		if !strings.Contains(judge, want) {
			t.Errorf("the constitution judge skill lacks %q", want)
		}
	}
	claims := repoText(t, ".satelle/skills/satelle-drift-claims-review.md")
	for _, want := range []string{"`CLAIM <file>:<line> \"<the statement>\"", "already carried in `drift-report`"} {
		if !strings.Contains(claims, want) {
			t.Errorf("the claims judge skill lacks %q", want)
		}
	}
}

// The drift-claims gate runs on every release (.version changes each time):
// judged by the reviewer binding over only the .version diff for a drift
// run, skipped by the trigger for another task's run, and a CLAIM reject keeps
// the run in_progress.
func TestDriftClaimsGateRunsEveryRelease(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	drift := d.start(d.drift, stepSeat(t))
	other := d.start(d.other, stepSeat(t))

	d.must("execution", "set", other, "--status", "done")
	if g := latestRun(d.sandbox, other, "drift-claims"); g.Result != "skipped" || !strings.Contains(g.Notes, "trigger satelle-drift-run-trigger") {
		t.Fatalf("other run's drift-claims = %s (%q), want skipped by the trigger", g.Result, g.Notes)
	}
	if ps := prompts(t, d.fake, "claims"); len(ps) != 0 {
		t.Fatalf("the other run dispatched the claims judge %d time(s)", len(ps))
	}

	const claim = `CLAIM .satelle/principles/p.md:6 "queue entries live in the story chain" — internal/kernel/queue.go:62`
	d.setFake("verdict.claims", "reject")
	d.setFake("notes.claims", claim)
	if r := d.run("execution", "set", drift, "--status", "done"); r.code == 0 {
		t.Fatal("a CLAIM reject let the run close")
	}
	if st := d.status(drift); st != "in_progress" {
		t.Fatalf("drift run is %s after a CLAIM reject, want in_progress", st)
	}
	if g := latestRun(d.sandbox, drift, "drift-constitution"); g.Result != "accept" {
		t.Fatalf("drift-constitution = %s, want accept before drift-claims runs", g.Result)
	}
	g := latestRun(d.sandbox, drift, "drift-claims")
	if g.Result != "reject" || g.Binding != "reviewer" {
		t.Fatalf("drift-claims = %s by %q, want reject by reviewer", g.Result, g.Binding)
	}
	ps := prompts(t, d.fake, "claims")
	if len(ps) != 1 || !strings.Contains(ps[0], "+satelle.version: 0.0.2") || strings.Contains(ps[0], "serveREST") {
		t.Fatalf("claims prompt does not carry only the .version hunk:\n%s", strings.Join(ps, "\n----\n"))
	}
	sys, err := os.ReadFile(filepath.Join(d.fake, "system.claims"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".satelle/constitution.md", ".satelle/principles/"} {
		if !strings.Contains(string(sys), want) {
			t.Errorf("claims judge's skill lacks the pointer %q", want)
		}
	}
	if route := d.must("execution", "route", drift); !strings.Contains(route, claim) {
		t.Errorf("execution route does not show the CLAIM line:\n%s", route)
	}
}

// An empty report is a pass: a drift run whose drift-report says "no drift"
// and whose judges all accept closes, with both drift gates judged.
func TestDriftEmptyReportPasses(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	drift := d.start(d.drift, stepSeat(t))
	d.exec(satelleBin, "no drift\n", "execution", "attach", drift, "--name", "drift-report", "--type", "text/markdown", "--file", "-")
	if r := d.run("execution", "set", drift, "--status", "done"); r.code != 0 {
		t.Fatalf("an all-accept drift run did not close: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := d.status(drift); st != "done" {
		t.Fatalf("drift run is %s, want done", st)
	}
	for _, gate := range []string{"drift-constitution", "drift-claims", "validate-after"} {
		if g := latestRun(d.sandbox, drift, gate); g.Result != "accept" {
			t.Errorf("%s = %s (%q), want accept", gate, g.Result, g.Notes)
		}
	}
	for _, gate := range []string{"constitution", "claims"} {
		ps := prompts(t, d.fake, gate)
		if len(ps) != 1 || !strings.Contains(ps[0], "drift-report") {
			t.Errorf("the %s judge's prompt does not name the drift-report file:\n%s", gate, strings.Join(ps, "\n----\n"))
		}
	}
}
