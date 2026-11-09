//go:build integration && unix

package tests

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// The findings fed to drift-fix.sh: one substrate (command), one
// constitution and one prose finding.
const driftFindings = `.satelle/skills/a.md:3: command: satelle story doc — doc is not a subcommand of satelle story
internal/x.go:3: constitution: satelle-operator-rulings Foo — a REST handler beside gRPC
.satelle/principles/p.md:6: prose: queue entries live in the story chain — internal/kernel/queue.go:62
`

const (
	commandKey      = "command:.satelle/skills/a.md:satelle story doc"
	constitutionKey = "constitution:internal/x.go:satelle-operator-rulings Foo"
	proseKey        = "prose:.satelle/principles/p.md:queue entries live in the story chain"
	flagLine        = `.satelle/skills/b.md:4: flag: satelle story list --nope — no such flag`
	flagKey         = "flag:.satelle/skills/b.md:satelle story list --nope"
)

// fixed is one line of drift-fix.sh <version>'s output.
type fixed struct {
	id, what, findings, warnings string
}

func parseFixed(t *testing.T, out string) fixed {
	t.Helper()
	f := strings.Split(strings.TrimSpace(out), "\t")
	if len(f) != 4 || strings.Contains(out, "\n\n") || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("output %q is not one <id>\\t<new|updated>\\t<n> findings\\t<m> warnings line", out)
	}
	return fixed{f[0], f[1], f[2], f[3]}
}

// driftFix runs the sandbox's scripts/drift-fix.sh.
func (d *driftSandbox) driftFix(stdin string, args ...string) result {
	d.t.Helper()
	return d.exec("bash", stdin, append([]string{"scripts/drift-fix.sh"}, args...)...)
}

// openDrift returns the open stories tagged drift.
func (d *driftSandbox) openDrift() []item.Item {
	d.t.Helper()
	var its []item.Item
	d.mustJSON(&its, "story", "list", "--tag", "drift", "--open")
	return its
}

// bodyLines returns the lines of body starting with prefix, prefix stripped.
func bodyLines(body, prefix string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(l, prefix); ok {
			out = append(out, rest)
		}
	}
	return out
}

// section returns body's text under "## name", up to the next "## ".
func bodySection(body, name string) string {
	_, rest, ok := strings.Cut(body, "## "+name+"\n")
	if !ok {
		return ""
	}
	rest, _, _ = strings.Cut(rest, "\n## ")
	return rest
}

