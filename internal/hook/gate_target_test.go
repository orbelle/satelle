package hook

import (
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
