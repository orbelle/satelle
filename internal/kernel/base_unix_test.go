//go:build !windows

package kernel

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

// acKernel is a three-state line a → b → c with a pathless code-ac gate on
// b → c, as the delivery workflow's code-ac is, over the git repo at dir.
func acKernel(t *testing.T, dir string) (*Kernel, *fakeDispatcher) {
	t.Helper()
	root := filepath.Join(dir, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"line\"\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.r1]
command = "claude"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "review.md"), "Review.\n")
	write(t, filepath.Join(root, "workflows", "line.toml"), `
[states.a]
start = true
[states.b]
[states.c]
terminal = true
[gates.code-ac]
skill = "review"
agent = "r1"
[[transition]]
from = ["a"]
to = "b"
[[transition]]
from = ["b"]
to = "c"
groups = [{ gates = ["code-ac"] }]
`)
	k, err := Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept}}
	k.Dispatcher = d
	return k, d
}

// mainRepo is a repo on main with the kernel's config committed (its runtime ignored), and
// origin/main (a remote-tracking ref, no remote) at that commit, M0.
func mainRepo(t *testing.T) (dir string, k *Kernel, d *fakeDispatcher, m0 string) {
	t.Helper()
	dir = t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README"), "r\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "init")
	k, d = acKernel(t, dir)
	write(t, filepath.Join(dir, ".gitignore"), ".satelle/runtime/\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "config")
	m0 = gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", m0)
	return dir, k, d, m0
}

// commitFile writes name and commits it, returning the new HEAD.
func commitFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	write(t, filepath.Join(dir, name), body)
	gittest.Git(t, dir, "add", "--", name)
	gittest.Git(t, dir, "commit", "-q", "-m", "add "+name)
	return gittest.Git(t, dir, "rev-parse", "HEAD")
}

// seatedItem creates an item, moves it to b and seats s1 in dir.
func seatedItem(t *testing.T, k *Kernel, dir, baseRef string) (item.Item, item.Seat) {
	t.Helper()
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	s, err := k.Seat(it.ID, "s1", "local:"+dir, baseRef, "ada")
	if err != nil {
		t.Fatal(err)
	}
	return it, s
}

// advanceMain commits on main, moves origin/main there and merges it into
// branch, which is checked out again. It returns main's new tip.
func advanceMain(t *testing.T, dir, branch string) string {
	t.Helper()
	gittest.Git(t, dir, "checkout", "-q", "main")
	commitFile(t, dir, "other/notes.md", "notes\n")
	m1 := commitFile(t, dir, "other/other.go", "package other\n")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", m1)
	gittest.Git(t, dir, "checkout", "-q", branch)
	gittest.Git(t, dir, "merge", "-q", "--no-edit", "origin/main")
	return m1
}

