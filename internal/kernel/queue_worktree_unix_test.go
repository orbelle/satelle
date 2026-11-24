//go:build unix

package kernel

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

func gitQueueRepo(t *testing.T) (root string, open func(...string) *Kernel) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, open = queueKernel(t)
	top := filepath.Dir(root)
	write(t, filepath.Join(top, ".gitignore"), ".satelle/runtime/\n")
	gittest.Git(t, top, "init", "-q", "-b", "main")
	return root, open
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
}

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
	root, open := gitQueueRepo(t)
	top := filepath.Dir(root)
	runtime := filepath.Join(root, "runtime")
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	k.Close()
	queueFile(t, root, "release", "title = \"Release\"\n[filter]\nkind = \"story\"\n[trigger]\ntask = \""+task.ID+"\"\n")
	commitAll(t, top)
	k = open()
	if k.linkedTree {
		t.Fatal("the main checkout reads as a linked worktree")
	}
	before := chainLen(t, k, "que_release")
	if before != 1 {
		t.Fatalf("main checkout wrote %d events, want the genesis", before)
	}
	k.Close()

	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Git(t, top, "worktree", "add", "-q", wt, "-b", "sto/x")
	wroot := filepath.Join(wt, ".satelle")
	queueFile(t, wroot, "release", "title = \"Old release\"\n[filter]\nstatus = \"done\"\n")
	queueFile(t, wroot, "fresh", "title = \"Fresh\"\n[filter]\nkind = \"story\"\n")
	wk, err := Open(Options{Root: wroot, Runtime: runtime, Dir: wt})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wk.Close() })
	if !wk.linkedTree {
		t.Fatal("the linked worktree reads as the main checkout")
	}
	if n := chainLen(t, wk, "que_release"); n != before {
		t.Fatalf("linked worktree open: chain %d events, want %d", n, before)
	}
	if wrote, err := wk.Remirror("que_release"); wrote || err != nil {
		t.Fatalf("remirror in a linked worktree: wrote %v, %v", wrote, err)
	}
	s := mustCreate(t, wk, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if n := chainLen(t, wk, "que_release"); n != before {
		t.Fatalf("linked worktree write: chain %d events, want %d", n, before)
	}
	if n := chainLen(t, wk, "que_fresh"); n != 0 {
		t.Fatalf("linked worktree wrote %d genesis events for que_fresh", n)
	}
	want := "queue fresh has no chain yet; a queue file is mirrored onto its chain only from the main checkout, so run a satelle command there first (.satelle/queues/fresh.toml)"
	if _, err := wk.Enqueue("fresh", []string{s.ID}, "", "me"); err == nil || err.Error() != want {
		t.Fatalf("enqueue: %v", err)
	}
	wk.Close()

	queueFile(t, root, "release", "title = \"Release 2\"\n[filter]\nkind = \"story\"\nstatus = \"backlog\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 2\n")
	k = open()
	if n := chainLen(t, k, "que_release"); n != before+1 {
		t.Fatalf("main checkout edit: chain %d events, want %d", n, before+1)
	}
	lastMirror(t, k, "que_release")
	assertChainIsFile(t, k, "que_release", filepath.Join(root, QueuesDir, "release.toml"))
}

func TestQueueFileMirrorInPlainRepo(t *testing.T) {
	root, open := gitQueueRepo(t)
	queueFile(t, root, "release", "title = \"Release\"\n[filter]\nkind = \"story\"\n")
	commitAll(t, filepath.Dir(root))
	k := open()
	if n := chainLen(t, k, "que_release"); n != 1 {
		t.Fatalf("genesis: %d events", n)
	}
	k.Close()
	queueFile(t, root, "release", "title = \"Release 2\"\n[filter]\nstatus = \"done\"\n")
	k = open()
	if n := chainLen(t, k, "que_release"); n != 2 {
		t.Fatalf("edit: %d events, want 2", n)
	}
	lastMirror(t, k, "que_release")
	assertChainIsFile(t, k, "que_release", filepath.Join(root, QueuesDir, "release.toml"))
	k.Close()
	k = open()
	if n := chainLen(t, k, "que_release"); n != 2 {
		t.Fatalf("unchanged file: %d events, want 2", n)
	}
}
