package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// AC6: only the edit-gate classifier sees inline programs; the fence and the
// substrate lock candidates are unchanged (sty_5fac62b9).
func TestInlineInterpreterIsInvisibleToFenceAndLock(t *testing.T) {
	c := `cd /elsewhere && python3 -c 'print(1)'`
	if got := mutationTargets(c, inlineAnchor); len(got) != 0 {
		t.Errorf("mutationTargets(%q) = %v, want none", c, got)
	}
	if home, foreign := bashMutationTargets(`python3 -c "open('a.go','w')"`, inlineAnchor); len(home)+len(foreign) != 0 {
		t.Errorf("bashMutationTargets saw an inline program: %v %v", home, foreign)
	}
}

// AC6, hook level: the cross-repo fence neither sees nor refuses an inline program
// run from another repository's directory.
func TestFenceDoesNotRefuseInlineInterpreterInOtherRepo(t *testing.T) {
	fenceTempRootsElsewhere(t)
	repo := tempRepo(t)
	t.Chdir(repo)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := "cd " + other + " && python3 -c 'print(1)'"
	if got := mutationTargets(c, repo); len(got) != 0 {
		t.Errorf("mutationTargets(%q) = %v, want none", c, got)
	}
	for _, handler := range []string{"gate", "commitgate"} {
		if out, err := runRootIn(t, bashEvent(c), "hook", handler); err != nil {
			t.Errorf("%s refused %q: %v\n%s", handler, c, err, out)
		}
	}
}
