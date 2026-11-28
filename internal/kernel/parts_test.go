package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const partInfra = `[states.intake]
start = true
[states.build]
[states.shipped]
terminal = true
[[transition]]
from = ["intake"]
to = "build"
[[transition]]
from = ["build"]
to = "shipped"
`

func partRoot(t *testing.T, withRepos bool) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	toml, err := os.ReadFile(filepath.Join(root, SettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, SettingsFile), string(toml)+"\n[[workflows.rule]]\nworkflow = \"default\"\ncategory = \"c\"\n")
	if withRepos {
		write(t, filepath.Join(root, reporeg.TrackedFile), "[repos.x]\norigin = \"https://github.com/o/x\"\n[repos.y]\norigin = \"https://github.com/o/y\"\n")
	}
	return root
}

func writePart(t *testing.T, root string) {
	t.Helper()
	write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n")
	write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"), partInfra)
}

func openPart(t *testing.T) *Kernel {
	t.Helper()
	root := partRoot(t, true)
	writePart(t, root)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func TestPartWorkflowPlacesItsRepository(t *testing.T) {
	k := openPart(t)
	for _, c := range []struct {
		f           item.Fields
		name, start string
	}{
		{item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "x/infra", "intake"},
		{item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c")}, "default", "backlog"},
		{item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("self")}, "default", "backlog"},
		{item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("y")}, "default", "backlog"},
		{item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("d"), Repo: ptr("x")}, "default", "backlog"},
	} {
		it, err := k.Create(c.f, "me")
		if err != nil || it.Workflow != c.name || it.Status != c.start {
			t.Errorf("create repo %v category %v = %s %s %v, want %s %s", deref(c.f.Repo), deref(c.f.Category), it.Workflow, it.Status, err, c.name, c.start)
		}
	}
	if errs := k.Validate(); len(errs) > 0 {
		t.Errorf("validate: %v", errors.Join(errs...))
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func pullRepo(t *testing.T, k *Kernel, id, repo string) {
	t.Helper()
	if _, err := k.Ledger.Append(id, item.KindUpdated, "remote", item.Fields{Repo: &repo}); err != nil {
		t.Fatal(err)
	}
	if err := k.Cache.SyncItem(k.Ledger, id); err != nil {
		t.Fatal(err)
	}
}

func TestPartWorkflowServesOnlyItsRepository(t *testing.T) {
	k := openPart(t)
	for _, repo := range []string{"self", "y"} {
		_, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Repo: ptr(repo), Workflow: ptr("x/infra")}, "me")
		var mismatch *TargetMismatch
		if !errors.As(err, &mismatch) || mismatch.Part != "x" {
			t.Errorf("create repo %s on x/infra: %v", repo, err)
		}
	}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"self", "y"} {
		if _, err := k.Update(it.ID, item.Fields{Repo: ptr(repo)}, "me"); err == nil || !strings.Contains(err.Error(), "repository x") {
			t.Errorf("repo change to %s: %v", repo, err)
		}
	}
	if got, _ := k.Get(it.ID); got.Repo != "x" || got.Workflow != "x/infra" {
		t.Errorf("the item changed: %+v", got)
	}
	pullRepo(t, k, it.ID, "y")
	got, err := k.Get(it.ID)
	if err != nil || got.Repo != "y" {
		t.Fatalf("pulled item %+v, %v", got, err)
	}
	var mismatch *TargetMismatch
	if _, err := k.Transition(context.Background(), it.ID, "build", "me"); !errors.As(err, &mismatch) {
		t.Errorf("transition of a mismatched item: %v", err)
	}
	if _, err := k.GateDry(context.Background(), GateDryRequest{Workflow: "x/infra", Gate: "any", Item: it.ID}); !errors.As(err, &mismatch) {
		t.Errorf("dry gate of a mismatched item: %v", err)
	}
	if mv, err := k.NextMove(it.ID, "s"); err != nil || mv.Act != "" {
		t.Errorf("next move of a mismatched item: %+v %v", mv, err)
	}
	if _, err := k.ItemWorkflow(got); !errors.As(err, &mismatch) || !strings.Contains(err.Error(), "serves only") {
		t.Errorf("ItemWorkflow: %v", err)
	}
	if err := k.checkTrigger(item.QueueKind, &item.Trigger{Workflow: "x/infra", MinSize: 1}); err == nil || !strings.Contains(err.Error(), "repository x") {
		t.Errorf("a queue trigger on a part workflow: %v", err)
	}
}

