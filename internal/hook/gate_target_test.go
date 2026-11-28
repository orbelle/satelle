package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestEditGateSeatedTarget(t *testing.T) {
	root, a, b, c := newRepo(t), newRepo(t), newRepo(t), newRepo(t)
	linkedA := filepath.Join(t.TempDir(), "wt")
	gittest.Git(t, a, "worktree", "add", "-q", linkedA)
	linkedA, _ = filepath.EvalSymlinks(linkedA)
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	temp, _ := filepath.EvalSymlinks(t.TempDir())

	var self Grant
	var targets []Target
	g := Gate{
		Root:      root,
		Exempt:    []string{"docs/**", "*.md"},
		Locked:    []string{"locked/**"},
		TempRoots: []string{temp},
		Grant:     func(string) Grant { return self },
		Targets:   func() []Target { return targets },
	}
	treeA := Target{Names: []string{"a"}, Item: "sto_1", Tree: a, Grant: Grant{Tree: true}}
	expect := func(t *testing.T, v Verdict, allow bool, want ...string) {
		t.Helper()
		if v.Allow != allow {
			t.Fatalf("allow = %v, want %v: %s", v.Allow, allow, v.Reason)
		}
		for _, w := range want {
			if !strings.Contains(v.Reason, w) {
				t.Errorf("reason lacks %q: %s", w, v.Reason)
			}
		}
	}

	t.Run("AC1 tree seat on target allows its checkout", func(t *testing.T) {
		self, targets = Grant{}, []Target{treeA}
		expect(t, g.Edit(filepath.Join(a, "x.go")), true)
		expect(t, g.Edit(filepath.Join(a, "locked", "x")), false, "locked path")
	})

	t.Run("AC2 other repositories and the control tree refused naming the target", func(t *testing.T) {
		self, targets = Grant{}, []Target{treeA}
		for _, p := range []string{filepath.Join(b, "x.go"), filepath.Join(c, "x.go"), filepath.Join(linkedA, "x.go")} {
			expect(t, g.Edit(p), false, "another repository's working tree", "repo a at "+a, "sto_1")
		}
		expect(t, g.Edit(filepath.Join(root, "main.go")), false, "targets repo a", "edits land in "+a)
		expect(t, g.Bash(`python3 -c 'print(1)'`, c), true)
	})

	t.Run("AC3 attachments-only seat refuses target and control", func(t *testing.T) {
		ao := Grant{AttachOnly: true, Item: "sto_1", State: "st-plan"}
		self = ao
		targets = []Target{{Names: []string{"a"}, Item: "sto_1", Tree: a, Grant: ao}}
		for _, p := range []string{filepath.Join(a, "x.go"), filepath.Join(root, "main.go"), filepath.Join(a, "docs", "x.md")} {
			v := g.Edit(p)
			expect(t, v, false, "st-plan")
			if !v.AttachOnly {
				t.Errorf("%s: AttachOnly false", p)
			}
		}
	})

	t.Run("AC3 attachments-only wins on a shared checkout in either order", func(t *testing.T) {
		self = Grant{}
		ao := Target{Names: []string{"a2"}, Item: "sto_2", Tree: a, Grant: Grant{AttachOnly: true, Item: "sto_2", State: "st-plan"}}
		for _, list := range [][]Target{{treeA, ao}, {ao, treeA}} {
			targets = list
			expect(t, g.Edit(filepath.Join(a, "x.go")), false, "sto_2")
		}
	})

	t.Run("AC4 self seat edits control and refuses external targets", func(t *testing.T) {
		self, targets = Grant{Tree: true}, nil
		expect(t, g.Edit(filepath.Join(root, "main.go")), true)
		targets = []Target{treeA}
		expect(t, g.Edit(filepath.Join(root, "main.go")), true)
		expect(t, g.Edit(filepath.Join(b, "x.go")), false, "self at "+root, "another repository's working tree")
	})

	t.Run("AC5 no targets keeps today's fence text", func(t *testing.T) {
		self, targets = Grant{Tree: true}, nil
		v := g.Edit(filepath.Join(b, "x.go"))
		want := "satelle: " + filepath.Join(b, "x.go") + " is in another repository's working tree (" + b + "). This session works in " + root + "; edit another repository from a session there."
		if v.Allow || v.Reason != want {
			t.Fatalf("got %+v\nwant %s", v, want)
		}
	})

	t.Run("AC6 shell writes follow the same rule", func(t *testing.T) {
		self, targets = Grant{}, []Target{treeA}
		for _, cmd := range []string{
			"echo x > " + a + "/f",
			"cp main.go " + a + "/f",
			"mv " + a + "/f " + a + "/g",
			"git -C " + a + " commit -m x",
			"cd " + a + " && git add f",
		} {
			expect(t, g.Bash(cmd, root), true)
		}
		for _, cmd := range []string{
			"echo x > " + b + "/f",
			"cp main.go " + b + "/f",
			"mv main.go " + b + "/f",
			"git -C " + b + " commit -m x",
		} {
			expect(t, g.Bash(cmd, root), false, "repo a at "+a)
		}
		expect(t, g.Bash(`git -C "$B" commit -m x`, root), false, "fail-closed rule")
	})

	link := func(t *testing.T, target, at string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, at); err != nil && !os.IsExist(err) {
			t.Skip("symlinks unavailable:", err)
		}
	}
	links := func(t *testing.T) {
		link(t, root, filepath.Join(a, "toroot"))
		link(t, b, filepath.Join(a, "tob"))
		link(t, filepath.Join(root, "main.go"), filepath.Join(a, "mainlink"))
		link(t, filepath.Join(root, "new.go"), filepath.Join(a, "dangling"))
		link(t, filepath.Join(root, "sub"), filepath.Join(a, "sublink"))
		link(t, filepath.Join(root, "sub"), filepath.Join(a, "dirlink"))
		link(t, "../new2.go", filepath.Join(root, "sub", "dl"))
		link(t, filepath.Join(a, "sub"), filepath.Join(a, "inner"))
		link(t, "sub", filepath.Join(a, "rel"))
		link(t, filepath.Join(a, "sub", "d2"), filepath.Join(a, "deep"))
		link(t, filepath.Join(a, "x.go"), filepath.Join(root, "rl"))
		link(t, filepath.Join(a, "x.go"), filepath.Join(root, "locked", "ll"))
		link(t, filepath.Join(a, "x.go"), filepath.Join(a, "locked", "al"))
	}
	mkdir := func(t *testing.T, d string) {
		t.Helper()
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(root, "sub"))
	mkdir(t, filepath.Join(a, "sub", "d2"))
	const note = "resolves through a symlink to"

	t.Run("AC8 shell writes through a symlink are judged where they land", func(t *testing.T) {
		self, targets = Grant{}, []Target{treeA}
		links(t)
		for _, r := range []struct{ cmd, lands string }{
			{"echo x > " + a + "/toroot/f", filepath.Join(root, "f")},
			{"echo x >> " + a + "/mainlink", filepath.Join(root, "main.go")},
			{"cp main.go " + a + "/toroot/f", filepath.Join(root, "f")},
			{"cp main.go " + a + "/tob/f", filepath.Join(b, "f")},
			{"touch " + a + "/dangling", filepath.Join(root, "new.go")},
			{"echo x > " + a + "/dangling", filepath.Join(root, "new.go")},
			{"cd " + a + "/toroot && touch f", filepath.Join(root, "f")},
			{"touch " + a + "/dirlink/dl", root + string(filepath.Separator)},
			{"echo x > " + a + "/sublink/../main.go", filepath.Join(root, "main.go")},
			{"cd " + a + "/sublink && echo x > ../main.go", filepath.Join(root, "main.go")},
			{"git -C " + a + "/sublink -C .. commit -m x", root},
			{"echo x > " + a + "/missing/../mainlink", filepath.Join(root, "main.go")},
			{"mkdir -p " + a + "/missing/../sublink/d", filepath.Join(root, "sub", "d")},
		} {
			t.Run(r.cmd, func(t *testing.T) { expect(t, g.Bash(r.cmd, root), false, note, r.lands) })
		}
		for _, r := range []struct{ cmd, lands string }{
			{"cd missing ; cd ../sublink ; cd sublink && git commit -m x", root},
			{"cd deep ; cd ../sublink ; cd sublink && git commit -m x", root},
			{"cd deep ; cd ../sublink && git commit -m x", filepath.Join(root, "sub")},
			{"cd deep/../sublink && echo x > f", filepath.Join(root, "sub", "f")},
			{"cd deep && cd .. && echo x > mainlink", filepath.Join(root, "main.go")},
			{"cd deep && cd ../sublink && echo x > f", filepath.Join(root, "sub", "f")},
			{"cd deep && cd ../sublink && echo x > ../main.go", filepath.Join(root, "main.go")},
			{"cd deep && cd ../sublink && git -C .. commit -m x", root},
			{"cd -P deep && cd ../../sublink && touch f", filepath.Join(root, "sub", "f")},
		} {
			t.Run(r.cmd, func(t *testing.T) { expect(t, g.Bash(r.cmd, a), false, note, r.lands) })
		}
		for _, cmd := range []string{
			"cd deep ; cd ../sub && touch g",
			"cd inner && touch h",
			"cd -P deep && cd .. && touch g",
		} {
			expect(t, g.Bash(cmd, a), true)
		}
		ga := g
		ga.Root = a
		expect(t, ga.Edit("deep/../mainlink"), false, note, filepath.Join(root, "main.go"))
		expect(t, g.Edit(filepath.Join(a, "dirlink", "dl")), false, note)
		if got := CanonicalPath(filepath.Join(a, "dirlink", "dl")); got != filepath.Join(root, "new2.go") {
			t.Errorf("dirlink/dl lands at %s", got)
		}
		for _, r := range []struct{ p, lands string }{
			{filepath.Join(a, "mainlink"), filepath.Join(root, "main.go")},
			{filepath.Join(a, "dangling"), filepath.Join(root, "new.go")},
			{a + "/sublink/../main.go", filepath.Join(root, "main.go")},
			{a + "/missing/../mainlink", filepath.Join(root, "main.go")},
		} {
			expect(t, g.Edit(r.p), false, note, r.lands)
		}
		for _, cmd := range []string{
			"echo x > " + a + "/inner/f",
			"cp main.go " + a + "/inner/f",
			"touch " + a + "/rel/g",
			"cd " + a + "/inner && touch h",
			"echo x > " + a + "/inner/../y",
			"rm " + a + "/rel",
		} {
			expect(t, g.Bash(cmd, root), true)
		}
		expect(t, g.Edit(filepath.Join(a, "inner", "f")), true)
	})

	t.Run("AC8 commands on the link entry keep today's refusals", func(t *testing.T) {
		self, targets = Grant{}, []Target{treeA}
		links(t)
		for _, cmd := range []string{
			"rm " + root + "/rl",
			"mv " + a + "/x.go " + root + "/rl",
			"ln -sf " + a + "/y " + root + "/rl",
			"rm " + root + "/locked/ll",
			"rm " + a + "/locked/al",
			"ln -sf " + a + "/y " + a + "/locked/al",
		} {
			expect(t, g.Bash(cmd, root), false)
		}
		expect(t, g.Bash("rm "+a+"/sublink/../main.go", root), false, note, filepath.Join(root, "main.go"))
	})

	t.Run("AC8 file-tool paths are joined literally", func(t *testing.T) {
		self, targets = Grant{}, []Target{treeA}
		t.Setenv("TMPDIR", temp)
		t.Setenv("HOME", t.TempDir())
		links(t)
		for _, p := range []string{"$TMPDIR/x", "~/x", "   "} {
			expect(t, g.Edit(p), false, "edits land in "+a)
		}
		self = Grant{Tree: true}
		ga := g
		ga.Root = a
		expect(t, ga.Edit("x.go"), true)
		expect(t, ga.Edit("sublink/../main.go"), false, note, filepath.Join(root, "main.go"))
	})

	t.Run("AC7 a registry problem refuses every path", func(t *testing.T) {
		self = Grant{Tree: true}
		problem := "no local checkout mapped in .satelle/repos.local.toml"
		targets = []Target{{Names: []string{"a"}, Item: "sto_1", Problem: problem, Grant: Grant{Tree: true}}}
		for _, p := range []string{filepath.Join(root, "main.go"), filepath.Join(a, "x.go"), filepath.Join(plain, "x"), filepath.Join(root, "docs", "x")} {
			expect(t, g.Edit(p), false, "targets repo a", problem)
		}
		expect(t, g.Edit(filepath.Join(temp, "draft")), true)
		targets = []Target{{Problem: "this session's seats cannot be read: boom"}}
		expect(t, g.Edit(filepath.Join(root, "main.go")), false, "this session's seats cannot be read: boom")
	})
}
