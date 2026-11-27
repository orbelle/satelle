package remotenode

import (
	"io/fs"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func baseSubstrate() substrateState {
	return substrateState{
		"skills/x": {
			Mode: fs.ModeDir | 0o555,
			ID:   config.FileID{Dev: 1, Ino: 73, Ctime: 100, Nlink: 2},
		},
		"skills/x/run.sh": {
			Mode:   0o444,
			Size:   12,
			SHA256: "aaaa",
			ID:     config.FileID{Dev: 1, Ino: 80, Ctime: 100, Nlink: 1},
		},
	}
}

func alterSubstrate(st substrateState, p string, f func(*substrateEntry)) {
	e := st[p]
	f(&e)
	st[p] = e
}

func TestDiffSubstrateIgnoresADirectoryInode(t *testing.T) {
	before, after := baseSubstrate(), baseSubstrate()
	alterSubstrate(after, "skills/x", func(e *substrateEntry) { e.ID.Ino = 108 })
	if lines := diffSubstrate(before, after); len(lines) != 0 {
		t.Fatalf("a directory whose inode number alone changed is reported: %v", lines)
	}
}

func TestDiffSubstrateNamesTheChange(t *testing.T) {
	cases := []struct {
		name  string
		alter func(substrateState)
		want  string
	}{
		{"file content", func(s substrateState) {
			alterSubstrate(s, "skills/x/run.sh", func(e *substrateEntry) { e.SHA256 = "bbbb" })
		}, "changed skills/x/run.sh"},
		{"file replaced", func(s substrateState) {
			alterSubstrate(s, "skills/x/run.sh", func(e *substrateEntry) { e.ID.Ino = 81 })
		}, "changed skills/x/run.sh"},
		{"file ctime", func(s substrateState) {
			alterSubstrate(s, "skills/x/run.sh", func(e *substrateEntry) { e.ID.Ctime = 101 })
		}, "changed skills/x/run.sh"},
		{"file size", func(s substrateState) {
			alterSubstrate(s, "skills/x/run.sh", func(e *substrateEntry) { e.Size = 13 })
		}, "changed skills/x/run.sh"},
		{"file added", func(s substrateState) {
			s["skills/x/new.md"] = substrateEntry{Mode: 0o444, ID: config.FileID{Dev: 1, Ino: 90}}
		}, "added skills/x/new.md"},
		{"file removed", func(s substrateState) {
			delete(s, "skills/x/run.sh")
		}, "removed skills/x/run.sh"},
		{"directory ctime", func(s substrateState) {
			alterSubstrate(s, "skills/x", func(e *substrateEntry) { e.ID.Ctime = 101 })
		}, "changed skills/x"},
		{"directory nlink", func(s substrateState) {
			alterSubstrate(s, "skills/x", func(e *substrateEntry) { e.ID.Nlink = 3 })
		}, "changed skills/x"},
		{"directory dev", func(s substrateState) {
			alterSubstrate(s, "skills/x", func(e *substrateEntry) { e.ID.Dev = 2 })
		}, "changed skills/x"},
		{"directory mode", func(s substrateState) {
			alterSubstrate(s, "skills/x", func(e *substrateEntry) { e.Mode = fs.ModeDir | 0o755 })
		}, "changed skills/x"},
		{"directory inode and ctime", func(s substrateState) {
			alterSubstrate(s, "skills/x", func(e *substrateEntry) { e.ID.Ino, e.ID.Ctime = 108, 101 })
		}, "changed skills/x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, after := baseSubstrate(), baseSubstrate()
			c.alter(after)
			lines := diffSubstrate(before, after)
			if !slices.Equal(lines, []string{c.want}) {
				t.Fatalf("diff = %v, want [%s]", lines, c.want)
			}
		})
	}
}
