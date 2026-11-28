package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func mapCheckouts(t *testing.T, root string, names ...string) {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		dir := t.TempDir()
		gittest.Git(t, dir, "init", "-q", "-b", "main")
		gittest.Git(t, dir, "remote", "add", "origin", "https://github.com/o/"+n)
		write(t, filepath.Join(dir, "a.txt"), n+"\n")
		gittest.Git(t, dir, "add", "-A")
		gittest.Git(t, dir, "commit", "-q", "-m", "init")
		gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", gittest.Git(t, dir, "rev-parse", "HEAD"))
		b.WriteString("[repos." + n + "]\npath = \"" + strings.ReplaceAll(strings.ReplaceAll(dir, `\`, `\\`), `"`, `\"`) + "\"\n")
	}
	write(t, filepath.Join(root, reporeg.LocalFile), b.String())
}

func TestSkillSource(t *testing.T) {
	shared := map[string]workflow.Skill{"rev": {Name: "rev", Body: "SHARED"}, "perf": {Name: "perf", Body: "SHARED"}}
	parts := map[string]map[string]workflow.Skill{"x": {"rev": {Name: "rev", Body: "PART"}}, "y": {}}
	same := func(a, b map[string]workflow.Skill) bool {
		return reflect.ValueOf(a).UnsafePointer() == reflect.ValueOf(b).UnsafePointer()
	}
	for _, target := range []string{"self", "unknown", "y"} {
		if src := skillSource(parts, target); src != "" {
			t.Errorf("source of %s = %q", target, src)
		}
		if !same(skillsFor(shared, parts, target), shared) {
			t.Errorf("skills of %s are not the shared map", target)
		}
	}
	if src := skillSource(parts, "x"); src != "x" {
		t.Errorf("source of x = %q", src)
	}
	got := skillsFor(shared, parts, "x")
	if same(got, shared) || got["rev"].Body != "PART" || got["perf"].Body != "SHARED" || shared["rev"].Body != "SHARED" {
		t.Errorf("skills of x = %+v, shared %+v", got, shared)
	}
	c := &Config{Skills: shared, Parts: map[string]*Part{"x": {Name: "x", Skills: parts["x"]}, "y": {Name: "y"}}}
	for _, part := range []string{"", "y", "nope"} {
		if s, ok := c.skillOf(part, "rev"); !ok || s.Body != "SHARED" {
			t.Errorf("skillOf(%q, rev) = %+v %v", part, s, ok)
		}
	}
	if s, ok := c.skillOf("x", "rev"); !ok || s.Body != "PART" {
		t.Errorf("skillOf(x, rev) = %+v %v", s, ok)
	}
	if s, ok := c.skillOf("x", "perf"); !ok || s.Body != "SHARED" {
		t.Errorf("skillOf(x, perf) = %+v %v", s, ok)
	}
	inner := strings.Repeat("a", 64)
	if sourcedCoverage(inner, "") != inner || sourcedCoverage(inner, "x") == inner || sourcedCoverage(inner, "x") == sourcedCoverage(inner, "x2") {
		t.Error("sourcedCoverage does not keep an empty source and separate the others")
	}
}

const partOnlyInfra = partInfra + `[gates.xg]
skill = "xonly"
[[transition]]
from = ["intake"]
to = "shipped"
groups = [{ mode = "sequential", gates = ["xg"] }]
`

func TestPartWorkflowNamesPartOnlySkill(t *testing.T) {
	root := partRoot(t, true)
	mapCheckouts(t, root, "x", "y")
	writePart(t, root)
	write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"), partOnlyInfra)
	write(t, filepath.Join(root, PartsDir, "x", SkillsDir, "xonly.md"), "---\ntype: check\n---\n```sh\necho xonly-ran\n```\n")
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Parts["x"]; p == nil || len(p.Problems) > 0 {
		t.Fatalf("part x %+v", p)
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, ok := k.Workflows["x/infra"]; !ok {
		t.Fatal("x/infra did not merge")
	}
	if errs := k.Validate(); len(errs) > 0 {
		t.Errorf("validate: %v", errors.Join(errs...))
	}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me")
	if err != nil || it.Workflow != "x/infra" {
		t.Fatalf("create %s %v", it.Workflow, err)
	}
	res, err := k.Transition(context.Background(), it.ID, "shipped", "me")
	if err != nil || !res.Moved || len(res.Gates) != 1 || res.Gates[0].Notes != "xonly-ran" {
		t.Errorf("x/infra gate: %v %+v", err, res.Gates)
	}
	k.Close()

	write(t, filepath.Join(root, WorkflowsDir, "shared.toml"), "[states.a]\nstart = true\n[states.b]\nterminal = true\n[gates.g]\nskill = \"xonly\"\n[[transition]]\nfrom = [\"a\"]\nto = \"b\"\ngroups = [{ mode = \"sequential\", gates = [\"g\"] }]\n")
	cfg, err = LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if all := errors.Join(cfg.Problems()...); all == nil || !strings.Contains(all.Error(), "workflow shared: gate \"g\": skill \"xonly\" is not in skills/") {
		t.Errorf("a shared workflow naming a part-only skill: %v", all)
	}
	if k, err := Open(Options{Root: root}); err == nil {
		k.Close()
		t.Error("open accepted a shared workflow naming a part-only skill")
	}
}

func TestPartSource(t *testing.T) {
	c := &Config{Parts: map[string]*Part{
		"s":      {Name: "s", Skills: map[string]workflow.Skill{"rev": {Name: "rev"}}, Digest: "ds"},
		"r":      {Name: "r", RuleFiles: RuleFiles{Constitution: &ruleFile{Path: "repos/r/constitution.md"}}, Digest: "dr"},
		"y":      {Name: "y", Digest: "dy"},
		"broken": {Name: "broken", Skills: map[string]workflow.Skill{"rev": {Name: "rev"}}, Problems: []error{errors.New("p")}},
		"self":   {Name: "self", RuleFiles: RuleFiles{Constitution: &ruleFile{Path: "c"}}, Digest: "dself"},
	}}
	sources := c.PartSources()
	for target, want := range map[string]string{"r": "r:dr", "s": "s:ds", "self": "", "y": "", "broken": "", "unknown": ""} {
		if got := partSource(sources, target); got != want {
			t.Errorf("partSource(%s) = %q, want %q", target, got, want)
		}
	}
	a := filesDigest(map[string][]byte{"skills/rev.md": []byte("R"), "constitution.md": []byte("C")})
	for _, other := range []map[string][]byte{
		{"skills/rev.md": []byte("R")},
		{"skills/rev.md": []byte("R"), "constitution.md": []byte("C2")},
		{"skills/rev.md": []byte("R"), "principles/constitution.md": []byte("C")},
		{"skills/rev.mdconstitution.md": []byte("RC")},
	} {
		if filesDigest(other) == a {
			t.Errorf("digest of %q equals the digest of a different file set", other)
		}
	}
}