// One drift-fix item per release: the findings of v9.9.9 give one item on the
// drift workflow tagged drift, drift-fix and release:v9.9.9, its keys under
// ## Findings and its warning under ## Warnings; a rerun updates the same item;
// empty input writes nothing; another version gets its own item; an unknown
// kind writes nothing (sto_a1681009 AC3).
func TestDriftFixOneItemPerRelease(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	r := d.driftFix(driftFindings, "9.9.9")
	if r.code != 0 {
		t.Fatalf("drift-fix.sh 9.9.9: exit %d\n%s%s", r.code, r.out, r.err)
	}
	first := parseFixed(t, r.out)
	if first.what != "new" || first.findings != "2 findings" || first.warnings != "1 warnings" {
		t.Fatalf("first run = %+v, want new with 2 findings and 1 warnings", first)
	}
	it := d.get(first.id)
	for _, tag := range []string{"drift", "drift-fix", "release:v9.9.9"} {
		if !slices.Contains(it.Tags, tag) {
			t.Errorf("fix item tags %v lack %s", it.Tags, tag)
		}
	}
	if it.Category != "chore" || it.Workflow != "drift" || it.Parent != "" || it.Title != "Drift fix: v9.9.9" {
		t.Errorf("fix item: category %q, workflow %q, parent %q, title %q", it.Category, it.Workflow, it.Parent, it.Title)
	}
	if len(it.Criteria) != 1 || !strings.Contains(it.Criteria[0], "drift-warning lines never hold this item open") {
		t.Errorf("fix item criteria %q", it.Criteria)
	}
	findings, warnings := bodySection(it.Body, "Findings"), bodySection(it.Body, "Warnings")
	if got := bodyLines(findings, "drift-key: "); !slices.Equal(got, []string{commandKey, constitutionKey}) {
		t.Errorf("## Findings keys %q, want the command and constitution keys:\n%s", got, it.Body)
	}
	if got := bodyLines(warnings, "drift-warning: "); !slices.Equal(got, []string{proseKey}) {
		t.Errorf("## Warnings %q, want the prose key:\n%s", got, it.Body)
	}
	for _, l := range strings.Split(strings.TrimSpace(driftFindings), "\n") {
		if !strings.Contains(it.Body, "\n> "+l+"\n") {
			t.Errorf("the body does not quote %q:\n%s", l, it.Body)
		}
	}

	r = d.driftFix(driftFindings+flagLine+"\n", "9.9.9")
	again := parseFixed(t, r.out)
	if r.code != 0 || again.id != first.id || again.what != "updated" || again.findings != "3 findings" {
		t.Fatalf("rerun = %+v (exit %d), want %s updated with 3 findings\n%s", again, r.code, first.id, r.err)
	}
	if got := bodyLines(d.get(first.id).Body, "drift-key: "); len(got) != 3 || !slices.Contains(got, flagKey) {
		t.Errorf("after the rerun drift-keys %q, want three with %s", got, flagKey)
	}
	if n := len(d.openDrift()); n != 1 {
		t.Fatalf("%d open drift items after the rerun, want 1", n)
	}

	if r := d.driftFix("", "9.9.9"); r.code != 0 || r.out != "" {
		t.Fatalf("empty input: exit %d %q\n%s", r.code, r.out, r.err)
	}
	if n := len(d.openDrift()); n != 1 {
		t.Fatalf("%d open drift items after empty input, want 1", n)
	}

	r = d.driftFix(flagLine+"\n", "9.9.10")
	other := parseFixed(t, r.out)
	if r.code != 0 || other.id == first.id || other.what != "new" {
		t.Fatalf("9.9.10 = %+v (exit %d), want a second new item\n%s", other, r.code, r.err)
	}

	if r := d.driftFix(".satelle/skills/a.md:3: bogus: x — y\n"+driftFindings, "9.9.11"); r.code != 2 || !strings.Contains(r.err, "unknown kind bogus") {
		t.Fatalf("unknown kind: exit %d, want 2\n%s%s", r.code, r.out, r.err)
	}
	if n := len(d.openDrift()); n != 2 {
		t.Fatalf("%d open drift items after an unknown kind, want 2", n)
	}
}

// A judge-only finding (kind prose) is a warning in the fix item, never a
// drift-key (sto_a1681009 AC7).
func TestDriftFixWarnings(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	const prose = `.satelle/principles/p.md:6: prose: queue entries live in the story chain — internal/kernel/queue.go:62`
	r := d.driftFix(prose+"\n", "9.9.9")
	f := parseFixed(t, r.out)
	if r.code != 0 || f.findings != "0 findings" || f.warnings != "1 warnings" {
		t.Fatalf("prose only = %+v (exit %d), want 0 findings and 1 warnings\n%s", f, r.code, r.err)
	}
	body := d.get(f.id).Body
	if got := bodyLines(bodySection(body, "Warnings"), "drift-warning: "); !slices.Equal(got, []string{proseKey}) {
		t.Errorf("## Warnings %q, want %s:\n%s", got, proseKey, body)
	}
	if !strings.Contains(bodySection(body, "Warnings"), "\n> "+prose+"\n") {
		t.Errorf("## Warnings does not quote the finding:\n%s", body)
	}
	if strings.Contains(body, "drift-key: prose:") || strings.TrimSpace(bodySection(body, "Findings")) != "none" {
		t.Errorf("a prose finding became a drift-key, or ## Findings is not none:\n%s", body)
	}
}

