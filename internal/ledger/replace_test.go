package ledger

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReplace: a chain is replaced only under its current head, by a complete
// chain of the same item that verifies, and OnWrite sees each new event.
func TestReplace(t *testing.T) {
	local, _ := Open(t.TempDir())
	other, _ := Open(t.TempDir())
	var written []string
	local.OnWrite = func(e Event) { written = append(written, e.Hash) }
	old, err := local.Append("que_x", "item.created", "satelle", map[string]string{"cause": "queue file q"})
	if err != nil {
		t.Fatal(err)
	}
	written = nil
	for i := range 3 {
		if _, err := other.Append("que_x", "note", "me", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	fresh, _ := other.Read("que_x")

	// A moved tail is refused and the file is unchanged.
	before, _ := os.ReadFile(local.Path("que_x"))
	var stale *StaleTail
	if err := local.Replace("que_x", "not-the-head", fresh); !errors.As(err, &stale) {
		t.Fatalf("moved tail: %v, want *StaleTail", err)
	}
	// A chain that does not verify, or names another item, is refused.
	broken := append([]Event(nil), fresh...)
	broken[1].Payload = []byte(`{"n":9}`)
	if err := local.Replace("que_x", old.Hash, broken); err == nil {
		t.Fatal("a chain that does not verify was accepted")
	}
	otherItem, _ := other.Append("sto_y", "note", "me", nil)
	if err := local.Replace("que_x", old.Hash, []Event{otherItem}); err == nil {
		t.Fatal("a chain naming another item was accepted")
	}
	if err := local.Replace("que_x", "", fresh); err == nil {
		t.Fatal("an empty head was accepted")
	}
	if err := local.Replace("que_x", old.Hash, nil); err == nil {
		t.Fatal("an empty chain was accepted")
	}
	if after, _ := os.ReadFile(local.Path("que_x")); !bytes.Equal(before, after) {
		t.Fatal("a refused replace changed the chain file")
	}
	if len(written) != 0 {
		t.Fatalf("a refused replace called OnWrite %d times", len(written))
	}

	// Under the matching head the chain is the other store's, byte for byte.
	if err := local.Replace("que_x", old.Hash, fresh); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(other.Path("que_x"))
	if got, _ := os.ReadFile(local.Path("que_x")); !bytes.Equal(got, want) {
		t.Fatalf("replaced chain:\n%s\nwant:\n%s", got, want)
	}
	if len(written) != len(fresh) {
		t.Fatalf("OnWrite called %d times, want %d", len(written), len(fresh))
	}
	for i, e := range fresh {
		if written[i] != e.Hash {
			t.Fatalf("OnWrite %d saw %s, want %s", i, written[i], e.Hash)
		}
	}
	if err := local.Verify(); err != nil {
		t.Fatal(err)
	}
	if items, _ := local.Items(); len(items) != 1 {
		t.Fatalf("items %v: a temporary file was left behind", items)
	}
	if ents, _ := os.ReadDir(filepath.Join(local.Dir(), "ledger")); len(ents) != 1 {
		t.Fatalf("ledger dir holds %d entries, want 1", len(ents))
	}
}