func TestTheJudgedBaseFollowsTheBaseBranchOnlyWhenBehindIt(t *testing.T) {
	cases := []struct {
		name string
		// run builds the history and returns the item and the base it must be
		// judged from.
		run   func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string)
		stale bool
	}{
		{"seated at the fork, main merged in: the merge base", func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string) {
			gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
			it, _ := seatedItem(t, k, dir, "")
			commitFile(t, dir, "story.go", "package story\n")
			return it, advanceMain(t, dir, "feat/story")
		}, false},
		{"no origin/main: the recorded base", func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string) {
			gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
			it, _ := seatedItem(t, k, dir, "")
			commitFile(t, dir, "story.go", "package story\n")
			advanceMain(t, dir, "feat/story")
			gittest.Git(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
			return it, m0
		}, false},
		{"re-seated on a story commit: the recorded base", func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string) {
			gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
			s := commitFile(t, dir, "story.go", "package story\n")
			it, _ := seatedItem(t, k, dir, "")
			commitFile(t, dir, "more.go", "package story\n")
			return it, s
		}, false},
		{"HEAD inside the base branch: the recorded base", func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string) {
			gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
			it, _ := seatedItem(t, k, dir, "")
			head := commitFile(t, dir, "story.go", "package story\n")
			gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", head)
			return it, m0
		}, false},
		{"stacked on a parent that advanced and was merged in: the parent's tip", func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string) {
			gittest.Git(t, dir, "checkout", "-q", "-b", "chore/parent")
			commitFile(t, dir, "parent.go", "package parent\n")
			gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
			it, seat := seatedItem(t, k, dir, "chore/parent")
			if seat.BaseRef != "chore/parent" {
				t.Fatalf("seat base ref = %q", seat.BaseRef)
			}
			commitFile(t, dir, "story.go", "package story\n")
			gittest.Git(t, dir, "checkout", "-q", "chore/parent")
			p2 := commitFile(t, dir, "parent2.go", "package parent\n")
			gittest.Git(t, dir, "checkout", "-q", "feat/story")
			gittest.Git(t, dir, "merge", "-q", "--no-edit", "chore/parent")
			_, events, err := k.load(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if seats := item.FoldSeats(events); len(seats) != 1 || seats[0].BaseRef != "chore/parent" {
				t.Fatalf("folded seats = %+v", seats)
			}
			return it, p2
		}, false},
		{"rebased after seating: the recorded base, stale", func(t *testing.T, dir string, k *Kernel, m0 string) (item.Item, string) {
			gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
			s := commitFile(t, dir, "story.go", "package story\n")
			it, _ := seatedItem(t, k, dir, "")
			gittest.Git(t, dir, "reset", "-q", "--hard", "HEAD~1")
			commitFile(t, dir, "story.go", "package story // rebased\n")
			return it, s
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, k, _, m0 := mainRepo(t)
			it, want := c.run(t, dir, k, m0)
			d, stale, err := k.ItemChange(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := d.Base
			if got != want {
				t.Fatalf("judged base = %s, want %s (main fork %s)", got, want, m0)
			}
			if (stale != nil) != c.stale {
				t.Fatalf("stale = %v, want stale %v", stale, c.stale)
			}
		})
	}
}

func TestAMergeOfMainKeepsTheJudgedDiffToTheItemsOwnChange(t *testing.T) {
	dir, k, d, m0 := mainRepo(t)
	gittest.Git(t, dir, "checkout", "-q", "-b", "feat/story")
	it, seat := seatedItem(t, k, dir, "")
	if seat.Base != m0 {
		t.Fatalf("seat base = %s, want the main fork %s", seat.Base, m0)
	}
	commitFile(t, dir, "story.go", "package story\n")
	m1 := advanceMain(t, dir, "feat/story")

	res, err := k.Transition(context.Background(), it.ID, "c", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Moved || len(res.Gates) != 1 || res.Gates[0].Gate != "code-ac" || res.Gates[0].Result != "accept" {
		t.Fatalf("transition = %+v", res)
	}
	if got := strings.Join(res.Gates[0].Changed, ","); got != "story.go" {
		t.Fatalf("judged paths = %q, want only the item's own story.go", got)
	}
	if len(d.reqs) == 0 {
		t.Fatal("code-ac was not dispatched")
	}
	prompt := d.reqs[len(d.reqs)-1].Prompt
	if !strings.Contains(prompt, "Base commit: "+m1) || !strings.Contains(prompt, "story.go") {
		t.Fatalf("the code-ac payload is not judged from main's tip %s over story.go:\n%s", m1, prompt)
	}
	for _, p := range []string{"other/notes.md", "other/other.go"} {
		if strings.Contains(prompt, p) {
			t.Fatalf("the code-ac payload carries merged-in main's %s:\n%s", p, prompt)
		}
	}
	if d, stale, err := k.ItemChange(it.ID); err != nil || stale != nil || d.Base != m1 {
		t.Fatalf("item base = %s, %v, %v; want %s", d.Base, stale, err, m1)
	}
}