// --keys maps each distinct finding to its key, in order, and refuses a line
// that is not a finding.
func TestDriftFixKeys(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	script, err := filepath.Abs("../scripts/drift-fix.sh")
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

// oldDriftBody is a per-finding drift story's body as scripts/drift-stories.sh
// wrote it: reshaped (the role line and the finding quoted) or not.
func oldDriftBody(key, line string, reshaped bool) string {
	if reshaped {
		return "drift-key: " + key + "\n\nWho is affected: the agent or operator following the file.\n\nThe finding, as the drift review reported it:\n\n> " + line + "\n\nraised by the drift review of v1.0.20\n"
	}
	return "drift-key: " + key + "\n\n" + line + "\n\nraised by the drift review of v1.0.20\n"
}

// The open drift stories are consolidated into one fix item: --open-findings
// fed to drift-fix.sh <version>, then --close <fix>, closes every original on
// the drift workflow through an accepting drift-resolved run (carried: the fix
// item lists its key), reselects one its chore workflow never moved first,
// and leaves one chore moved on its route, carried; nothing is cancelled and
// only the fix item stays open (sto_a1681009 AC4).
func TestDriftFixConsolidates(t *testing.T) {
	t.Parallel()
	d := newDriftStorySandbox(t)
	d.write(".satelle/workflows/chore.toml", `name = "chore"
[states.backlog]
start = true
[states.work]
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "work"
[[transition]]
from = ["backlog", "work"]
to = "done"
`)
	d.must("validate")
	d.git("add", "-A")
	d.git("commit", "-q", "-m", "chore route")

	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	create := func(title, body string, extra ...string) string {
		t.Helper()
		args := append([]string{"--title", title, "--tag", "drift", "--tag", "release:v1.0.20", "--tag", "epic:hygiene", "--body", body, "--criteria", "the finding is fixed"}, extra...)
		return d.create("story", args...).ID
	}
	reshaped := create("Drift: satelle story doc", oldDriftBody(commandKey, lines[0], true))
	unreshaped := create("Drift: Foo", oldDriftBody(constitutionKey, lines[1], false))
	prose := create("Drift: queue entries", oldDriftBody(proseKey, lines[2], true))
	keyless := create("a drift story with no drift-key line", "no key here\n")
	const unmovedKey, movedKey = "skill:.satelle/skills/u.md:u", "link:.satelle/skills/m.md:gone"
	unmoved := create("Drift: u", "drift-key: "+unmovedKey+"\n\nWho is affected: the operator.\n", "--workflow", "chore")
	moved := create("Drift: m", "drift-key: "+movedKey+"\n\nWho is affected: the operator.\n", "--workflow", "chore")
	d.must("story", "set", moved, "--status", "work")
	originals := []string{reshaped, unreshaped, prose, keyless, unmoved}
	for _, id := range originals[:4] {
		if it := d.get(id); it.Workflow != "drift" {
			t.Fatalf("%s is on %s, want drift", id, it.Workflow)
		}
	}

	// The consolidation, as the operator runs it at release.
	open := d.driftFix("", "--open-findings")
	if open.code != 0 {
		t.Fatalf("--open-findings: exit %d\n%s%s", open.code, open.out, open.err)
	}
	var found []string
	for _, l := range strings.Split(strings.TrimSpace(open.out), "\n") {
		id, line, ok := strings.Cut(l, "\t")
		if !ok || !slices.Contains(append(originals, moved), id) {
			t.Fatalf("--open-findings line %q is not <original id>\\t<finding>", l)
		}
		found = append(found, line)
	}
	for _, want := range []string{lines[0], lines[1], lines[2], keyless + ":1: prose: a drift story with no drift-key line — carried from " + keyless,
		".satelle/skills/u.md:1: skill: u — carried from " + unmoved} {
		if !slices.Contains(found, want) {
			t.Errorf("--open-findings lacks %q in:\n%s", want, open.out)
		}
	}
	r := d.driftFix(strings.Join(found, "\n")+"\n", "1.0.48")
	if r.code != 0 {
		t.Fatalf("drift-fix.sh 1.0.48: exit %d\n%s%s", r.code, r.out, r.err)
	}
	fix := parseFixed(t, r.out).id
	closed := d.driftFix("", "--close", fix)
	if closed.code != 0 {
		t.Fatalf("--close: exit %d\n%s%s", closed.code, closed.out, closed.err)
	}

	fixBody := d.get(fix).Body
	for _, k := range []string{commandKey, constitutionKey, unmovedKey, movedKey} {
		if !strings.Contains(bodySection(fixBody, "Findings"), "drift-key: "+k+"\n") {
			t.Errorf("the fix item lacks drift-key %s:\n%s", k, fixBody)
		}
	}
	for _, k := range []string{proseKey, "prose:" + keyless + ":a drift story with no drift-key line"} {
		if !strings.Contains(bodySection(fixBody, "Warnings"), "drift-warning: "+k+"\n") {
			t.Errorf("the fix item lacks drift-warning %s:\n%s", k, fixBody)
		}
	}
	out := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(closed.out), "\n") {
		id, what, _ := strings.Cut(l, "\t")
		out[id] = what
	}
	for _, id := range originals {
		if !strings.HasPrefix(out[id], "closed") {
			t.Errorf("%s: --close printed %q, want closed", id, out[id])
		}
		it := d.get(id)
		if it.Workflow != "drift" || it.Status != "done" || !slices.Contains(it.Tags, "drift-carried-by:"+fix) {
			t.Errorf("%s is at %s/%s with tags %v; want drift/done tagged drift-carried-by:%s", id, it.Workflow, it.Status, it.Tags, fix)
		}
		if g := latestRun(d.sandbox, id, "drift-resolved"); g.Result != "accept" {
			t.Errorf("%s: drift-resolved = %s (%q), want accept", id, g.Result, g.Notes)
		}
	}
	if !strings.HasPrefix(out[moved], "carried\ton chore/work") {
		t.Errorf("%s: --close printed %q, want carried on chore/work", moved, out[moved])
	}
	if it := d.get(moved); it.Workflow != "chore" || it.Status != "work" || slices.Contains(it.Tags, "drift") || !slices.Contains(it.Tags, "drift-carried-by:"+fix) {
		t.Errorf("the moved story is at %s/%s with tags %v; want chore/work, no drift tag, drift-carried-by:%s", it.Workflow, it.Status, it.Tags, fix)
	}
	for _, id := range append(originals, moved, fix) {
		for _, g := range d.gateRuns(id) {
			if g.Gate == "cancel-review" {
				t.Errorf("%s ran cancel-review: nothing is cancelled", id)
			}
		}
	}
	if its := d.openDrift(); len(its) != 1 || its[0].ID != fix {
		t.Fatalf("open drift items after the consolidation: %d, want only the fix item %s", len(its), fix)
	}

	// A rerun is idempotent: the fix item carries itself, nothing else is open.
	again := d.driftFix("", "--open-findings")
	var lines2 []string
	for _, l := range strings.Split(strings.TrimSpace(again.out), "\n") {
		_, line, _ := strings.Cut(l, "\t")
		lines2 = append(lines2, line)
	}
	if r := parseFixed(t, d.driftFix(strings.Join(lines2, "\n")+"\n", "1.0.48").out); r.id != fix || r.what != "updated" {
		t.Fatalf("rerun = %+v, want %s updated", r, fix)
	}
	if r := d.driftFix("", "--close", fix); r.code != 0 || strings.TrimSpace(r.out) != "" {
		t.Fatalf("rerun --close: exit %d %q, want nothing to close", r.code, r.out)
	}
}
