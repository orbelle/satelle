//go:build integration && unix

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// rehomeMode is the configuration a re-home sandbox ends in.
type rehomeMode int

const (
	rehomeFull       rehomeMode = iota // the repo's drift workflow and rule
	rehomeNoRule                       // the drift rule removed
	rehomeNoWorkflow                   // the drift rule and drift.toml removed
)

// rehomeSandbox is a drift story sandbox with a legacy workflow (backlog,
// work, done, cancelled; backlog -> done gated by legacy-no, a check that
// exits 1 and names no agent, the only stub) and the drift stories the
// re-home one-off works on, each a child of the hygiene epic.
type rehomeSandbox struct {
	*driftSandbox
	hygiene          string
	u, f, g, k, d, x string // story ids; d and x only in rehomeFull
	keyU, keyF, keyG string
	keyD             string         // D's key, which X carries too
	before           map[string]int // chain lengths once every story is built
	legacyStatus     map[string]string
}

func newRehomeSandbox(t *testing.T, mode rehomeMode) *rehomeSandbox {
	t.Helper()
	d := newDriftStorySandbox(t)
	d.write(".satelle/workflows/legacy.toml", `name = "legacy"
[states.backlog]
start = true
[states.work]
[states.done]
terminal = true
[states.cancelled]
terminal = true
[gates.legacy-no]
skill = "legacy-no"
[[transition]]
from = ["backlog"]
to = "work"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["legacy-no"] }]
[[transition]]
from = ["*"]
to = "cancelled"
`)
	d.write(".satelle/skills/legacy-no.md", "---\ntype: check\n---\n```sh\nexit 1\n```\n")
	d.must("validate")
	s := &rehomeSandbox{driftSandbox: d, before: map[string]int{}, legacyStatus: map[string]string{}}
	var epics []item.Item
	d.mustJSON(&epics, "story", "list", "--tag", "epic:hygiene", "--open")
	for _, e := range epics {
		for _, tag := range e.Tags {
			if tag == "epic" {
				s.hygiene = e.ID
			}
		}
	}
	if s.hygiene == "" {
		t.Fatal("the sandbox has no hygiene epic")
	}
	story := func(name, key string, extra ...string) string {
		t.Helper()
		body := "Who is affected: the operator.\n\nstory " + name + "\n"
		if key != "" {
			body = "drift-key: " + key + "\n\n" + body
		}
		args := append([]string{"--title", "Drift: " + name, "--body", body, "--criteria", "the finding " + name + " is fixed",
			"--tag", "drift", "--tag", "release:v1.0.20", "--tag", "epic:hygiene", "--category", "chore", "--parent", s.hygiene}, extra...)
		return d.create("story", args...).ID
	}
	s.keyU, s.keyF, s.keyG, s.keyD = "skill:.satelle/skills/u.md:u", "skill:.satelle/skills/f.md:f", "skill:.satelle/skills/g.md:g", "skill:.satelle/skills/d.md:d"
	s.u = story("U", s.keyU, "--workflow", "legacy")
	s.f = story("F", s.keyF, "--workflow", "legacy")
	d.must("story", "set", s.f, "--status", "work")
	s.g = story("G", s.keyG, "--workflow", "legacy")
	if r := d.run("story", "set", s.g, "--status", "done"); r.code != 2 {
		t.Fatalf("G done: exit %d, want 2 (legacy-no rejects)\n%s%s", r.code, r.out, r.err)
	}
	if runs := d.gateRuns(s.g); len(runs) != 1 || runs[0].Gate != "legacy-no" || runs[0].Result != "reject" {
		t.Fatalf("G's gate runs: %+v", runs)
	}
	s.k = story("K", "", "--workflow", "legacy")
	s.legacyStatus = map[string]string{s.u: "backlog", s.f: "work", s.g: "backlog", s.k: "backlog"}
	if mode == rehomeFull {
		s.d = story("D", s.keyD)
		s.x = story("X", s.keyD, "--workflow", "legacy")
		s.legacyStatus[s.x] = "backlog"
		if it := d.get(s.d); it.Workflow != "drift" || it.Status != "open" {
			t.Fatalf("D is at %s/%s, want drift/open", it.Workflow, it.Status)
		}
	}
	for id, st := range s.legacyStatus {
		if it := d.get(id); it.Workflow != "legacy" || it.Status != st {
			t.Fatalf("%s is at %s/%s, want legacy/%s", id, it.Workflow, it.Status, st)
		}
	}
	switch mode {
	case rehomeNoRule, rehomeNoWorkflow:
		cfg := readSandbox(t, d.sandbox, ".satelle/satelle.toml")
		rule := driftRule(t)
		if !strings.Contains(cfg, rule) {
			t.Fatal("the sandbox's satelle.toml does not carry the drift rule")
		}
		d.write(".satelle/satelle.toml", strings.Replace(cfg, rule, "", 1))
		if mode == rehomeNoWorkflow {
			if err := os.Remove(filepath.Join(d.dir, ".satelle", "workflows", "drift.toml")); err != nil {
				t.Fatal(err)
			}
		}
	}
	d.must("validate")
	d.git("add", "-A")
	d.git("commit", "-q", "-m", "legacy workflow and drift stories")
	for _, id := range s.ids() {
		s.before[id] = len(d.ledger(id, ""))
	}
	return s
}

