//go:build unix

package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestOpenRefusesWritableTarget(t *testing.T) {
	_, main := gitQueueTree(t)
	root := filepath.Join(main, ".satelle")
	runtime := filepath.Join(t.TempDir(), "runtime")
	opts := Options{Root: root, Runtime: runtime, Dir: main}

	refused := opts
	refused.Target = "app"
	k, err := Open(refused)
	var mt *MappedTargetError
	if !errors.As(err, &mt) || k != nil {
		t.Fatalf("a writable open naming a target: %v, want a *MappedTargetError", err)
	}
	if mt.Target != "app" || mt.Control != root {
		t.Fatalf("refusal names target %q control %q", mt.Target, mt.Control)
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused open touched the runtime: %v", err)
	}

	k, err = Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	before := chainLen(t, k, "que_release")
	k.Close()
	queueFile(t, root, "release", "title = \"Release Z\"\n[filter]\nstatus = \"done\"\n")

	ro := refused
	ro.ReadOnly = "a mapped target work tree"
	k, err = Open(ro)
	if err != nil {
		t.Fatalf("a read-only open naming a target: %v", err)
	}
	if n := chainLen(t, k, "que_release"); n != before {
		t.Fatalf("a read-only target open mirrored the queue file: chain %d events, want %d", n, before)
	}
	if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s")}, "me"); !errors.Is(err, ledger.ErrReadOnly) {
		t.Fatalf("a read-only target kernel created an item: %v", err)
	}
	k.Close()

	k, err = Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if n := chainLen(t, k, "que_release"); n != before+1 {
		t.Fatalf("the control case did not mirror: chain %d events, want %d", n, before+1)
	}
}
