//go:build !windows

package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const skillFlow = `
[states.open]
start = true
[states.work]
performer = { agent = "doer", skill = "perf" }
[states.closed]
terminal = true

[gates.chk]
skill = "chk"
[gates.rev]
skill = "rev"
agent = "r1"
after = ["only"]
carry = true

[[transition]]
from = ["open"]
to = "work"
groups = [{ mode = "sequential", gates = ["chk", "rev"] }]
[[transition]]
from = ["work"]
to = "closed"
groups = [{ mode = "sequential", gates = ["rev"] }]
`

const skillRepos = "[repos.x]\norigin = \"https://github.com/o/x\"\n[repos.x2]\norigin = \"https://github.com/o/x2\"\n[repos.y]\norigin = \"https://github.com/o/y\"\n"

func skillRoot(t *testing.T, repos string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, SettingsFile), "[workflows]\ndefault = \"flow\"\n")
	write(t, filepath.Join(root, "agents.toml"), "[agents.doer]\ncommand = \"claude\"\nmodel = \"big\"\ntools = [\"Read\"]\n[agents.r1]\ncommand = \"claude\"\nmodel = \"m1\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, WorkflowsDir, "flow.toml"), skillFlow)
	write(t, filepath.Join(root, SkillsDir, "chk.md"), "---\ntype: check\n---\n```sh\necho shared-chk\n```\n")
	write(t, filepath.Join(root, SkillsDir, "rev.md"), "SHARED-REV\n")
	write(t, filepath.Join(root, SkillsDir, "perf.md"), "SHARED-PERF\n")
	write(t, filepath.Join(root, SkillsDir, "only.md"), "---\ntype: check\n---\n```sh\necho shared-only\n```\n")
	if repos != "" {
		write(t, filepath.Join(root, reporeg.TrackedFile), repos)
		mapCheckouts(t, root, "x", "x2", "y")
	}
	return root
}

func writePartSkills(t *testing.T, root, x string) {
	t.Helper()
	dir := filepath.Join(root, PartsDir, x, SkillsDir)
	write(t, filepath.Join(dir, "chk", "SKILL.md"), "---\ntype: check\n---\n```sh\nprintf 'part-chk %s' \"$SATELLE_SKILL_DIR\"\n```\n")
	write(t, filepath.Join(dir, "rev.md"), "PART-REV\n")
	write(t, filepath.Join(dir, "perf.md"), "PART-PERF\n")
}

func openSkills(t *testing.T, root string) (*Kernel, *fakeDispatcher) {
	t.Helper()
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept, "doer": "did"}}
	k.Dispatcher = d
	k.Git = fakeGit{changed: []string{"src/a.go"}}
	return k, d
}

func lastSystem(d *fakeDispatcher, role agent.Role) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(d.calls[i], string(role)+":") {
			return d.reqs[i].System
		}
	}
	return ""
}

func newSkillItem(t *testing.T, k *Kernel, repo string) item.Item {
	t.Helper()
	f := item.Fields{Kind: ptr("story"), Title: ptr("t"), Body: ptr("b")}
	if repo != "" {
		f.Repo = ptr(repo)
	}
	it, err := k.Create(f, "me")
	if err != nil {
		t.Fatalf("create repo %q: %v", repo, err)
	}
	return it
}

type skillRun struct {
	chk, rev, perf, after string
}

func runSkills(t *testing.T, k *Kernel, d *fakeDispatcher, repo string) skillRun {
	t.Helper()
	ctx := context.Background()
	it := newSkillItem(t, k, repo)
	res, err := k.Transition(ctx, it.ID, "work", "me")
	if err != nil || !res.Moved || gates(res) != "chk=accept rev=accept" {
		t.Fatalf("repo %q open->work: %v %s", repo, err, gates(res))
	}
	out := skillRun{chk: res.Gates[0].Notes, rev: lastSystem(d, agent.Judge), after: res.Gates[1].Notes}
	if _, err := k.Perform(ctx, it.ID, "me"); err != nil {
		t.Fatalf("repo %q perform: %v", repo, err)
	}
	out.perf = lastSystem(d, agent.Perform)
	return out
}

