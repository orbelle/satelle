package kernel

import (
	"errors"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestReadOnlyOpenWritesNothing(t *testing.T) {
	root, _ := queueKernel(t)
	queueFile(t, root, "inbox", "title = \"Inbox\"\n[filter]\nkind = \"story\"\n")
	k, err := Open(Options{Root: root, ReadOnly: "a dry run"})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if n := chainLen(t, k, "que_inbox"); n != 0 {
		t.Fatalf("a read-only open materialized que_inbox: %d events", n)
	}
	if _, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("s")}, "me"); !errors.Is(err, ledger.ErrReadOnly) {
		t.Fatalf("a read-only kernel created an item: %v", err)
	}
}
