package cli

// sty_5bf84ba4: the deployed.version stamp belongs to the repository, so in a
// linked worktree the drift gate reads the main working tree's stamp — a
// worktree's own .satelle (gitignored, so never carrying the stamp) is not
// refused as unstamped.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// releaseBinary sets a release binary version so the drift gate is live under
// `go test` (a dev build never gates).
func releaseBinary(t *testing.T, ver string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = ver
	t.Cleanup(func() { buildinfo.Version = old })
}

// stampedMainWithWorktree builds a real git main tree whose .satelle carries
// mainStamp, and a linked worktree of it; it returns both roots.
func stampedMainWithWorktree(t *testing.T, mainStamp string) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = gitMainTree(t, base)
	writeFile(t, main, filepath.Join(config.DefaultDataDir, deployedVersionName), "satelle.version: "+mainStamp+"\n")
	wt = filepath.Join(base, "wt")
	gitIn(t, main, "worktree", "add", "-q", wt)
	return main, wt
}

func evalPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return r
}

// AC1 + AC3: a linked worktree of a stamped repo is not refused, with or without
// its own unstamped .satelle; an unstamped main tree still is.
func TestCheckBreakingDriftLinkedWorktreeReadsMainStamp(t *testing.T) {
	releaseBinary(t, "0.0.900")

	// Non-vacuity control (AC3): under this release version an initialized but
	// unstamped (non-worktree) repo is refused, naming init.
	plain := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plain, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := checkBreakingDrift(plain); err == nil || !strings.Contains(err.Error(), "satelle init") {
		t.Fatalf("unstamped main tree must be refused naming init, got err=%v", err)
	}

	main, wt := stampedMainWithWorktree(t, "0.0.900")
	if got, want := evalPath(t, stampDataDir(wt)), evalPath(t, filepath.Join(main, config.DefaultDataDir)); got != want {
		t.Fatalf("stampDataDir(worktree) = %s, want the main tree's %s", got, want)
	}

	t.Run("worktree has an unstamped .satelle", func(t *testing.T) {
		wtData := filepath.Join(wt, config.DefaultDataDir)
		if err := os.MkdirAll(wtData, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(wtData) })
		if _, err := os.Stat(filepath.Join(wtData, deployedVersionName)); !os.IsNotExist(err) {
			t.Fatalf("fixture: worktree .satelle must have no stamp (stat err=%v)", err)
		}
		if bw, err := checkBreakingDrift(wt); bw != nil || err != nil {
			t.Fatalf("worktree of a stamped repo must not be gated: warn=%v err=%v", bw, err)
		}
	})
	t.Run("worktree has no .satelle", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(wt, config.DefaultDataDir)); !os.IsNotExist(err) {
			t.Fatalf("fixture: worktree must have no .satelle (stat err=%v)", err)
		}
		if bw, err := checkBreakingDrift(wt); bw != nil || err != nil {
			t.Fatalf("worktree of a stamped repo must not be gated: warn=%v err=%v", bw, err)
		}
	})
}

// AC2: in a linked worktree the drift decision is the one the main tree's stamp
// gives — here a refusal across an init-heals Breaking release — even when the
// worktree's own .satelle carries a stamp that alone would be quiet.
func TestCheckBreakingDriftLinkedWorktreeFollowsMainStampDecision(t *testing.T) {
	releaseBinary(t, "0.0.900")

	main, wt := stampedMainWithWorktree(t, "0.0.100")
	// The worktree's own stamp is current: read alone it gives no drift at all.
	writeFile(t, wt, filepath.Join(config.DefaultDataDir, deployedVersionName), "satelle.version: 0.0.900\n")
	if bw, err := breakingDrift("0.0.900", "0.0.900", nil); bw != nil || err != nil {
		t.Fatalf("fixture: the worktree's own stamp must be quiet: warn=%v err=%v", bw, err)
	}

	mainWarn, mainErr := checkBreakingDrift(main)
	if mainErr == nil {
		t.Fatalf("fixture: main stamp 0.0.100 must cross an init-heals Breaking release and refuse (warn=%v)", mainWarn)
	}
	if !strings.Contains(mainErr.Error(), "satelle init") {
		t.Fatalf("main-tree refusal must name init: %v", mainErr)
	}

	wtWarn, wtErr := checkBreakingDrift(wt)
	if wtErr == nil || wtErr.Error() != mainErr.Error() {
		t.Fatalf("worktree decision must be the main tree's:\n got err=%v\nwant err=%v", wtErr, mainErr)
	}
	if (wtWarn == nil) != (mainWarn == nil) {
		t.Fatalf("worktree warning must match main: got %v want %v", wtWarn, mainWarn)
	}
}
