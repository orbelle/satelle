//go:build integration && unix

package tests

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// The findings fed to drift-stories.sh: one substrate (command), one
// constitution and one prose finding.
const driftFindings = `.satelle/skills/a.md:3: command: satelle story doc — doc is not a subcommand of satelle story
internal/x.go:3: constitution: satelle-operator-rulings Foo — a REST handler beside gRPC
.satelle/principles/p.md:6: prose: queue entries live in the story chain — internal/kernel/queue.go:62
`

type storiesSandbox struct {
	*sandbox
	hygiene, constitution string // epic ids
}

// newStoriesSandbox has an open hygiene epic, an open constitution epic and an
// open ordinary story that carries epic:hygiene but not the bare epic tag, as
// an epic's child does.
func newStoriesSandbox(t *testing.T) *storiesSandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by scripts/drift-stories.sh and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	s.env["SATELLE_BIN"] = satelleBin
	s.must("init")
	ss := &storiesSandbox{sandbox: s}
	ss.hygiene = s.create("story", "--title", "Epic: hygiene", "--tag", "epic", "--tag", "epic:hygiene").ID
	ss.constitution = s.create("story", "--title", "Epic: constitution drift", "--tag", "epic", "--tag", "epic:constitution").ID
	s.create("story", "--title", "a hygiene child", "--tag", "epic:hygiene")
	return ss
}

func (s *storiesSandbox) raise(stdin string) result {
	s.t.Helper()
	script, err := filepath.Abs("../scripts/drift-stories.sh")
	if err != nil {
		s.t.Fatal(err)
	}
	return s.exec("bash", stdin, script, "9.9.9")
}

// drifts returns the open stories tagged drift.
func (s *storiesSandbox) drifts() []item.Item {
	s.t.Helper()
	var its []item.Item
	s.mustJSON(&its, "story", "list", "--tag", "drift", "--open")
	return its
}

// raised parses the script's output: key -> (id, new|existing).
func raised(t *testing.T, out string) map[string][2]string {
	t.Helper()
	m := map[string][2]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			t.Fatalf("output line %q is not <key>\\t<id>\\t<new|existing>", l)
		}
		m[f[0]] = [2]string{f[1], f[2]}
	}
	return m
}

const (
	commandKey      = "command:.satelle/skills/a.md:satelle story doc"
	constitutionKey = "constitution:internal/x.go:satelle-operator-rulings Foo"
	proseKey        = "prose:.satelle/principles/p.md:queue entries live in the story chain"
)

// One story per finding: tagged drift and release:v<version>, parented on the
// epic of its kind and carrying that epic's tag, with its drift-key in the
// body; chore for a .satelle/ finding that is not kind constitution.
func TestDriftStoriesRaisesOnePerFinding(t *testing.T) {
	t.Parallel()
	s := newStoriesSandbox(t)
	r := s.raise(driftFindings)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	out := raised(t, r.out)
	stories := s.drifts()
	if len(out) != 3 || len(stories) != 3 {
		t.Fatalf("want 3 new stories, got %d lines and %d stories:\n%s", len(out), len(stories), r.out)
	}
	byID := map[string]item.Item{}
	for _, it := range stories {
		byID[it.ID] = it
	}
	for key, want := range map[string]struct{ epic, tag, category string }{
		commandKey:      {s.hygiene, "epic:hygiene", "chore"},
		proseKey:        {s.hygiene, "epic:hygiene", "chore"},
		constitutionKey: {s.constitution, "epic:constitution", ""},
	} {
		got, ok := out[key]
		if !ok || got[1] != "new" {
			t.Errorf("%s: %v, want a new story", key, got)
			continue
		}
		it := byID[got[0]]
		for _, tag := range []string{"drift", "release:v9.9.9", want.tag} {
			if !slices.Contains(it.Tags, tag) {
				t.Errorf("%s (%s) tags %v lack %s", key, it.ID, it.Tags, tag)
			}
		}
		if it.Parent != want.epic {
			t.Errorf("%s (%s) parent %q, want %s", key, it.ID, it.Parent, want.epic)
		}
		if it.Category != want.category {
			t.Errorf("%s (%s) category %q, want %q", key, it.ID, it.Category, want.category)
		}
		if !strings.Contains(it.Body, "drift-key: "+key+"\n") {
			t.Errorf("%s (%s) body lacks its drift-key:\n%s", key, it.ID, it.Body)
		}
	}
}

