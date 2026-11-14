//go:build integration && unix

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The drift review's task id as the repo's substrate writes it, and the files
// that carry it on a literal task= line (release.md in its step 11).
const driftTaskLine = `(?m)^task=(tas_[A-Za-z0-9_]+)$`

var driftTaskFiles = []string{
	".satelle/skills/release.md",
	".satelle/skills/drift-review.md",
	".satelle/skills/satelle-drift-run-trigger.md",
	".satelle/skills/satelle-drift-resolved-check.md",
	".satelle/skills/satelle-drift-accept-quoted.md",
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

// repoGateAgents maps each gate of a repo workflow file to the agents that
// judge it (its agent, then its panel), as the repo's substrate binds them,
// so a fixture follows a rename instead of naming the repo's reviewers.
func repoGateAgents(t *testing.T, rel string) map[string][]string {
	t.Helper()
	var wf struct {
		Gates map[string]struct {
			Agent string   `toml:"agent"`
			Panel []string `toml:"panel"`
		} `toml:"gates"`
	}
	if _, err := toml.Decode(repoText(t, rel), &wf); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	out := map[string][]string{}
	for name, g := range wf.Gates {
		if g.Agent != "" {
			out[name] = append(out[name], g.Agent)
		}
		out[name] = append(out[name], g.Panel...)
	}
	return out
}

// judgeAgentsTOML is an agents.toml that binds every judge the named repo
// workflow files use to the claude stand-in, each on model (its own stand-in model when
// model is empty), after extra (already TOML).
func judgeAgentsTOML(t *testing.T, extra, model string, rels ...string) string {
	t.Helper()
	var names []string
	for _, rel := range rels {
		for _, agents := range repoGateAgents(t, rel) {
			for _, a := range agents {
				if !slices.Contains(names, a) {
					names = append(names, a)
				}
			}
		}
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString(extra)
	for i, n := range names {
		m := model
		if m == "" {
			m = fmt.Sprintf("judge-%d", i+1)
		}
		fmt.Fprintf(&b, "[agents.%q]\ncommand = \"claude\"\nmodel = %q\ntools = [\"Read\", \"Grep\", \"Glob\"]\n", n, m)
	}
	return b.String()
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
// (it defines the DRIFT line), "claims" (the WARN line), else "other". Each
// records its prompt in prompt.<gate>.<pid> and answers with verdict.<gate>
// (default accept) and notes.<gate> (by default notes that give a reason and
// name a file, as the gates' reasons rules require).
func fakeDriftJudge(t *testing.T, s *sandbox) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), `#!/bin/sh
sys=; prev=
for a in "$@"; do [ "$prev" = "--append-system-prompt" ] && sys=$a; prev=$a; done
g=other
case $sys in
  *'DRIFT <principle>'*) g=constitution ;;
  *'WARN <file>'*) g=claims ;;
esac
cat > "$FAKE_DIR/prompt.$g.$$"
printf '%s' "$sys" > "$FAKE_DIR/system.$g"
v=accept; [ -f "$FAKE_DIR/verdict.$g" ] && v=$(cat "$FAKE_DIR/verdict.$g")
n="$v by $g: judged the diff of internal/x.go against the record"; [ -f "$FAKE_DIR/notes.$g" ] && n=$(cat "$FAKE_DIR/notes.$g")
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
	s.write(".satelle/agents.toml", judgeAgentsTOML(t, "[agents.executor]\ncommand = \"session\"\n", "",
		".satelle/workflows/runbook.toml", ".satelle/workflows/drift.toml"))
	s.write(".satelle/workflows/runbook.toml", repoText(t, ".satelle/workflows/runbook.toml"))
	for _, name := range []string{"task-run", "satelle-task-validate-before-review", "satelle-task-validate-after-review", "satelle-release-kept-check",
		"satelle-drift-constitution-review", "satelle-drift-claims-review", "satelle-drift-diff-covered", "satelle-drift-closures-check",
		"satelle-verdict-notes-check", "satelle-verdict-reference-check"} {
		s.write(".satelle/skills/"+name+".md", repoText(t, ".satelle/skills/"+name+".md"))
	}
	// The trigger and validate-after's drift check as the repo has them until
	// the drift task exists; then its id.
	bound := []string{"satelle-drift-run-trigger", "satelle-drift-accept-quoted"}
	for _, name := range bound {
		s.write(".satelle/skills/"+name+".md", repoText(t, ".satelle/skills/"+name+".md"))
	}
	d.drift = s.create("task", "--title", "Drift review after a release", "--input-kind", "story", "--input-tag", "drift").ID
	d.other = s.create("task", "--title", "Remove finished worktrees", "--input-kind", "story").ID
	for _, name := range bound {
		body := regexp.MustCompile(driftTaskLine).ReplaceAllString(repoText(t, ".satelle/skills/"+name+".md"), "task="+d.drift)
		if !strings.Contains(body, "\ntask="+d.drift+"\n") {
			t.Fatalf("%s.md has no task=<id> line", name)
		}
		s.write(".satelle/skills/"+name+".md", body)
	}
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

const acceptQuoted = ".satelle/skills/satelle-drift-accept-quoted.md"

// acceptQuotedLiterals are the report's line forms satelle-drift-accept-quoted
// restates, each a name='value' line of its script.
func acceptQuotedLiterals(t *testing.T) map[string]string {
	t.Helper()
	text := repoText(t, acceptQuoted)
	out := map[string]string{}
	for _, name := range []string{"version_prefix", "exit_prefix", "fix_heading", "no_drift"} {
		m := regexp.MustCompile(`(?m)^` + name + `='([^']*)'$`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("%s has no %s='...' line", acceptQuoted, name)
		}
		out[name] = m[1]
	}
	return out
}

