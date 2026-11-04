package mirror

import (
	"slices"
	"testing"
)

func TestDirtyMarks(t *testing.T) {
	home := t.TempDir()
	if got := DirtyRepos(home); len(got) != 0 {
		t.Fatalf("fresh home has marks: %v", got)
	}
	for _, p := range []string{"/repo/b", "/repo/a", "/repo/a"} {
		if err := MarkDirty(home, p); err != nil {
			t.Fatal(err)
		}
	}
	if got := DirtyRepos(home); !slices.Equal(got, []string{"/repo/a", "/repo/b"}) {
		t.Fatalf("DirtyRepos = %v, want each marked repo once", got)
	}
	if err := ClearDirty(home, "/repo/a"); err != nil {
		t.Fatal(err)
	}
	if err := ClearDirty(home, "/repo/a"); err != nil {
		t.Fatalf("clearing an absent mark: %v", err)
	}
	if got := DirtyRepos(home); !slices.Equal(got, []string{"/repo/b"}) {
		t.Fatalf("DirtyRepos after clear = %v", got)
	}
}