// A finding an open drift story already carries is not raised again, even
// at another line; the epics still resolve with their drift children open.
func TestDriftStoriesSkipsOpenDuplicate(t *testing.T) {
	t.Parallel()
	s := newStoriesSandbox(t)
	first := s.raise(driftFindings)
	if first.code != 0 {
		t.Fatalf("first run: exit %d\n%s%s", first.code, first.out, first.err)
	}
	again := s.raise(strings.Replace(driftFindings, "a.md:3:", "a.md:30:", 1))
	if again.code != 0 {
		t.Fatalf("second run: exit %d\n%s%s", again.code, again.out, again.err)
	}
	if n := len(s.drifts()); n != 3 {
		t.Fatalf("%d drift stories after the second run, want 3", n)
	}
	was, now := raised(t, first.out), raised(t, again.out)
	for key, f := range was {
		if now[key] != [2]string{f[0], "existing"} {
			t.Errorf("%s: second run %v, want %s existing", key, now[key], f[0])
		}
	}
}

// With the constitution epic closed, a constitution finding stops the script
// before any story is created, naming the tag.
func TestDriftStoriesNoEpicExits2(t *testing.T) {
	t.Parallel()
	s := newStoriesSandbox(t)
	s.must("story", "set", s.constitution, "--status", "done")
	r := s.raise(driftFindings)
	if r.code != 2 || !strings.Contains(r.err, "epic:constitution") {
		t.Fatalf("exit %d, want 2 naming epic:constitution\n%s%s", r.code, r.out, r.err)
	}
	if n := len(s.drifts()); n != 0 {
		t.Fatalf("%d drift stories raised, want none", n)
	}
}