// fixtureDriftReport is a drift-report in drift-review.md steps 3 and 4's shape for
// the sandbox's release v0.0.2, finding nothing; fix, when not empty, is its
// "## Fix item" lines (the id, then the keys it carries).
func fixtureDriftReport(fix string) string {
	return "# Drift review of v0.0.2\n\nno drift\n\n## Checks\n" +
		"- satelle satelle 0.0.2 (fixture)\n" +
		"- scripts/drift-check.sh: drift-check exit 0\n" +
		"- drift-constitution: v0.0.1..v0.0.2 over cmd/, internal/, go.mod, go.sum, .satelle/constitution.md, .satelle/principles/\n" +
		"- drift-claims: .satelle/constitution.md and .satelle/principles/ against cmd/ and internal/ at v0.0.2\n\n" +
		"## Substrate findings\n\n## Constitution findings\n\n## Claim warnings\n\n" +
		"## Fix item\n" + fix + "\n" +
		"## Open drift items\n\n## No longer found\n\n## Resolved by the judges\n\n" +
		"## Closed\n\n## Carried\n\n## Stayed open\n"
}

// noDriftReport is fixtureDriftReport with an empty "## Fix item".
func noDriftReport() string { return fixtureDriftReport("") }

// attachReport attaches body as exe's drift-report.
func (d *driftSandbox) attachReport(exe, body string) {
	d.t.Helper()
	if r := d.exec(satelleBin, body, "execution", "attach", exe, "--name", "drift-report", "--type", "text/markdown", "--file", "-"); r.code != 0 {
		d.t.Fatalf("attach drift-report: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

// driftQuotes are the three lines of exe's attached drift-report an accept
// quotes, found by satelle-drift-accept-quoted's own line forms: the version
// line and the drift-check exit line without their "- ", and the fix item's
// id or the line "no drift".
func (d *driftSandbox) driftQuotes(exe string) []string {
	d.t.Helper()
	lit := acceptQuotedLiterals(d.t)
	rep := d.must("execution", "attachment", exe, "drift-report")
	var version, exit string
	for _, f := range reportSection(rep, "Checks") {
		l := strings.TrimRight(strings.Join(f, "\t"), " \t\r")
		if version == "" && strings.HasPrefix(l, lit["version_prefix"]) {
			version = strings.TrimPrefix(l, "- ")
		}
		if exit == "" && strings.HasPrefix(l, lit["exit_prefix"]) {
			exit = strings.TrimPrefix(l, "- ")
		}
	}
	fix := lit["no_drift"]
	if fi := reportSection(rep, strings.TrimPrefix(lit["fix_heading"], "## ")); len(fi) > 0 {
		fix = strings.Fields(fi[0][0])[0]
	}
	if version == "" || exit == "" {
		d.t.Fatalf("%s's drift-report has no version or exit line:\n%s", exe, rep)
	}
	return []string{version, exit, fix}
}

// skillDriftAccept is the drift accept the validate-after judge's skill shows
// in its ## Verdict section, its three placeholders filled in order with
// quotes.
func skillDriftAccept(t *testing.T, quotes ...string) string {
	t.Helper()
	_, verdict, ok := strings.Cut(repoText(t, ".satelle/skills/satelle-task-validate-after-review.md"), "\n## Verdict\n")
	if !ok {
		t.Fatal("the validate-after skill has no ## Verdict section")
	}
	notes := ""
	for _, block := range regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllStringSubmatch(verdict, -1) {
		var v struct {
			Verdict string `json:"verdict"`
			Notes   string `json:"notes"`
		}
		if err := json.Unmarshal([]byte(block[1]), &v); err != nil {
			t.Fatalf("example verdict %q: %v", block[1], err)
		}
		if v.Verdict == "accept" && strings.Contains(v.Notes, "attachments/drift-report") {
			notes = v.Notes
		}
	}
	if notes == "" {
		t.Fatal("the validate-after skill shows no accept naming attachments/drift-report")
	}
	ph := regexp.MustCompile(`<[^<>]*>`)
	if n := len(ph.FindAllString(notes, -1)); n != 3 || len(quotes) != 3 {
		t.Fatalf("the skill's drift accept has %d placeholders, given %d quotes: %q", n, len(quotes), notes)
	}
	i := 0
	return ph.ReplaceAllStringFunc(notes, func(string) string { i++; return quotes[i-1] })
}

// citeReport makes the validate-after judge answer the skill's drift accept
// quoting exe's own drift-report.
func (d *driftSandbox) citeReport(exe string) {
	d.t.Helper()
	d.setFake("notes.other", skillDriftAccept(d.t, d.driftQuotes(exe)...))
}

// gatesAccepted fails unless each gate's latest run on exe accepted.
func (d *driftSandbox) gatesAccepted(exe string, gates ...string) {
	d.t.Helper()
	for _, gate := range gates {
		if g := latestRun(d.sandbox, exe, gate); g.Result != "accept" {
			d.t.Errorf("%s = %s (%q), want accept", gate, g.Result, g.Notes)
		}
	}
}

// The drift-constitution gate judges only drift runs: another task's run over
// the same diff skips it by the trigger; a drift run seated by release.md
// step 11 is judged by the runbook's drift-constitution binding over its internal/ diff with the
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
	if want := repoGateAgents(t, ".satelle/workflows/runbook.toml")["drift-constitution"]; g.Result != "reject" || len(want) != 1 || g.Binding != want[0] {
		t.Fatalf("drift run's drift-constitution = %s by %q, want reject by the runbook's judge %v", g.Result, g.Binding, want)
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

	d.setFake("notes.constitution", "no drift: every principle holds over the diff of internal/x.go")
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
	small.setFake("notes.constitution", "no drift: every principle holds over the diff of internal/x.go")
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
	for _, want := range []string{"`WARN <file>:<line> \"<the statement>\"", "always **accept**"} {
		if !strings.Contains(claims, want) {
			t.Errorf("the claims judge skill lacks %q", want)
		}
	}
}

// The drift-claims gate runs when the run's range changed one of its paths
// (.version on every binary-changed release, the constitution or a
// principle): skipped by the trigger for another task's run, and for a drift
// run judged by the runbook's drift-claims binding over only the .version
// diff of this sandbox's release. Its verdict never blocks:
// each example verdict in the skill's ## Verdict section (zero warnings, one
// warning) passes the gate's real notes and reference after checks, and the
// run reaches done; the skill states no reject, and drift-review.md step 5
// copies no WARN or CLAIM line into judged.txt (sto_a1681009 AC8).
func TestDriftClaimsNeverRejects(t *testing.T) {
	t.Parallel()
	skill := repoText(t, ".satelle/skills/satelle-drift-claims-review.md")
	if strings.Contains(skill, `"verdict": "reject"`) {
		t.Fatal("the claims skill still states a reject verdict")
	}
	_, verdict, ok := strings.Cut(skill, "\n## Verdict\n")
	if !ok {
		t.Fatal("the claims skill has no ## Verdict section")
	}
	var examples []struct {
		Verdict string `json:"verdict"`
		Notes   string `json:"notes"`
	}
	for _, block := range regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllStringSubmatch(verdict, -1) {
		var v struct {
			Verdict string `json:"verdict"`
			Notes   string `json:"notes"`
		}
		if err := json.Unmarshal([]byte(block[1]), &v); err != nil {
			t.Fatalf("example verdict %q: %v", block[1], err)
		}
		examples = append(examples, v)
	}
	if len(examples) != 2 || strings.Contains(examples[0].Notes, "WARN ") || !strings.Contains(examples[1].Notes, "\nWARN ") {
		t.Fatalf("want two example verdicts, zero and one warning: %+v", examples)
	}
	for i, ex := range examples {
		d := newDriftSandbox(t, nil)
		drift := d.start(d.drift, stepSeat(t))
		if i == 0 {
			other := d.start(d.other, stepSeat(t))
			d.must("execution", "set", other, "--status", "done")
			if g := latestRun(d.sandbox, other, "drift-claims"); g.Result != "skipped" || !strings.Contains(g.Notes, "trigger satelle-drift-run-trigger") {
				t.Fatalf("other run's drift-claims = %s (%q), want skipped by the trigger", g.Result, g.Notes)
			}
			if ps := prompts(t, d.fake, "claims"); len(ps) != 0 {
				t.Fatalf("the other run dispatched the claims judge %d time(s)", len(ps))
			}
		}
		d.attachReport(drift, noDriftReport())
		d.citeReport(drift)
		d.setFake("verdict.claims", ex.Verdict)
		d.setFake("notes.claims", ex.Notes)
		if r := d.run("execution", "set", drift, "--status", "done"); r.code != 0 {
			t.Fatalf("example %d: the run did not close: exit %d\n%s%s", i, r.code, r.out, r.err)
		}
		g := latestRun(d.sandbox, drift, "drift-claims")
		if want := repoGateAgents(t, ".satelle/workflows/runbook.toml")["drift-claims"]; g.Result != "accept" || len(want) != 1 || g.Binding != want[0] {
			t.Fatalf("example %d: drift-claims = %s by %q (%q), want accept by the runbook's judge %v", i, g.Result, g.Binding, g.Notes, want)
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
	}

	review := repoText(t, ".satelle/skills/drift-review.md")
	_, five, _ := strings.Cut(review, "\n## 5.")
	five, _, _ = strings.Cut(five, "\n## 6.")
	if strings.Contains(five, "CLAIM") {
		t.Error("drift-review.md step 5 still turns CLAIM lines into findings")
	}
	for _, para := range strings.Split(five, "\n- ") {
		if strings.Contains(para, "WARN") && strings.Contains(para, "judged.txt") {
			t.Errorf("drift-review.md step 5 copies a WARN line into judged.txt:\n%s", para)
		}
	}
}

// An empty report is a pass: a drift run whose drift-report says "no drift"
// and whose judges all accept, validate-after's quoting the report, closes,
// with both drift gates judged.
func TestDriftEmptyReportPasses(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	drift := d.start(d.drift, stepSeat(t))
	d.attachReport(drift, noDriftReport())
	d.citeReport(drift)
	if r := d.run("execution", "set", drift, "--status", "done"); r.code != 0 {
		t.Fatalf("an all-accept drift run did not close: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if st := d.status(drift); st != "done" {
		t.Fatalf("drift run is %s, want done", st)
	}
	d.gatesAccepted(drift, "release-kept", "drift-constitution", "drift-claims", "drift-closures", "validate-after")
	for _, gate := range []string{"constitution", "claims"} {
		ps := prompts(t, d.fake, gate)
		if len(ps) != 1 || !strings.Contains(ps[0], "drift-report") {
			t.Errorf("the %s judge's prompt does not name the drift-report file:\n%s", gate, strings.Join(ps, "\n----\n"))
		}
	}
}

// presentDone presents exe done with notes as the validate-after judge's
// answer and returns validate-after's run.
func (d *driftSandbox) presentDone(exe, notes string) (result, gateRun) {
	d.t.Helper()
	d.setFake("notes.other", notes)
	r := d.run("execution", "set", exe, "--status", "done")
	return r, latestRun(d.sandbox, exe, "validate-after")
}

// sto_09b81217 AC1: on a drift review run, validate-after's accept stands only
// when it names attachments/drift-report and quotes that report's version
// line, drift-check exit line and fix item (or its no drift line). Each one
// left out, or invented, is refused by satelle-drift-accept-quoted naming the
// report's own text, with every other gate accepting; the skill's own drift
// accept with the run's values closes it. Another task's run, and a judge's
// reject, never reach the check.
func TestValidateAfterHoldsADriftAcceptToTheReport(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	exe := d.start(d.drift, stepSeat(t))
	d.attachReport(exe, noDriftReport())
	q := d.driftQuotes(exe)
	if want := []string{"satelle satelle 0.0.2 (fixture)", "scripts/drift-check.sh: drift-check exit 0", "no drift"}; !slices.Equal(q, want) {
		t.Fatalf("the fixture's quotes %q, want %q", q, want)
	}
	refused := func(name, notes, want string) {
		t.Helper()
		r, g := d.presentDone(exe, notes)
		if r.code == 0 {
			t.Fatalf("%s: the run closed:\n%s", name, r.out)
		}
		if st := d.status(exe); st != "in_progress" {
			t.Fatalf("%s: run is %s, want in_progress", name, st)
		}
		if g.Result != "reject" || !strings.Contains(g.Notes, "after satelle-drift-accept-quoted:") || !strings.Contains(g.Notes, want) {
			t.Fatalf("%s: validate-after = %s (%q), want a reject by satelle-drift-accept-quoted naming %q", name, g.Result, g.Notes, want)
		}
		d.gatesAccepted(exe, "release-kept", "drift-constitution", "drift-claims", "drift-closures")
	}
	refused("no report path", strings.ReplaceAll(skillDriftAccept(t, q...), "attachments/drift-report", "attachments/report"),
		"accepted without naming attachments/drift-report")
	refused("invented version", skillDriftAccept(t, "satelle 9.9.9", q[1], q[2]),
		"accepted without quoting attachments/drift-report's version line: satelle satelle 0.0.2 (fixture)")
	refused("no exit", skillDriftAccept(t, q[0], "", q[2]),
		"accepted without quoting attachments/drift-report's drift-check exit line: scripts/drift-check.sh: drift-check exit 0")
	refused("invented fix item", skillDriftAccept(t, q[0], q[1], "sto_00000000"),
		"accepted without quoting attachments/drift-report's line: no drift")

	// A judge's reject is its own finding: the check does not run on it.
	d.setFake("verdict.other", "reject")
	_, g := d.presentDone(exe, "attachments/drift-report lacks the evidence the VERIFICATION asks for")
	if g.Result != "reject" || strings.Contains(g.Notes, "satelle-drift-accept-quoted") {
		t.Fatalf("judge's reject: validate-after = %s (%q), want the judge's reject with no satelle-drift-accept-quoted line", g.Result, g.Notes)
	}
	d.setFake("verdict.other", "")

	// Another task's run passes the check with no output.
	other := d.start(d.other, stepSeat(t))
	d.setFake("notes.other", "")
	d.must("execution", "set", other, "--status", "done")
	if g := latestRun(d.sandbox, other, "validate-after"); g.Result != "accept" || strings.Contains(g.Notes, "satelle-drift-accept-quoted") {
		t.Fatalf("other run's validate-after = %s (%q), want accept with no satelle-drift-accept-quoted line", g.Result, g.Notes)
	}

	// A report with a fix item: the id is the quote, not no drift.
	d.attachReport(exe, fixtureDriftReport("sto_12345678\nflag:cmd/x.go:--y\n"))
	q = d.driftQuotes(exe)
	if q[2] != "sto_12345678" {
		t.Fatalf("the fix-item report's third quote is %q, want sto_12345678", q[2])
	}
	refused("no drift for a fix item", skillDriftAccept(t, q[0], q[1], "no drift"),
		"accepted without quoting attachments/drift-report's fix item: sto_12345678")
	d.citeReport(exe)
	r := d.run("execution", "set", exe, "--status", "done")
	g = latestRun(d.sandbox, exe, "validate-after")
	if r.code != 0 {
		t.Fatalf("the cited accept did not close the run: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if g.Result != "accept" || !strings.Contains(g.Notes, "the accept quotes attachments/drift-report: satelle satelle 0.0.2 (fixture); scripts/drift-check.sh: drift-check exit 0; sto_12345678") {
		t.Fatalf("validate-after = %s (%q), want accept quoting the report", g.Result, g.Notes)
	}
	if st := d.status(exe); st != "done" {
		t.Fatalf("run is %s, want done", st)
	}
}

// satelle-drift-accept-quoted restates its owners: the judge skill's drift
// paragraph names each line it requires, the report path and the check;
// drift-review.md step 3 writes each line form; runbook's validate-after lists
// it third, behind the notes and reference rules.
func TestDriftAcceptCheckMirrorsItsOwners(t *testing.T) {
	lit := acceptQuotedLiterals(t)
	skill := repoText(t, ".satelle/skills/satelle-task-validate-after-review.md")
	_, judge, ok := strings.Cut(skill, "\n## How to judge\n")
	if !ok {
		t.Fatal("the validate-after skill has no ## How to judge section")
	}
	judge, _, _ = strings.Cut(judge, "\n## ")
	for _, want := range []string{lit["version_prefix"], lit["exit_prefix"], lit["fix_heading"], lit["no_drift"], "attachments/drift-report", "satelle-drift-accept-quoted"} {
		if !strings.Contains(judge, want) {
			t.Errorf("the validate-after skill's ## How to judge lacks %q", want)
		}
	}
	review := repoText(t, driftReview)
	_, three, _ := strings.Cut(review, "\n## 3.")
	three, _, _ = strings.Cut(three, "\n## 4.")
	for _, want := range []string{`echo "` + lit["version_prefix"], `echo "` + lit["exit_prefix"], `echo "` + lit["fix_heading"] + `"`, `echo "` + lit["no_drift"] + `"`} {
		if !strings.Contains(three, want) {
			t.Errorf("drift-review.md step 3 does not write %q", want)
		}
	}
	var wf struct {
		Gates map[string]struct {
			After []string `toml:"after"`
		} `toml:"gates"`
	}
	if _, err := toml.Decode(repoText(t, ".satelle/workflows/runbook.toml"), &wf); err != nil {
		t.Fatal(err)
	}
	if after := wf.Gates["validate-after"].After; len(after) != 3 || after[2] != "satelle-drift-accept-quoted" {
		t.Errorf("runbook's validate-after after = %v, want satelle-drift-accept-quoted third", after)
	}
}

// sto_09b81217 AC4: a no-drift run with every other gate accepting stays
// in_progress on the notes exe_9a209ff1's judge gave (no record path), and
// closes on an accept quoting its own report's version, exit and no drift
// lines, judged by the runbook's validate-after binding.
func TestValidateAfterCitedAcceptClosesNoDriftRun(t *testing.T) {
	t.Parallel()
	d := newDriftSandbox(t, nil)
	exe := d.start(d.drift, stepSeat(t))
	d.attachReport(exe, noDriftReport())

	r, g := d.presentDone(exe, exe9a209ff1Notes)
	if r.code == 0 {
		t.Fatalf("exe_9a209ff1's notes closed the run:\n%s", r.out)
	}
	if g.Result != "reject" || !strings.Contains(g.Notes, "the judge gave no reasons") || !strings.Contains(g.Notes, "after satelle-drift-accept-quoted: not run") {
		t.Fatalf("validate-after = %s (%q), want a reject: the judge gave no reasons", g.Result, g.Notes)
	}
	if st := d.status(exe); st != "in_progress" {
		t.Fatalf("run is %s, want in_progress", st)
	}
	d.gatesAccepted(exe, "release-kept", "drift-constitution", "drift-claims", "drift-closures")

	d.citeReport(exe)
	if r := d.run("execution", "set", exe, "--status", "done"); r.code != 0 {
		t.Fatalf("the cited accept did not close the run: exit %d\n%s%s", r.code, r.out, r.err)
	}
	d.gatesAccepted(exe, "release-kept", "drift-constitution", "drift-claims", "drift-closures", "validate-after")
	g = latestRun(d.sandbox, exe, "validate-after")
	if want := repoGateAgents(t, ".satelle/workflows/runbook.toml")["validate-after"]; len(want) != 1 || g.Binding != want[0] {
		t.Fatalf("validate-after judged by %q, want the runbook's judge %v", g.Binding, want)
	}
	// AC3: the rebind on recorded evidence (attachment judge-evidence) is to
	// GPT-5.6 Sol; the baseline Gemini binding fails here.
	if want := "reviewer-gpt-5.6-sol-high"; g.Binding != want {
		t.Fatalf("validate-after judged by %q, want %q: validate-after is rebound on the judge evidence", g.Binding, want)
	}
	if !strings.Contains(g.Notes, "the accept quotes attachments/drift-report: satelle satelle 0.0.2 (fixture); scripts/drift-check.sh: drift-check exit 0; no drift") || strings.Contains(g.Notes, "the judge gave no reasons") {
		t.Fatalf("validate-after notes %q, want the accept quoting the report", g.Notes)
	}
	if st := d.status(exe); st != "done" {
		t.Fatalf("run is %s, want done", st)
	}
}

// driftTaskBody is the drift review task's body as plan sto_da9775c1's
// Configuration to apply restates it.
const driftTaskBody = `ACTION: follow .satelle/skills/drift-review.md.

## Head after a binary-unchanged release
This section overrides any statement above that the run's head is the released tag v<version> or that its base is the previous release's tag.
ACTION: after a binary-changed release the run judges the previous tag to the released tag, as stated above. After a binary-unchanged release no new tag exists: the run judges from the published tag v<version> to main, the commit main fast-forwarded to (release.md step 11).
VERIFICATION: on a binary-unchanged head the drift-report opens "# Drift review of main <sha12> (binary-unchanged since v<version>)" and its Checks lines name the range v<version>..main <sha12>. A v<version> tag at the head is required only after a binary-changed release. A drift-claims or drift-constitution gate skipped because the range changed none of its paths is not a missing check.
`

// unchangedDriftSandbox is a drift sandbox with a bare origin whose main is
// one commit (files) ahead of the published v0.0.2, as a binary-unchanged
// release leaves it, and the main sha.
func unchangedDriftSandbox(t *testing.T, files map[string]string) (*driftCloseSandbox, string) {
	t.Helper()
	d := newDriftCloseSandbox(t)
	origin := realDir(t, t.TempDir())
	d.exec("git", "", "init", "-q", "--bare", origin)
	d.git("remote", "add", "origin", origin)
	// The sandbox's config commit is part of the published v0.0.2.
	d.git("tag", "-f", "v0.0.2")
	for p, body := range files {
		d.write(p, body)
	}
	d.git("add", "-A")
	d.git("commit", "-q", "-m", "a binary-unchanged landing")
	d.git("push", "-q", "origin", "HEAD:refs/heads/main")
	d.git("push", "-q", "origin", "--tags")
	d.git("fetch", "-q", "origin")
	return d, d.rev("HEAD")
}

// stepEleven runs release.md's step 11 block as written, its task= line bound
// to the sandbox's drift task and its cd line dropped (the sandbox is the main
// checkout).
func (d *driftCloseSandbox) stepEleven() result {
	d.t.Helper()
	block := shBlock(d.t, ".satelle/skills/release.md", "## 11.", d.drift)
	cd := "cd <the main checkout>\n"
	if !strings.Contains(block, cd) {
		d.t.Fatalf("release.md step 11 has no %q line", cd)
	}
	return d.exec("bash", "", "-c", strings.Replace(block, cd, "", 1))
}

func (d *driftCloseSandbox) driftRuns() []string {
	d.t.Helper()
	var runs []struct {
		ID string `json:"id"`
	}
	d.mustJSON(&runs, "execution", "list", "--parent", d.drift)
	var out []string
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

// AC5: after a binary-unchanged release (main ahead of v0.0.2, no new tag)
// release.md step 11 starts the drift review judged from v0.0.2 to main;
// drift-review.md names the head main <sha12> on the report; the run's task
// attachment holds the restated body and the report holds what its
// VERIFICATION names; the run reaches done with drift-constitution judging the
// landed source, drift-claims skipped on paths unless a principle changed (then
// judged over that hunk alone, with the restated skill), and both skipped when
// only substrate landed. A drift task whose body does not state this head, and
// a HEAD that is neither the tag nor origin/main, are refused with no run.
func TestDriftReviewAfterBinaryUnchangedRun(t *testing.T) {
	t.Parallel()
	skill := "---\nname: x\ntype: skill\ndescription: x\n---\n\nx\n"
	d, main := unchangedDriftSandbox(t, map[string]string{
		".satelle/skills/x.md": skill,
		"internal/x_test.go":   "package x\n\n// a test\n",
	})
	sha12 := main[:12]

	// The task still says the head is the released tag: refused, no run.
	if r := d.stepEleven(); r.code != 1 || !strings.Contains(r.err, "does not state the head of a binary-unchanged release") {
		t.Fatalf("step 11 with the old task body: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if runs := d.driftRuns(); len(runs) != 0 {
		t.Fatalf("a refused step 11 created runs %v", runs)
	}
	d.must("task", "set", d.drift, "--body", driftTaskBody)
	if r := d.stepEleven(); r.code != 0 {
		t.Fatalf("step 11 on main ahead of v0.0.2: exit %d\n%s%s", r.code, r.out, r.err)
	}
	runs := d.driftRuns()
	if len(runs) != 1 {
		t.Fatalf("drift runs %v, want one", runs)
	}
	exe := runs[0]
	var diff struct {
		Base  string   `json:"base"`
		Paths []string `json:"paths"`
	}
	d.mustJSON(&diff, "execution", "diff", exe)
	if want := d.rev("v0.0.2^{commit}"); diff.Base != want || !slices.Contains(diff.Paths, "internal/x_test.go") || !slices.Contains(diff.Paths, ".satelle/skills/x.md") {
		t.Fatalf("run %s judged from %s over %v; want v0.0.2 (%s) with both landed files", exe, diff.Base, diff.Paths, want)
	}
	if r := d.step("## 0."); !strings.Contains(r.out, "run "+exe+": main "+sha12+" judged from v0.0.2\n") {
		t.Fatalf("drift-review step 0:\n%s", r.out)
	}
	d.substrate(exe, "", "0")
	d.step("## 2.")
	d.step("## 3.")
	d.step("## 4.")
	rep := d.report(exe)
	heading := "# Drift review of main " + sha12 + " (binary-unchanged since v0.0.2)"
	rangeLine := "v0.0.2..main " + sha12
	if !strings.HasPrefix(rep, heading+"\n") || !strings.Contains(rep, "\n- drift-constitution: "+rangeLine+" over ") ||
		!strings.Contains(rep, "\n- satelle ") || !strings.Contains(rep, "\n- scripts/drift-check.sh: drift-check exit 0\n") {
		t.Fatalf("the report:\n%s", rep)
	}
	task := d.must("execution", "attachment", exe, "task")
	if !strings.Contains(task, "## Head after a binary-unchanged release") {
		t.Fatalf("the run's task attachment lacks the restated section:\n%s", task)
	}
	for _, want := range []string{"# Drift review of main <sha12> (binary-unchanged since v<version>)", "v<version>..main <sha12>"} {
		if !strings.Contains(task, want) {
			t.Fatalf("the restated VERIFICATION lacks %q", want)
		}
		got := strings.NewReplacer("<sha12>", sha12, "<version>", "0.0.2").Replace(want)
		if !strings.Contains(rep, got) {
			t.Fatalf("the report lacks %q, which the VERIFICATION names:\n%s", got, rep)
		}
	}
	prompts(t, d.fake, "constitution")
	d.citeReport(exe)
	if r := d.run("execution", "set", exe, "--status", "done"); r.code != 0 {
		t.Fatalf("the drift run did not close: exit %d\n%s%s", r.code, r.out, r.err)
	}
	d.gatesAccepted(exe, "drift-constitution", "drift-closures", "validate-after")
	if g := latestRun(d.sandbox, exe, "drift-claims"); g.Result != "skipped" || !strings.Contains(g.Notes, "no changed path matches") {
		t.Fatalf("drift-claims = %s (%q), want skipped on paths", g.Result, g.Notes)
	}
	if ps := prompts(t, d.fake, "constitution"); len(ps) != 1 || !strings.Contains(ps[0], "+// a test") {
		t.Fatalf("the constitution judge's prompt lacks the internal/x_test.go hunk:\n%s", strings.Join(ps, "\n----\n"))
	}

	// HEAD ahead of the tag but not origin/main: refused, no run.
	d.write("docs/local.md", "local\n")
	d.git("add", "-A")
	d.git("commit", "-q", "-m", "local only")
	if r := d.stepEleven(); r.code != 1 || !strings.Contains(r.err, "neither at v0.0.2 nor at origin/main ahead of it") {
		t.Fatalf("step 11 ahead of origin/main: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if runs := d.driftRuns(); len(runs) != 1 {
		t.Fatalf("a refused step 11 created a run: %v", runs)
	}

	// A principle landed too: drift-claims judges that hunk alone, with the
	// restated skill.
	p, _ := unchangedDriftSandbox(t, map[string]string{
		".satelle/skills/x.md":     skill,
		"internal/x_test.go":       "package x\n",
		".satelle/principles/p.md": "---\nname: p\ntype: principle\ndescription: A principle.\n---\n\nThe queue lives in a file.\n",
	})
	p.must("task", "set", p.drift, "--body", driftTaskBody)
	if r := p.stepEleven(); r.code != 0 {
		t.Fatalf("step 11 (principle): exit %d\n%s%s", r.code, r.out, r.err)
	}
	pexe := p.driftRuns()[0]
	p.attachReport(pexe, noDriftReport())
	p.citeReport(pexe)
	if r := p.run("execution", "set", pexe, "--status", "done"); r.code != 0 {
		t.Fatalf("the drift run (principle) did not close: exit %d\n%s%s", r.code, r.out, r.err)
	}
	ps := prompts(t, p.fake, "claims")
	if len(ps) != 1 || !strings.Contains(ps[0], "+The queue lives in a file.") || strings.Contains(ps[0], "satelle.version") {
		t.Fatalf("the claims prompt does not carry only the principle hunk:\n%s", strings.Join(ps, "\n----\n"))
	}
	if sys, _ := os.ReadFile(filepath.Join(p.fake, "system.claims")); !strings.Contains(string(sys), "The diff in your prompt is the range's change to") {
		t.Fatal("the claims judge was not given the restated skill")
	}
	p.gatesAccepted(pexe, "drift-claims", "validate-after")

	// Only substrate landed: both judges are skipped on paths.
	s, _ := unchangedDriftSandbox(t, map[string]string{".satelle/skills/x.md": skill})
	s.must("task", "set", s.drift, "--body", driftTaskBody)
	if r := s.stepEleven(); r.code != 0 {
		t.Fatalf("step 11 (substrate): exit %d\n%s%s", r.code, r.out, r.err)
	}
	sexe := s.driftRuns()[0]
	s.attachReport(sexe, noDriftReport())
	s.citeReport(sexe)
	if r := s.run("execution", "set", sexe, "--status", "done"); r.code != 0 {
		t.Fatalf("the drift run (substrate) did not close: exit %d\n%s%s", r.code, r.out, r.err)
	}
	for _, gate := range []string{"drift-constitution", "drift-claims"} {
		if g := latestRun(s.sandbox, sexe, gate); g.Result != "skipped" || !strings.Contains(g.Notes, "no changed path matches") {
			t.Fatalf("%s = %s (%q), want skipped on paths", gate, g.Result, g.Notes)
		}
	}

	// At the released tag (a binary-changed release) the report is as before:
	// it names the tag and the previous tag's range.
	tg := newDriftCloseSandbox(t)
	tg.git("tag", "-f", "v0.0.2")
	texe := tg.start(tg.drift, stepSeat(t))
	if r := tg.step("## 0."); !strings.Contains(r.out, "run "+texe+": v0.0.2 judged from v0.0.1\n") {
		t.Fatalf("drift-review step 0 at the tag:\n%s", r.out)
	}
	tg.substrate(texe, "", "0")
	tg.step("## 2.")
	tg.step("## 3.")
	if rep := tg.report(texe); !strings.HasPrefix(rep, "# Drift review of v0.0.2\n") || !strings.Contains(rep, "\n- drift-constitution: v0.0.1..v0.0.2 over ") ||
		!strings.Contains(rep, " against cmd/ and internal/ at v0.0.2\n") {
		t.Fatalf("the report at the tag:\n%s", rep)
	}
}

// AC5: no drift skill or the runbook still says the claims gate runs on every
// release, and the claims skill names every path the gate runs on, so the two
// cannot drift apart again.
func TestDriftSkillsStateTheirTrigger(t *testing.T) {
	for _, f := range []string{".satelle/skills/satelle-drift-claims-review.md", driftReview,
		".satelle/skills/satelle-drift-constitution-review.md", ".satelle/workflows/runbook.toml"} {
		var words []string
		for _, w := range strings.Fields(repoText(t, f)) {
			if w != "#" {
				words = append(words, w)
			}
		}
		text := strings.Join(words, " ")
		for _, stale := range []string{"is only `.version`", "run on every release", "relevant on every release", "relevant to every drift run"} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still says %q", f, stale)
			}
		}
	}
	var wf struct {
		Gates map[string]struct {
			Paths []string `toml:"paths"`
		} `toml:"gates"`
	}
	if _, err := toml.Decode(repoText(t, ".satelle/workflows/runbook.toml"), &wf); err != nil {
		t.Fatal(err)
	}
	paths := wf.Gates["drift-claims"].Paths
	if len(paths) == 0 {
		t.Fatal("runbook.toml's drift-claims has no paths")
	}
	claims := strings.Join(strings.Fields(repoText(t, ".satelle/skills/satelle-drift-claims-review.md")), " ")
	_, sentence, ok := strings.Cut(claims, "The diff in your prompt is")
	if !ok {
		t.Fatal("the claims skill has no sentence starting \"The diff in your prompt is\"")
	}
	sentence, _, _ = strings.Cut(sentence, ". ")
	for _, p := range paths {
		if p = strings.TrimSuffix(p, "/**"); !strings.Contains(sentence, p) {
			t.Errorf("the claims skill's diff sentence does not name the gate path %s:%s", p, sentence)
		}
	}
}