func TestPartWorkflowTerminalOnlyForItsRepository(t *testing.T) {
	k := openPart(t)
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"build", "shipped"} {
		if _, err := k.Transition(context.Background(), it.ID, to, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if o, err := k.ItemOutcome(it.ID); err != nil || !o.Terminal {
		t.Fatalf("outcome of a shipped x item: %+v %v", o, err)
	}
	pullRepo(t, k, it.ID, "y")
	if o, err := k.ItemOutcome(it.ID); err != nil || o.Terminal {
		t.Errorf("outcome of a mismatched item: %+v %v", o, err)
	}
}

func TestNoPartsDirectoryPlacesAsToday(t *testing.T) {
	for _, withRepos := range []bool{false, true} {
		root := partRoot(t, withRepos)
		cfg, err := LoadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Parts != nil || cfg.PartWarnings != nil {
			t.Errorf("parts %v %v with no %s/", cfg.Parts, cfg.PartWarnings, PartsDir)
		}
		for n, w := range cfg.Workflows {
			if w.Part != "" {
				t.Errorf("workflow %s has part %q", n, w.Part)
			}
		}
		k, err := Open(Options{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range []item.Item{
			{Kind: "story", Category: "c"},
			{Kind: "story", Category: "c", Repo: "x"},
			{Kind: "task"},
			{Kind: "story", Tags: []string{"t"}},
		} {
			name, start, err := k.WorkflowFor(it)
			want := k.Rules.Select(it)
			if err != nil || name != want || start != k.Workflows[want].StartState() {
				t.Errorf("WorkflowFor(%+v) = %s %s %v, want %s", it, name, start, err, want)
			}
		}
		k.Close()
	}
}

const partForeign = `[states.intake]
start = true
[states.build]
performer = { agent = "infra-reviewer", skill = "infra-tests-check" }
[states.shipped]
terminal = true
[[transition]]
from = ["intake"]
to = "build"
[[transition]]
from = ["build"]
to = "shipped"
`

func writeForeignPart(t *testing.T, root string) {
	t.Helper()
	write(t, filepath.Join(root, PartsDir, "infra", SettingsFile), "[[workflows.rule]]\nworkflow = \"delivery\"\ncategory = \"c\"\n")
	write(t, filepath.Join(root, PartsDir, "infra", WorkflowsDir, "delivery.toml"), partForeign)
}

func TestPartUnregisteredIsIgnored(t *testing.T) {
	root := partRoot(t, false)
	writeForeignPart(t, root)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.Problems(); len(ps) > 0 {
		t.Fatalf("problems: %v", errors.Join(ps...))
	}
	if len(cfg.Parts) != 0 {
		t.Errorf("parts %v", cfg.Parts)
	}
	if len(cfg.PartWarnings) != 1 || cfg.PartWarnings[0].String() != "repos/infra: not loaded: repos.toml registers no valid repository of that name" {
		t.Errorf("warnings %v", cfg.PartWarnings)
	}
	if _, ok := cfg.Workflows["infra/delivery"]; ok {
		t.Error("an unregistered part's workflow loaded")
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c")}, "me")
	if err != nil || it.Workflow != "default" {
		t.Errorf("self story = %s %v", it.Workflow, err)
	}
}

func TestPartWarningNames(t *testing.T) {
	root := partRoot(t, true)
	write(t, filepath.Join(root, PartsDir, "self", SettingsFile), "")
	if runtime.GOOS != "windows" {
		write(t, filepath.Join(root, PartsDir, "a\x1b[31m\nb", SettingsFile), "")
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.Problems(); len(ps) > 0 {
		t.Fatalf("problems: %v", errors.Join(ps...))
	}
	var lines []string
	for _, w := range cfg.PartWarnings {
		lines = append(lines, w.String())
	}
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "repos/self: not loaded: self is the control repository") {
		t.Errorf("warnings %q", all)
	}
	if runtime.GOOS != "windows" && (!strings.Contains(all, "not a valid repository name") || strings.Contains(all, "\x1b")) {
		t.Errorf("warnings %q", all)
	}
	if len(cfg.Parts) != 0 {
		t.Errorf("parts %v", cfg.Parts)
	}
}

func foreignRoot(t *testing.T) string {
	t.Helper()
	root := partRoot(t, false)
	write(t, filepath.Join(root, reporeg.TrackedFile), "[repos.infra]\norigin = \"https://github.com/o/infra\"\n[repos.y]\norigin = \"https://github.com/o/y\"\n")
	return root
}

func TestPartUnresolvedRefusesItsStories(t *testing.T) {
	root := foreignRoot(t)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	early, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("infra")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	self, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s"), Category: ptr("c")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	k.Close()
	writeForeignPart(t, root)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := cfg.Problems(); len(ps) > 0 {
		t.Fatalf("problems: %v", errors.Join(ps...))
	}
	p := cfg.Parts["infra"]
	if p == nil || len(p.Problems) == 0 {
		t.Fatalf("part %+v", p)
	}
	all := errors.Join(p.Problems...).Error()
	for _, want := range []string{"infra-reviewer", "infra-tests-check", "repos/infra: "} {
		if !strings.Contains(all, want) {
			t.Errorf("part problems lack %q: %s", want, all)
		}
	}
	k, err = Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, ok := k.Workflows["infra/delivery"]; ok {
		t.Error("an unresolved part's workflow loaded")
	}
	var refused *PartUnresolved
	if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("b"), Category: ptr("c"), Repo: ptr("infra")}, "me"); !errors.As(err, &refused) || !strings.Contains(err.Error(), "infra-reviewer") {
		t.Errorf("create --repo infra: %v", err)
	}
	for _, repo := range []string{"self", "y"} {
		it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("b"), Category: ptr("c"), Repo: ptr(repo)}, "me")
		if err != nil || it.Workflow != "default" {
			t.Errorf("create --repo %s = %s %v", repo, it.Workflow, err)
		}
	}
	if _, err := k.Seat(early.ID, "s", LocalLocation(t.TempDir()), "", "me"); !errors.As(err, &refused) {
		t.Errorf("seat: %v", err)
	}
	if _, err := k.Perform(context.Background(), early.ID, "me"); !errors.As(err, &refused) {
		t.Errorf("perform: %v", err)
	}
	if _, err := k.Transition(context.Background(), early.ID, "done", "me"); !errors.As(err, &refused) {
		t.Errorf("transition: %v", err)
	}
	if _, err := k.GateDry(context.Background(), GateDryRequest{Workflow: "default", Gate: "any", Item: early.ID}); !errors.As(err, &refused) {
		t.Errorf("dry gate: %v", err)
	}
	if _, err := k.Update(self.ID, item.Fields{Repo: ptr("infra")}, "me"); !errors.As(err, &refused) {
		t.Errorf("repo change to infra: %v", err)
	}
	if !Refused(refused) {
		t.Error("Refused does not accept a PartUnresolved")
	}
	var u interface{ Unplaceable() bool }
	if !errors.As(error(refused), &u) || !u.Unplaceable() {
		t.Error("a PartUnresolved is not unplaceable")
	}
}