// No findings: no story, no output, exit 0.
func TestDriftStoriesEmptyInput(t *testing.T) {
	t.Parallel()
	s := newStoriesSandbox(t)
	r := s.raise("")
	if r.code != 0 || r.out != "" {
		t.Fatalf("empty input: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := len(s.drifts()); n != 0 {
		t.Fatalf("%d drift stories raised, want none", n)
	}
}

// One finding of every kind drift-stories.sh maps.
const driftEveryKind = `.satelle/skills/a.md:3: command: satelle story doc — doc is not a subcommand of satelle story
.satelle/skills/b.md:4: flag: satelle story list --nope — no such flag
.satelle/skills/c.md:5: topic: satelle help nope — no such help topic
.satelle/skills/d.md:6: link: [[no-such-principle]] — no such principle
.satelle/skills/e.md:7: skill: ghost — no such skill
.satelle/workflows/f.toml:8: config: gates.ghost — no such gate
.satelle/principles/p.md:6: prose: queue entries live in the story chain — internal/kernel/queue.go:62
internal/x.go:3: constitution: satelle-operator-rulings Foo — a REST handler beside gRPC
`

// shapeWant is what a raised story's role line and criterion say for a kind.
func shapeWant(kind, file string) (role, proof string) {
	switch kind {
	case "prose":
		return "Who is affected: the reader of the principle " + file + ",",
			"the next drift run's drift-claims judge no longer raises drift-key"
	case "constitution":
		return "Who is affected: the maintainer of the Go file " + file + ",",
			"the next drift run's drift-constitution judge no longer raises drift-key"
	}
	return "Who is affected: the agent or operator following " + file + ",",
		"scripts/drift-check.sh no longer prints drift-key"
}

// checkShape asserts a drift story is in the story shape for its finding: the
// drift-key line first, the role its kind affects, the finding quoted, and one
// criterion that quotes the finding and names the proof that it is fixed.
func checkShape(t *testing.T, it item.Item, line string) {
	t.Helper()
	file, rest, _ := strings.Cut(line, ":")
	_, rest, _ = strings.Cut(rest, ": ")
	kind, rest, _ := strings.Cut(rest, ": ")
	subject, _, _ := strings.Cut(rest, " — ")
	key := kind + ":" + file + ":" + subject
	role, proof := shapeWant(kind, file)
	if !strings.HasPrefix(it.Body, "drift-key: "+key+"\n") {
		t.Errorf("%s (%s): body does not open with its drift-key:\n%s", kind, it.ID, it.Body)
	}
	if !strings.Contains(it.Body, "\n"+role) {
		t.Errorf("%s (%s): body lacks the role %q:\n%s", kind, it.ID, role, it.Body)
	}
	if !strings.Contains(it.Body, "\n> "+line+"\n") {
		t.Errorf("%s (%s): body does not quote the finding %q:\n%s", kind, it.ID, line, it.Body)
	}
	want := `The finding "` + line + `" is fixed: ` + proof + " `" + key + "`"
	if len(it.Criteria) != 1 || it.Criteria[0] != want {
		t.Errorf("%s (%s): criteria %q, want [%q]", kind, it.ID, it.Criteria, want)
	}
}

// A story raised for each kind names the role the finding affects, quotes the
// finding, and carries the criterion naming its proof.
func TestDriftStoriesStoryShapePerKind(t *testing.T) {
	t.Parallel()
	s := newStoriesSandbox(t)
	r := s.raise(driftEveryKind)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	stories := s.drifts()
	lines := strings.Split(strings.TrimSpace(driftEveryKind), "\n")
	if len(stories) != len(lines) {
		t.Fatalf("%d drift stories, want %d:\n%s", len(stories), len(lines), r.out)
	}
	for _, line := range lines {
		var found *item.Item
		for i := range stories {
			if strings.Contains(stories[i].Body, "\n> "+line+"\n") {
				found = &stories[i]
			}
		}
		if found == nil {
			t.Errorf("no story quotes %q", line)
			continue
		}
		checkShape(t, *found, line)
	}
}

// --reshape rewrites an open drift story of the old shape in backlog to the
// story shape, keeping its drift-key and release; one past backlog is held,
// untouched; a second run leaves the reshaped story as it is.
func TestDriftStoriesReshape(t *testing.T) {
	t.Parallel()
	s := newStoriesSandbox(t)
	s.write(".satelle/workflows/default.toml", `name = "default"

[states.backlog]
start = true

[states.plan]

[states.done]
terminal = true

[[transition]]
from = ["backlog"]
to = "plan"

[[transition]]
from = ["backlog", "plan"]
to = "done"
`)
	old := func(line, key string) item.Item {
		return s.create("story", "--title", "Drift: old", "--tag", "drift", "--tag", "release:v1.0.20",
			"--body", "drift-key: "+key+"\n\n"+line+"\n\nraised by the drift review of v1.0.20\n")
	}
	lines := strings.Split(strings.TrimSpace(driftFindings), "\n")
	keys := []string{commandKey, constitutionKey, proseKey}
	var ids []string
	for i, line := range lines {
		ids = append(ids, old(line, keys[i]).ID)
	}
	held := old(".satelle/skills/z.md:9: link: [[gone]] — no such principle", "link:.satelle/skills/z.md:[[gone]]")
	s.must("story", "set", held.ID, "--status", "plan")

	script, err := filepath.Abs("../scripts/drift-stories.sh")
	if err != nil {
		t.Fatal(err)
	}
	r := s.exec("bash", "", script, "--reshape")
	if r.code != 0 {
		t.Fatalf("reshape: exit %d\n%s%s", r.code, r.out, r.err)
	}
	out := raised(t, r.out)
	for i, key := range keys {
		if out[key] != [2]string{ids[i], "reshaped"} {
			t.Errorf("%s: %v, want %s reshaped", key, out[key], ids[i])
		}
		it := s.get(ids[i])
		checkShape(t, it, lines[i])
		if !strings.HasSuffix(it.Body, "\nraised by the drift review of v1.0.20\n") {
			t.Errorf("%s (%s): reshape lost the release line:\n%s", key, it.ID, it.Body)
		}
	}
	if got := out["link:.satelle/skills/z.md:[[gone]]"]; got != [2]string{held.ID, "held:plan"} {
		t.Errorf("held story: %v, want %s held:plan", got, held.ID)
	}
	if it := s.get(held.ID); it.Body != held.Body || len(it.Criteria) != 0 {
		t.Errorf("held story was changed: %q %q", it.Body, it.Criteria)
	}

	again := s.exec("bash", "", script, "--reshape")
	if again.code != 0 {
		t.Fatalf("second reshape: exit %d\n%s%s", again.code, again.out, again.err)
	}
	for i, key := range keys {
		if got := raised(t, again.out)[key]; got != [2]string{ids[i], "already"} {
			t.Errorf("second reshape %s: %v, want %s already", key, got, ids[i])
		}
	}
	// The reshaped stories still dedup a finding raised again.
	r = s.raise(driftFindings)
	for i, key := range keys {
		if got := raised(t, r.out)[key]; got != [2]string{ids[i], "existing"} {
			t.Errorf("raise after reshape %s: %v, want %s existing", key, got, ids[i])
		}
	}
}
