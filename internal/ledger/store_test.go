package ledger

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestAppendReadVerify(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e1, err := s.Append("itm_1", "item.created", "me", map[string]any{"title": "x", "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.Append("itm_1", "note", "me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if e1.Prev != "" || e2.Prev != e1.Hash {
		t.Fatalf("prev linkage: %q %q %q", e1.Prev, e2.Prev, e1.Hash)
	}
	got, err := s.Read("itm_1")
	if err != nil || len(got) != 2 {
		t.Fatalf("read: %v %d", err, len(got))
	}
	if got[0].Hash != e1.Hash || !got[0].At.Equal(e1.At) {
		t.Fatalf("round trip: %+v vs %+v", got[0], e1)
	}
	if err := s.Verify(); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Items()
	if len(items) != 1 || items[0] != "itm_1" {
		t.Fatalf("items %v", items)
	}
	if _, err := s.Append("../x", "k", "", nil); err == nil {
		t.Fatal("invalid item accepted")
	}
}

func TestTamperDetected(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < 3; i++ {
		if _, err := s.Append("a", "k", "me", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(s.Path("a"))
	os.WriteFile(s.Path("a"), []byte(strings.Replace(string(b), `"n":1`, `"n":7`, 1)), 0o644)
	err := s.Verify()
	var bl *BrokenLink
	if !errors.As(err, &bl) || bl.Index != 1 {
		t.Fatalf("want broken link at 1, got %v", err)
	}
}

func TestConcurrentAppends(t *testing.T) {
	s, _ := Open(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := s.Append("c", "k", "me", map[string]int{"j": j}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	events, _ := s.Read("c")
	if len(events) != 100 {
		t.Fatalf("got %d events", len(events))
	}
	if err := s.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestImportAndAppendRaw(t *testing.T) {
	src, _ := Open(t.TempDir())
	dst, _ := Open(t.TempDir())
	for i := 0; i < 3; i++ {
		src.Append("a", "k", "me", map[string]int{"i": i})
	}
	events, _ := src.Read("a")
	if n, err := dst.Import(events[:2]); err != nil || n != 2 {
		t.Fatalf("import: %d %v", n, err)
	}
	if n, err := dst.Import(events); err != nil || n != 1 {
		t.Fatalf("re-import: %d %v", n, err)
	}
	if err := dst.Verify(); err != nil {
		t.Fatal(err)
	}
	bad := events[2]
	bad.Prev = ""
	if err := dst.AppendRaw(bad); err == nil {
		t.Fatal("bad raw event accepted")
	}
	other, _ := Open(t.TempDir())
	other.Append("a", "k", "someone", nil)
	if _, err := other.Import(events); err == nil {
		t.Fatal("divergent chain accepted")
	}
}

func TestReadFromAndBlobs(t *testing.T) {
	s, _ := Open(t.TempDir())
	s.Append("a", "k", "", nil)
	ev, off, err := s.ReadFrom("a", 0)
	if err != nil || len(ev) != 1 {
		t.Fatal(err)
	}
	s.Append("a", "k2", "", nil)
	ev, _, _ = s.ReadFrom("a", off)
	if len(ev) != 1 || ev[0].Kind != "k2" {
		t.Fatalf("tail read %+v", ev)
	}
	sha, err := s.PutBlob([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetBlob(sha)
	if err != nil || string(b) != "hello" {
		t.Fatalf("blob %q %v", b, err)
	}
	if _, err := s.GetBlob("../../x"); err == nil {
		t.Fatal("bad blob id accepted")
	}
}

func TestOnWriteSeesAppendsAndImports(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	a.OnWrite = func(e Event) { seen = append(seen, e.Item+":"+e.Kind) }
	e1, err := a.Append("it_1", "k.one", "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.OnWrite = a.OnWrite
	if _, err := b.Import([]Event{e1}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, ","); got != "it_1:k.one,it_1:k.one" {
		t.Fatalf("OnWrite saw %q", got)
	}
}