func TestPartSkillsServeTheirRepository(t *testing.T) {
	root := skillRoot(t, skillRepos)
	write(t, filepath.Join(root, PartsDir, "x", ".keep"), "")
	writePartSkills(t, root, "x")
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Parts["x"]; p == nil || len(p.Problems) > 0 || len(p.Skills) != 3 {
		t.Fatalf("part x %+v", p)
	}
	k, d := openSkills(t, root)

	got := runSkills(t, k, d, "x")
	wantChk := "part-chk " + filepath.Join(root, PartsDir, "x", SkillsDir, "chk")
	if got.chk != wantChk || got.rev != "PART-REV\n" || got.perf != "PART-PERF\n" || !strings.Contains(got.after, "shared-only") {
		t.Errorf("repo x read %+v, want chk %q, PART-REV, PART-PERF and the shared after", got, wantChk)
	}
	it := newSkillItem(t, k, "x")
	dry, err := k.GateDry(context.Background(), GateDryRequest{Workflow: "flow", Gate: "rev", Item: it.ID, ShowInput: true})
	if err != nil || dry.Input == nil || !strings.HasPrefix(dry.Input.SkillPath, filepath.Join(root, PartsDir, "x", SkillsDir)+string(filepath.Separator)) {
		t.Errorf("dry input for repo x: %+v %v", dry.Input, err)
	}

	for _, repo := range []string{"", "self", "y"} {
		got := runSkills(t, k, d, repo)
		if got.chk != "shared-chk" || got.rev != "SHARED-REV\n" || got.perf != "SHARED-PERF\n" || !strings.Contains(got.after, "shared-only") {
			t.Errorf("repo %q read %+v, want the shared skills", repo, got)
		}
	}
}

func TestPartSkillCarryFollowsRepo(t *testing.T) {
	root := skillRoot(t, skillRepos)
	writePartSkills(t, root, "x")
	k, d := openSkills(t, root)
	ctx := context.Background()
	for _, c := range []struct {
		from, to, want, system string
	}{
		{"self", "x", gate.Accept, "PART-REV\n"},
		{"self", "", gate.Carried, ""},
		{"x", "", gate.Carried, ""},
		{"y", "x2", gate.Carried, ""},
		{"y", "x", gate.Accept, "PART-REV\n"},
		{"x", "self", gate.Accept, "SHARED-REV\n"},
	} {
		it := newSkillItem(t, k, c.from)
		if res, err := k.Transition(ctx, it.ID, "work", "me"); err != nil || !res.Moved {
			t.Fatalf("%s open->work: %v %s", c.from, err, gates(res))
		}
		if c.to != "" {
			if _, err := k.Update(it.ID, item.Fields{Repo: ptr(c.to)}, "me"); err != nil {
				t.Fatalf("repo %s -> %s: %v", c.from, c.to, err)
			}
		}
		d.mu.Lock()
		d.calls, d.reqs = nil, nil
		d.mu.Unlock()
		res, err := k.Transition(ctx, it.ID, "closed", "me")
		if err != nil || len(res.Gates) != 1 || res.Gates[0].Result != c.want {
			t.Errorf("%s -> %q work->closed: %v %s, want rev=%s", c.from, c.to, err, gates(res), c.want)
			continue
		}
		if c.want == gate.Carried && (res.Gates[0].CarriedFrom == nil || res.Gates[0].CarriedFrom.Transition != "open->work") {
			t.Errorf("%s -> %q carried from %+v", c.from, c.to, res.Gates[0].CarriedFrom)
		}
		if c.system != "" && lastSystem(d, agent.Judge) != c.system {
			t.Errorf("%s -> %q judge read %q, want %q", c.from, c.to, lastSystem(d, agent.Judge), c.system)
		}
	}
}

func innerCoverage(it item.Item, diff string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", it.Body, strings.Join(it.Criteria, "\x1f"))
	h.Write([]byte(diff))
	return hex.EncodeToString(h.Sum(nil))
}

