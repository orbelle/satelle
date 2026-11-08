package ledger

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBlobGaps(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	held, _ := s.PutBlob([]byte("held"))
	gone, _ := s.PutBlob([]byte("gone"))
	bad, _ := s.PutBlob([]byte("bad"))
	if err := os.Remove(filepath.Join(s.Dir(), "blobs", gone)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "blobs", bad), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs := []BlobRef{
		{Seq: 0, Event: "e0", Kind: "k", SHA256: held, What: "a"},
		{Seq: 1, Event: "e1", Kind: "k", SHA256: gone, What: "b"},
		{Seq: 2, Event: "e2", Kind: "k", SHA256: bad, What: "c"},
	}
	gaps, err := s.BlobGaps("it", refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 2 || gaps[0].Ref.SHA256 != gone || gaps[0].Why != GapMissing || gaps[1].Ref.SHA256 != bad || gaps[1].Why != GapCorrupt {
		t.Fatalf("gaps %+v", gaps)
	}
	if got, want := gaps[0].String(), "gap: it event 1 k e1 blob "+gone+" (b): missing"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
}
