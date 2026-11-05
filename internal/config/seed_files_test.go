package config

import (
	"testing"
)

// sty_6d43eb74 AC1: the scope-free walk reads the [team] files no config area
// owns under a local scope, while the push's gated share stays empty.
func TestTeamExtraFilesIgnoresScope(t *testing.T) {
	f := newTierFixture(t, "[team]\npaths = [\"skills/**\", \"scripts/*.sh\"]\n"+tierBoundToml, map[string]string{
		"scripts/x.sh": "#!/bin/sh\necho x\n", "scripts/notes.txt": "x\n",
	})
	gated, err := teamExtraFiles(f.cfg, LocalScope, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(gated) != 0 {
		t.Errorf("teamExtraFiles under a local scope = %v, want none", gated)
	}
	files, err := TeamExtraFiles(f.cfg, f.root, Ignore{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "scripts/x.sh" || files[0].Area != TeamArea || string(files[0].Content) != "#!/bin/sh\necho x\n" {
		t.Errorf("TeamExtraFiles = %+v, want only scripts/x.sh in the team area", files)
	}
}