func TestCoverageSkillSourceIsDomainSeparated(t *testing.T) {
	root := skillRoot(t, skillRepos)
	writePartSkills(t, root, "x")
	writePartSkills(t, root, "x2")
	k, d := openSkills(t, root)
	sp := span{base: "b0", git: k.Git}
	paths := []string{"src/a.go"}
	diff := "diff:src/a.go"
	cov := func(it item.Item) string {
		t.Helper()
		c, _, err := k.coverage(it, nil, sp, paths)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	forged := item.Item{ID: "a", Body: "B\x00C", Criteria: []string{"skills=x"}}
	real := item.Item{ID: "b", Body: "B", Criteria: []string{"C"}, Repo: "x"}
	if cov(forged) == cov(real) {
		t.Error("a self item's body and criteria forge a repo x coverage")
	}
	forged.Repo = "y"
	if cov(forged) == cov(real) {
		t.Error("a repo y item's body and criteria forge a repo x coverage")
	}
	inner := innerCoverage(real, diff)
	s := sha256.Sum256([]byte("satelle-part-skills\x1f" + k.PartSources["x"] + "\x1f" + inner))
	if got := cov(real); got != hex.EncodeToString(s[:]) {
		t.Errorf("repo x coverage %s, want the domain-separated %x", got, s)
	}
	other := real
	other.Repo = "x2"
	if cov(other) == cov(real) {
		t.Error("repos x and x2 share a coverage")
	}

	ctx := context.Background()
	a, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Body: ptr("B\x00C"), Criteria: &[]string{"skills=x"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, a.ID, "work", "me"); err != nil || gates(res) != "chk=accept rev=accept" {
		t.Fatalf("forged open->work: %v %s", err, gates(res))
	}
	b, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("b"), Body: ptr("B"), Criteria: &[]string{"C"}, Repo: ptr("x")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(ctx, b.ID, "work", "me")
	if err != nil || gates(res) != "chk=accept rev=accept" || lastSystem(d, agent.Judge) != "PART-REV\n" {
		t.Errorf("repo x open->work: %v %s %q", err, gates(res), lastSystem(d, agent.Judge))
	}
}

func TestNoPartsDirectorySkillsAsToday(t *testing.T) {
	for _, c := range []struct {
		name, repos string
		partDir     bool
	}{
		{"no registry", "", false},
		{"registry", skillRepos, false},
		{"part without skills", skillRepos, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := skillRoot(t, c.repos)
			if c.partDir {
				write(t, filepath.Join(root, PartsDir, "x", ".keep"), "")
			}
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.PartSkills() != nil || (!c.partDir && len(cfg.Parts) > 0) {
				t.Fatalf("parts %+v part skills %+v", cfg.Parts, cfg.PartSkills())
			}
			k, d := openSkills(t, root)
			repos := []string{"", "self"}
			if c.repos != "" {
				repos = append(repos, "y", "x")
			}
			for _, repo := range repos {
				it := newSkillItem(t, k, repo)
				skills, err := k.SkillsFor(it)
				if err != nil || reflect.ValueOf(skills).UnsafePointer() != reflect.ValueOf(k.Skills).UnsafePointer() {
					t.Errorf("repo %q skills are not the shared map: %v", repo, err)
				}
				got, _, err := k.coverage(it, nil, span{base: "b0", git: k.Git}, []string{"src/a.go"})
				if err != nil || got != innerCoverage(it, "diff:src/a.go") {
					t.Errorf("repo %q coverage %s is not today's hash: %v", repo, got, err)
				}
				run := runSkills(t, k, d, repo)
				if run.chk != "shared-chk" || run.rev != "SHARED-REV\n" || run.perf != "SHARED-PERF\n" {
					t.Errorf("repo %q read %+v", repo, run)
				}
				done, err := k.Transition(context.Background(), it.ID, "work", "me")
				if err != nil || !done.Moved {
					t.Fatal(err)
				}
				res, err := k.Transition(context.Background(), it.ID, "closed", "me")
				if err != nil || gates(res) != "rev=carried" {
					t.Errorf("repo %q carry: %v %s", repo, err, gates(res))
				}
			}
		})
	}
}

