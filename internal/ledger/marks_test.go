package ledger

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMarksChangeOnAppendOnly(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.Marks()
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty marks: %v %v", empty, err)
	}
	e1, err := s.Append("itm_1", "item.created", "me", map[string]any{"title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	m1, err := s.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(m1) != 1 || m1["itm_1"].Size == 0 {
		t.Fatalf("after append: %v", m1)
	}
	again, err := s.Marks()
	if err != nil || again["itm_1"] != m1["itm_1"] {
		t.Fatalf("marks not stable: %v vs %v (%v)", again, m1, err)
	}
	e2, err := NewEventWith("evt_raw", time.Now().UTC(), "itm_1", "note", "me", nil, e1.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRaw(e2); err != nil {
		t.Fatal(err)
	}
	m2, err := s.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if m2["itm_1"].Size <= m1["itm_1"].Size {
		t.Fatalf("append raw did not change the mark: %v -> %v", m1, m2)
	}
	if _, err := s.Append("itm_2", "item.created", "me", nil); err != nil {
		t.Fatal(err)
	}
	m3, err := s.Marks()
	if err != nil || len(m3) != 2 || m3["itm_1"] != m2["itm_1"] {
		t.Fatalf("second item: %v %v", m3, err)
	}
}

func TestLockPassSerialises(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var inside, overlap atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := s.LockPass()
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			if inside.Add(1) > 1 {
				overlap.Add(1)
			}
			time.Sleep(50 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()
	if overlap.Load() != 0 {
		t.Fatal("two passes held the pass lock at once")
	}
}
