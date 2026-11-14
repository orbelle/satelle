package ledger

import (
	"errors"
	"strings"
	"testing"
)

// A read-only store refuses every write, saying why, and still reads.
func TestReadOnlyRefusesEveryWrite(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Append("it_1", "item.created", "me", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetReadOnly("a dry run")
	next, err := NewEventWith("ev_2", e.At, "it_1", "item.updated", "me", nil, e.Hash)
	if err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]func() error{
		"append":     func() error { _, err := s.Append("it_1", "item.updated", "me", nil); return err },
		"append raw": func() error { return s.AppendRaw(next) },
		"blob":       func() error { _, err := s.PutBlob([]byte("x")); return err },
	} {
		if err := write(); !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "a dry run") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if events, err := s.Read("it_1"); err != nil || len(events) != 1 {
		t.Fatalf("read %d %v", len(events), err)
	}
}
