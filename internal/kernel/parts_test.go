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
		if cfg.Parts != nil {
			t.Errorf("parts %v with no %s/", cfg.Parts, PartsDir)
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

func TestPartProblems(t *testing.T) {
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
		{"unregistered", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "z", SettingsFile), "")
		}, "repos/z: repos.toml registers no valid repository"},
		{"self", func(t *testing.T, root string) {
			write(t, filepath.Join(root, PartsDir, "self", SettingsFile), "")
		}, "repos/self: self is the control repository"},
		{"unsafe name", func(t *testing.T, root string) {
			if runtime.GOOS == "windows" {
				t.Skip("no control characters in Windows file names")
			}
			write(t, filepath.Join(root, PartsDir, "a\x1b[31m\nb", SettingsFile), "")
		}, "not a valid repository name"},
		{"repos link", func(t *testing.T, root string) {
			link(t, outside, filepath.Join(root, PartsDir))
		}, "repos: not a directory"},
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
			all := err
			if cfg != nil {
				all = errors.Join(err, errors.Join(cfg.Problems()...))
			}
			if all == nil || !strings.Contains(all.Error(), c.want) {
				t.Fatalf("problems %v, want %q", all, c.want)
			}
			if strings.Contains(all.Error(), "SECRET-OUTSIDE") || strings.Contains(all.Error(), "\x1b") {
				t.Errorf("a problem shows outside or unsafe content: %q", all)
			}
			if strings.Contains(c.name, "link") && cfg != nil {
				if w, ok := cfg.Workflows["x/infra"]; ok && w.Part == "x" && c.name != "settings link" {
					t.Errorf("a part workflow loaded through a link")
				}
			}
			if _, err := Open(Options{Root: root}); err == nil {
				t.Error("Open accepted the configuration")
			}
		})
	}
}
