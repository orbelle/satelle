//go:build clislow

package cli

import (
	"testing"
)

// AC3/AC5: behind, diverged and unmerged each refuse the push, upload nothing,
// and leave the state they refused on in sync state.
func TestSlowSnapshotOnlyRefusesBehindDivergedUnmerged(t *testing.T) {
	for _, k := range snapKinds {
		t.Run(k.name, func(t *testing.T) {
			ts, f := newSnapServer(t)
			seedCred(t, ts.URL)
			a := publishedMachine(t, ts.URL)
			b := newSnapMachine(t, ts.URL)
			b.deploy()
			b.pullDocs()
			a.write(k.file, "from A\n")
			a.pushConfig()
			a.pushDocs()

			// B holds nothing A has not pushed: behind.
			k.refused(t, f, b)
			if got := b.recorded()[k.area].State; got != "behind" {
				t.Errorf("recorded after the behind refusal = %q, want behind", got)
			}

			// B has its own change on top: diverged.
			b.write(k.file, "from B\n")
			k.refused(t, f, b)
			if got := b.recorded()[k.area].State; got != "diverged" {
				t.Errorf("recorded after the diverged refusal = %q, want diverged", got)
			}

			// B pulls over its own change: a conflict copy is parked, unmerged.
			k.pull(b)
			k.refused(t, f, b)
			if got := b.recorded()[k.area].State; got != "unmerged" {
				t.Errorf("recorded after the unmerged refusal = %q, want unmerged", got)
			}
		})
	}
}