func TestPartLoadProblems(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, filepath.Join(outside, "infra.toml"), "SECRET-OUTSIDE = 1\n")
	write(t, filepath.Join(outside, SettingsFile), "SECRET-OUTSIDE = 1\n")
	write(t, filepath.Join(outside, "x", SettingsFile), "SECRET-OUTSIDE = 1\n")
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
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{"undefined rule workflow", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "[[workflows.rule]]\nworkflow = \"nope\"\ncategory = \"c\"\n")
		}, "repos/x/satelle.toml: workflows: rule 1 names workflow \"nope\""},
		{"undefined default", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "[workflows]\ndefault = \"nope\"\n")
		}, "default workflow nope is not defined under repos/x/workflows/"},
		{"part link", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "x"), filepath.Join(root, PartsDir, "x"))
		}, "repos/x: not a directory"},
		{"settings link", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, SettingsFile), filepath.Join(root, PartsDir, "x", SettingsFile))
		}, "repos/x/satelle.toml: not a regular file"},
		{"workflows link", func(t *testing.T, root string) {
			link(t, outside, filepath.Join(root, PartsDir, "x", WorkflowsDir))
		}, "repos/x/workflows: not a directory"},
		{"workflow file link", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "infra.toml"), filepath.Join(root, PartsDir, "x", WorkflowsDir, "infra.toml"))
		}, "repos/x/workflows/infra.toml: not a regular file"},
		{"workflows a file", func(t *testing.T, root string) {
			p := filepath.Join(root, PartsDir, "x", WorkflowsDir)
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			write(t, p, "x")
		}, "repos/x/workflows: not a directory"},
		{"workflows unreadable", func(t *testing.T, root string) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("mode 000 does not refuse here")
			}
			p := filepath.Join(root, PartsDir, "x", WorkflowsDir)
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(p, 0o755) })
		}, "repos/x/workflows: cannot be read"},
		{"invalid toml", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "not = [valid")
		}, "repos/x/satelle.toml"},
		{"unknown workflows key", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "x", SettingsFile), "[workflows]\nbogus = 1\n")
		}, "repos/x/satelle.toml: unknown key workflows.bogus"},
		{"invalid file stem", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "a b.toml"), partInfra)
		}, "is not a valid workflow file name"},
		{"invalid name", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "x", WorkflowsDir, "other.toml"), "name = \"a/b\"\n"+partInfra)
		}, "workflow name 'a/b' is not a valid name"},
		{"shared name collides", func(t *testing.T, root string) {
			write(t, filepath.Join(root, WorkflowsDir, "clash.toml"), "name = \"x/infra\"\n"+partInfra)
		}, "workflow x/infra is also defined in"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := partRoot(t, true)
			writePart(t, root)
			c.setup(t, root)
			cfg, err := LoadConfig(root)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if ps := cfg.Problems(); len(ps) > 0 {
				t.Fatalf("shared problems: %v", errors.Join(ps...))
			}
			p := cfg.Parts["x"]
			if p == nil || len(p.Problems) == 0 {
				t.Fatalf("part %+v", p)
			}
			all := errors.Join(p.Problems...).Error()
			if !strings.Contains(all, c.want) {
				t.Fatalf("part problems %q, want %q", all, c.want)
			}
			if strings.Contains(all, "SECRET-OUTSIDE") || strings.Contains(all, "\x1b") {
				t.Errorf("a problem shows outside or unsafe content: %q", all)
			}
			if w, ok := cfg.Workflows["x/infra"]; ok && w.Part == "x" {
				t.Error("an unresolved part's workflow loaded")
			}
			k, err := Open(Options{Root: root})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer k.Close()
			var refused *PartUnresolved
			if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a"), Category: ptr("c"), Repo: ptr("x")}, "me"); !errors.As(err, &refused) {
				t.Errorf("create --repo x: %v", err)
			}
		})
	}
}

func TestPartsDirectoryUnreadableFailsClosed(t *testing.T) {
	outside := t.TempDir()
	root := partRoot(t, true)
	if err := os.Symlink(outside, filepath.Join(root, PartsDir)); err != nil {
		t.Skip(err)
	}
	if _, err := LoadConfig(root); err == nil || !strings.Contains(err.Error(), "repos: not a directory") {
		t.Errorf("load: %v", err)
	}
	if _, err := Open(Options{Root: root}); err == nil {
		t.Error("Open accepted a linked repos/")
	}
}
