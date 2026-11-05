//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostSurfaceReportsRuntimeKeyShapedEntries proves the host-surface guard
// names a new <name>-<8hex> entry wherever it lands. The AC6 probe writes such a
// name as a file under ~/.config/satelle; hashTree once dropped it because the
// SATELLE_HOME runtime-key handling ran for every root and recorded only dirs.
// The roots are fakes in t.TempDir(); nothing here touches the real host.
func TestHostSurfaceReportsRuntimeKeyShapedEntries(t *testing.T) {
	home := filepath.Join(t.TempDir(), "satelle-home")
	xdg := filepath.Join(t.TempDir(), "satelle-xdg")
	live := filepath.Join(home, "live-0a1b2c3d")
	for _, d := range []string{home, xdg, live} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(live, "state.db"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real captures take the pre-existing key dirs from hostguard's snapshot,
	// which finds live-0a1b2c3d.
	roots := hostRoots{Roots: hostguard.Roots{SatelleHome: home}, xdgConfig: xdg, preExistingKeys: preExistingKeyDirs(t, home)}
	if _, ok := roots.preExistingKeys["live-0a1b2c3d"]; !ok {
		t.Fatalf("pre-existing key dir not detected: %v", roots.preExistingKeys)
	}
	before := captureHostSurfaceAt(roots)

	// Not pollution: a live service rewriting a pre-existing key dir.
	if err := os.WriteFile(filepath.Join(live, "state.db"), []byte("v2-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diffs := diffHostSurface(before, captureHostSurfaceAt(roots)); len(diffs) != 0 {
		t.Fatalf("change inside a pre-existing key dir must not trip the guard: %v", diffs)
	}

	// Pollution: new key-shaped dir and file under home, key-shaped file under xdg.
	newDir := filepath.Join(home, "newdir-4b18b0f5")
	newHomeFile := filepath.Join(home, "newfile-4b18b0f5")
	newXDGFile := filepath.Join(xdg, "newfile-4b18b0f5")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{newHomeFile, newXDGFile} {
		if err := os.WriteFile(f, []byte("probe\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	joined := strings.Join(diffHostSurface(before, captureHostSurfaceAt(roots)), "\n")
	for _, want := range []string{
		"host SATELLE_HOME (" + home + "): added newdir-4b18b0f5/",
		"host SATELLE_HOME (" + home + "): added newfile-4b18b0f5",
		"host ~/.config/satelle (" + xdg + "): added newfile-4b18b0f5",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff does not name %q; got:\n%s", want, joined)
		}
	}
}

// An empty SATELLE_HOME still yields a non-nil key set, so a new key dir under
// it is recorded rather than skipped.
func TestPreExistingKeyDirsNonNilForEmptyHome(t *testing.T) {
	if got := preExistingKeyDirs(t, t.TempDir()); got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil map, got %#v", got)
	}
}

// preExistingKeyDirs is how newHostRoots finds the key dirs already under home.
func preExistingKeyDirs(t *testing.T, home string) map[string]struct{} {
	t.Helper()
	snap, err := hostguard.Capture(hostguard.Roots{SatelleHome: home})
	if err != nil {
		t.Fatal(err)
	}
	return snap.KeyDirs()
}
