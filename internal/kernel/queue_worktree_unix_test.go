//go:build unix

package kernel

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

// gitQueueTree makes a satelle repo under a path holding spaces, commits its
// configuration and the release queue file, and returns the main checkout.
func gitQueueTree(t *testing.T) (base, main string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base = filepath.Join(t.TempDir(), "satelle repo")
	main = filepath.Join(base, "main tree")
	root := filepath.Join(main, ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\n")
	write(t, filepath.Join(root, "workflows", "default.toml"), `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "done"
`)
	queueFile(t, root, "release", "title = \"Release A\"\n[filter]\nkind = \"story\"\n")
	gittest.Git(t, main, "init", "-q", "-b", "main")
	gittest.Git(t, main, "add", ".satelle/satelle.toml", ".satelle/workflows", ".satelle/queues")
	gittest.Git(t, main, "commit", "-qm", "base")
	return base, main
}

// worktreeQueueRepo adds a linked worktree beside the main checkout.
func worktreeQueueRepo(t *testing.T) (main, wt string) {
	t.Helper()
	base, main := gitQueueTree(t)
	wt = filepath.Join(base, "linked wt")
	gittest.Git(t, main, "worktree", "add", "-q", wt, "-b", "wt")
	return main, wt
}

// openAt opens the kernel of the checkout at dir on the main checkout's
// runtime, as the CLI shares one runtime across every worktree.
func openAt(t *testing.T, main, dir string) *Kernel {
	t.Helper()
	k, err := Open(Options{Root: filepath.Join(dir, ".satelle"), Runtime: filepath.Join(main, ".satelle", "runtime"), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func releaseFile(dir string) string { return filepath.Join(dir, ".satelle", QueuesDir, "release.toml") }

func lastMirror(t *testing.T, k *Kernel, id string) {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	e := events[len(events)-1]
	var p struct{ Cause string }
	if e.Kind != item.KindUpdated || e.Actor != "satelle" || e.Decode(&p) != nil || p.Cause != "queue file .satelle/queues/release.toml" {
		t.Fatalf("last event %s by %q cause %q, want the file's item.updated", e.Kind, e.Actor, p.Cause)
	}
}

func TestQueueFileMirrorSkipsLinkedWorktree(t *testing.T) {
	main, wt := worktreeQueueRepo(t)
	mk := openAt(t, main, main)
	task := mustCreate(t, mk, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	before := chainLen(t, mk, "que_release")
	if before != 1 {
		t.Fatalf("main checkout wrote %d events, want the genesis", before)
	}
	mk.Close()

	wroot := filepath.Join(wt, ".satelle")
	queueFile(t, wroot, "release", "title = \"Release B\"\n[filter]\nstatus = \"done\"\n[trigger]\ntask = \""+task.ID+"\"\n")
	queueFile(t, wroot, "fresh", "title = \"Fresh\"\n[filter]\nkind = \"story\"\n")
	wk := openAt(t, main, wt)
	if !wk.linkedTree {
		t.Fatal("the linked worktree reads as the main checkout")
	}
	if n := chainLen(t, wk, "que_release"); n != before {
		t.Fatalf("linked worktree open: chain %d events, want %d", n, before)
	}
	if wrote, err := wk.Remirror("que_release"); wrote || err != nil {
		t.Fatalf("remirror in a linked worktree: wrote %v, %v", wrote, err)
	}
	if n := chainLen(t, wk, "que_release"); n != before {
		t.Fatalf("linked worktree remirror: chain %d events, want %d", n, before)
	}
	s := mustCreate(t, wk, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if n := chainLen(t, wk, "que_release"); n != before {
		t.Fatalf("linked worktree write: chain %d events, want %d", n, before)
	}
	if n := chainLen(t, wk, "que_fresh"); n != 0 {
		t.Fatalf("linked worktree wrote %d genesis events for que_fresh", n)
	}
	if _, err := wk.Enqueue("fresh", []string{s.ID}, "", "me"); err == nil || !strings.Contains(err.Error(), "main checkout") {
		t.Fatalf("enqueue on a chainless queue in a linked worktree: %v", err)
	}
	wk.Close()

	queueFile(t, filepath.Join(main, ".satelle"), "release", "title = \"Release C\"\n[filter]\nkind = \"story\"\nstatus = \"backlog\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 2\n")
	mk = openAt(t, main, main)
	if mk.linkedTree {
		t.Fatal("the main checkout reads as a linked worktree")
	}
	if n := chainLen(t, mk, "que_release"); n != before+1 {
		t.Fatalf("main checkout edit: chain %d events, want %d", n, before+1)
	}
	lastMirror(t, mk, "que_release")
	assertChainIsFile(t, mk, "que_release", releaseFile(main))
	mk.Close()
	mk = openAt(t, main, main)
	if n := chainLen(t, mk, "que_release"); n != before+1 {
		t.Fatalf("main checkout reopen: chain %d events, want %d", n, before+1)
	}
}

func TestQueueFileMirrorInPlainRepo(t *testing.T) {
	_, main := gitQueueTree(t)
	k := openAt(t, main, main)
	if k.linkedTree {
		t.Fatal("a repo with no worktree reads as linked")
	}
	if n := chainLen(t, k, "que_release"); n != 1 {
		t.Fatalf("genesis: %d events", n)
	}
	assertChainIsFile(t, k, "que_release", releaseFile(main))
	k.Close()
	queueFile(t, filepath.Join(main, ".satelle"), "release", "title = \"Release 2\"\n[filter]\nstatus = \"done\"\n")
	k = openAt(t, main, main)
	if n := chainLen(t, k, "que_release"); n != 2 {
		t.Fatalf("edit: %d events, want 2", n)
	}
	lastMirror(t, k, "que_release")
	assertChainIsFile(t, k, "que_release", releaseFile(main))
	k.Close()
	k = openAt(t, main, main)
	if n := chainLen(t, k, "que_release"); n != 2 {
		t.Fatalf("unchanged file: %d events, want 2", n)
	}
}
