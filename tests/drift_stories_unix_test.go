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