func TestPartSkillProblems(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, filepath.Join(outside, "rev.md"), "SECRET-OUTSIDE\n")
	write(t, filepath.Join(outside, "chk", "SKILL.md"), "---\ntype: check\n---\n```sh\necho SECRET-OUTSIDE\n```\n")
	write(t, filepath.Join(outside, "run.sh"), "SECRET-OUTSIDE\n")
	link := func(t *testing.T, target, at string) {
		t.Helper()
		if err := os.RemoveAll(at); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, at); err != nil {
			t.Skip(err)
		}
	}
	skills := func(root string) string { return filepath.Join(root, PartsDir, "x", SkillsDir) }
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{"skills link", func(t *testing.T, root string) {
			link(t, outside, skills(root))
		}, "repos/x/skills: not a directory"},
		{"skill file link", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "rev.md"), filepath.Join(skills(root), "rev.md"))
		}, "repos/x/skills: \"rev.md\" is not a regular file or directory"},
		{"skill dir file link", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "chk", "SKILL.md"), filepath.Join(skills(root), "chk", "SKILL.md"))
		}, "repos/x/skills/chk: SKILL.md is not a regular file or directory"},
		{"skill script link", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "run.sh"), filepath.Join(skills(root), "chk", "scripts", "run.sh"))
		}, "repos/x/skills/chk: \"scripts/run.sh\" is not a regular file or directory"},
		{"skills a file", func(t *testing.T, root string) {
			if err := os.RemoveAll(skills(root)); err != nil {
				t.Fatal(err)
			}
			write(t, skills(root), "x")
		}, "repos/x/skills: not a directory"},
		{"invalid stem", func(t *testing.T, root string) {
			write(t, filepath.Join(skills(root), "a\x1bb.md"), "body\n")
		}, "is not a valid skill name"},
		{"duplicate", func(t *testing.T, root string) {
			write(t, filepath.Join(skills(root), "rev", "SKILL.md"), "PART-REV\n")
		}, "repos/x/skills: skill rev: duplicate skill defined at both skills/rev.md and skills/rev/SKILL.md"},
		{"override changes type", func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(skills(root), "chk")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(skills(root), "chk.md"), "---\ntype: principle\n---\nno check\n")
		}, "repos/x/skills: skill chk is type \"principle\" after \"\" but the shared skill it overrides is type \"check\" after \"\"; an override keeps its type and after"},
		{"override changes after", func(t *testing.T, root string) {
			write(t, filepath.Join(skills(root), "only.md"), "---\ntype: check\nafter: always\n---\n```sh\necho part-only\n```\n")
		}, "an override keeps its type and after"},
		{"check without a block", func(t *testing.T, root string) {
			write(t, filepath.Join(skills(root), "lone.md"), "---\ntype: check\n---\nno block\n")
		}, "repos/x/skills: skill lone: a check needs a ```sh or ```check block"},
		{"legacy answer", func(t *testing.T, root string) {
			write(t, filepath.Join(skills(root), "rev.md"), "Answer with {\"decision\":\"accept\"}.\n")
		}, "repos/x/skills: workflow flow: gate \"rev\": skill \"rev\" answers with"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := skillRoot(t, skillRepos)
			writePartSkills(t, root, "x")
			k, _ := openSkills(t, root)
			early := newSkillItem(t, k, "x")
			k.Close()
			c.setup(t, root)
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if ps := cfg.Problems(); len(ps) > 0 {
				t.Fatalf("shared problems: %v", errors.Join(ps...))
			}
			ps := cfg.Unresolved()["x"]
			all := errors.Join(ps...).Error()
			if len(ps) == 0 || !strings.Contains(all, c.want) {
				t.Fatalf("part problems %q, want %q", all, c.want)
			}
			if strings.Contains(all, "SECRET-OUTSIDE") || strings.Contains(all, "\x1b") || strings.Contains(all, root) {
				t.Errorf("a problem shows outside, unsafe or absolute content: %q", all)
			}
			if errs := Validate(root); len(errs) > 0 {
				t.Errorf("validate: %v", errors.Join(errs...))
			}
			k, d := openSkills(t, root)
			for _, repo := range []string{"self", "y"} {
				if got := runSkills(t, k, d, repo); got.chk != "shared-chk" || got.perf != "SHARED-PERF\n" {
					t.Errorf("repo %s read %+v", repo, got)
				}
			}
			var refused *PartUnresolved
			if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Repo: ptr("x")}, "me"); !errors.As(err, &refused) {
				t.Errorf("create --repo x: %v", err)
			}
			if _, err := k.Seat(early.ID, "s", LocalLocation(t.TempDir()), "", "me"); !errors.As(err, &refused) {
				t.Errorf("seat: %v", err)
			}
			if _, err := k.Perform(context.Background(), early.ID, "me"); !errors.As(err, &refused) {
				t.Errorf("perform: %v", err)
			}
			if _, err := k.Transition(context.Background(), early.ID, "work", "me"); !errors.As(err, &refused) {
				t.Errorf("transition: %v", err)
			}
			if _, err := k.GateDry(context.Background(), GateDryRequest{Workflow: "flow", Gate: "rev", Item: early.ID}); !errors.As(err, &refused) {
				t.Errorf("dry gate: %v", err)
			}
			if sk, err := k.SkillsFor(early); !errors.As(err, &refused) || sk != nil {
				t.Errorf("skills for repo x: %v %v", len(sk), err)
			}
		})
	}
}