func (s *rehomeSandbox) ids() []string {
	var out []string
	for _, id := range []string{s.u, s.f, s.g, s.k, s.d, s.x} {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (s *rehomeSandbox) rehome() result {
	s.t.Helper()
	return s.exec("bash", "", "scripts/drift-stories.sh", "--rehome")
}

// unchanged asserts every legacy story is where it was built: on legacy at its
// status, tagged drift, its chain as long as before.
func (s *rehomeSandbox) unchanged() {
	s.t.Helper()
	for id, st := range s.legacyStatus {
		it := s.get(id)
		if it.Workflow != "legacy" || it.Status != st || !hasTag(it, "drift") {
			s.t.Errorf("%s is at %s/%s with tags %v, want legacy/%s tagged drift", id, it.Workflow, it.Status, it.Tags, st)
		}
		if n := len(s.ledger(id, "")); n != s.before[id] {
			s.t.Errorf("%s's chain is %d events, was %d", id, n, s.before[id])
		}
	}
	var all []item.Item
	s.mustJSON(&all, "story", "list")
	for _, it := range all {
		for _, tag := range it.Tags {
			if strings.HasPrefix(tag, "drift-record-of:") {
				s.t.Errorf("%s was re-raised as %s", tag, it.ID)
			}
		}
	}
}

func hasTag(it item.Item, tag string) bool {
	for _, t := range it.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// driftKeyOf is a body's first drift-key line's key, "-" when it has none.
func driftKeyOf(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if k, ok := strings.CutPrefix(l, "drift-key: "); ok {
			return k
		}
	}
	return "-"
}

// The one-off re-homes every open drift story off the drift workflow: an
// untouched story (U, and the keyless K) moves in place to drift/open; one its
// route moved (F) or judged (G) keeps its route and its record is re-raised on
// drift; one whose key already has an open drift story there (X, D's key) is
// only retagged. No story is cancelled, and a second run has nothing to do.
func TestDriftStoriesRehome(t *testing.T) {
	t.Parallel()
	s := newRehomeSandbox(t, rehomeFull)

	if r := s.run("story", "set", s.g, "--reselect", "drift"); r.code != 2 || !strings.Contains(r.err, "gate run") {
		t.Fatalf("G reselect: exit %d, want 2 naming its gate run\n%s%s", r.code, r.out, r.err)
	}
	if n := len(s.ledger(s.g, "")); n != s.before[s.g] {
		t.Fatalf("the refused reselect wrote to G's chain: %d events, was %d", n, s.before[s.g])
	}

	r := s.rehome()
	if r.code != 0 {
		t.Fatalf("--rehome: exit %d\n%s%s", r.code, r.out, r.err)
	}
	lines := map[string][3]string{} // old id -> key, new id, outcome
	for _, l := range strings.Split(strings.TrimSpace(r.out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 4 {
			t.Fatalf("output line %q is not <key>\\t<old>\\t<new>\\t<outcome>", l)
		}
		if _, dup := lines[f[1]]; dup {
			t.Fatalf("%s is reported twice:\n%s", f[1], r.out)
		}
		lines[f[1]] = [3]string{f[0], f[2], f[3]}
	}
	if len(lines) != 5 {
		t.Fatalf("--rehome reported %d stories, want U, K, F, G and X:\n%s", len(lines), r.out)
	}
	want := map[string][2]string{ // key, outcome; the new id is checked below
		s.u: {s.keyU, "moved"}, s.k: {"-", "moved"}, s.f: {s.keyF, "kept:work"}, s.g: {s.keyG, "kept:backlog"}, s.x: {s.keyD, "duplicate"},
	}
	for old, w := range want {
		got, ok := lines[old]
		if !ok || got[0] != w[0] || got[2] != w[1] {
			t.Errorf("%s: reported %v, want key %s and %s", old, got, w[0], w[1])
		}
	}
	if lines[s.u][1] != s.u || lines[s.k][1] != s.k {
		t.Errorf("a moved story was reported under a new id:\n%s", r.out)
	}
	if lines[s.x][1] != s.d {
		t.Errorf("X's duplicate points at %s, want D %s", lines[s.x][1], s.d)
	}

	// U and K: moved in place, the chain grows by exactly the placement pair.
	for _, id := range []string{s.u, s.k} {
		it := s.get(id)
		if it.Workflow != "drift" || it.Status != "open" || it.Parent != s.hygiene || !hasTag(it, "drift") {
			t.Errorf("%s is at %s/%s parent %s tags %v, want drift/open under %s tagged drift", id, it.Workflow, it.Status, it.Parent, it.Tags, s.hygiene)
		}
		evs := s.ledger(id, "")
		if len(evs) != s.before[id]+2 {
			t.Fatalf("%s's chain went from %d to %d events, want +2", id, s.before[id], len(evs))
		}
		var f item.Fields
		var m item.Move
		if evs[len(evs)-2].Kind != item.KindUpdated || json.Unmarshal(evs[len(evs)-2].Payload, &f) != nil || f.Workflow == nil || *f.Workflow != "drift" ||
			evs[len(evs)-1].Kind != item.KindTransitioned || json.Unmarshal(evs[len(evs)-1].Payload, &m) != nil || m.From != "backlog" || m.To != "open" {
			t.Errorf("%s's last two events are %s %s, %s %s; want item.updated {workflow: drift}, item.transitioned {backlog -> open}",
				id, evs[len(evs)-2].Kind, evs[len(evs)-2].Payload, evs[len(evs)-1].Kind, evs[len(evs)-1].Payload)
		}
		if runs := s.gateRuns(id); len(runs) != 0 {
			t.Errorf("%s has gate runs: %+v", id, runs)
		}
	}

	// F and G: keep their route; their records sit on drift.
	for _, c := range []struct{ old, status string }{{s.f, "work"}, {s.g, "backlog"}} {
		old := s.get(c.old)
		rec := lines[c.old][1]
		if old.Workflow != "legacy" || old.Status != c.status || hasTag(old, "drift") || !hasTag(old, "drift-moved:"+rec) {
			t.Errorf("%s is at %s/%s with tags %v, want legacy/%s with drift-moved:%s and no drift", c.old, old.Workflow, old.Status, old.Tags, c.status, rec)
		}
		n := s.get(rec)
		wantTags := append(append([]string{}, old.Tags...), "drift-record-of:"+c.old)
		wantTags = slicesWithout(wantTags, "drift-moved:"+rec)
		wantTags = append(wantTags, "drift")
		if n.Workflow != "drift" || n.Status != "open" || n.Title != old.Title || n.Body != old.Body ||
			!reflect.DeepEqual(n.Criteria, old.Criteria) || n.Parent != old.Parent || n.Category != old.Category || !sameSet(n.Tags, wantTags) {
			t.Errorf("record %s of %s: %+v\nwant drift/open with %s's title, body, criteria, parent, category and tags %v", rec, c.old, n, c.old, wantTags)
		}
	}

	// D unchanged; X only retagged.
	if n := len(s.ledger(s.d, "")); n != s.before[s.d] {
		t.Errorf("D's chain went from %d to %d events", s.before[s.d], n)
	}
	if x := s.get(s.x); x.Workflow != "legacy" || x.Status != "backlog" || hasTag(x, "drift") || !hasTag(x, "drift-moved:"+s.d) {
		t.Errorf("X is at %s/%s with tags %v, want legacy/backlog with drift-moved:%s", x.Workflow, x.Status, x.Tags, s.d)
	}

	// No story is cancelled.
	var all []item.Item
	s.mustJSON(&all, "story", "list")
	for _, it := range all {
		if it.Status == "cancelled" {
			t.Errorf("%s was cancelled", it.ID)
		}
		if hasTag(it, "drift-record-of:"+s.x) {
			t.Errorf("X was re-raised as %s", it.ID)
		}
	}
	for _, e := range s.ledger("", "attachment.added") {
		if strings.Contains(string(e.Payload), "cancel-reason") {
			t.Errorf("a cancel-reason was attached to %s", e.Item)
		}
	}

	// Every open drift story is on drift, one per key.
	var open []item.Item
	s.mustJSON(&open, "story", "list", "--tag", "drift", "--open")
	var keys []string
	for _, it := range open {
		if it.Workflow != "drift" {
			t.Errorf("open drift story %s is on %s", it.ID, it.Workflow)
		}
		if hasTag(it, "epic") {
			continue
		}
		keys = append(keys, driftKeyOf(it.Body))
	}
	sort.Strings(keys)
	if wantKeys := []string{"-", s.keyD, s.keyF, s.keyG, s.keyU}; !reflect.DeepEqual(keys, sortedCopy(wantKeys)) {
		t.Errorf("open drift keys %v, want %v", keys, sortedCopy(wantKeys))
	}

	// A second run has nothing to do and writes nothing.
	total := len(s.ledger("", ""))
	r = s.rehome()
	if r.code != 1 || r.out != "" || !strings.Contains(r.err, "nothing to re-home") {
		t.Fatalf("second --rehome: exit %d, want 1 with no output\n%s%s", r.code, r.out, r.err)
	}
	if n := len(s.ledger("", "")); n != total {
		t.Errorf("the second run wrote: %d events, was %d", n, total)
	}
}

// Without the drift rule the one-off moves nothing anywhere: a reselect names
// the workflow the rules select instead, and --rehome exits 2 before writing.
func TestDriftStoriesRehomeNoRule(t *testing.T) {
	t.Parallel()
	s := newRehomeSandbox(t, rehomeNoRule)
	if r := s.run("story", "set", s.u, "--reselect", "drift"); r.code != 1 || !strings.Contains(r.err, "standing") {
		t.Fatalf("U reselect without the rule: exit %d, want 1 naming standing\n%s%s", r.code, r.out, r.err)
	}
	r := s.rehome()
	if r.code != 2 || !strings.Contains(r.err, "no drift rule") {
		t.Fatalf("--rehome without the rule: exit %d, want 2 naming the missing rule\n%s%s", r.code, r.out, r.err)
	}
	s.unchanged()
}

// Without the drift workflow (and so its rule) --rehome exits 2 and every
// story is unchanged.
func TestDriftStoriesRehomeNoWorkflow(t *testing.T) {
	t.Parallel()
	s := newRehomeSandbox(t, rehomeNoWorkflow)
	r := s.rehome()
	if r.code != 2 || !strings.Contains(r.err, "no drift workflow") {
		t.Fatalf("--rehome without the workflow: exit %d, want 2 naming the missing workflow\n%s%s", r.code, r.out, r.err)
	}
	s.unchanged()
}

func slicesWithout(xs []string, drop string) []string {
	var out []string
	for _, x := range xs {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	return reflect.DeepEqual(sortedCopy(a), sortedCopy(b))
}

func sortedCopy(xs []string) []string {
	out := append([]string{}, xs...)
	sort.Strings(out)
	return out
}
