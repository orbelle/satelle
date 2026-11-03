//go:build clislow

package cli

import (
	"testing"
)

// A push refused because the hosted copy is ahead names the verb that fits:
// pull for an area nothing here changed, reconcile for one changed on both
// machines. The decision is snapsync's, by the test `sync status` reports with.
func TestSlowRefusedPushNamesPullForABehindAreaAndReconcileForADivergedOne(t *testing.T) {
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

			_, err := k.run(b) // B changed nothing: behind
			requirePullRemedy(t, k.name+" behind", err)
			requirePullRemedy(t, k.name+" behind --snapshot-only", k.refused(t, f, b))

			b.write(k.file, "from B\n") // now diverged
			_, err = k.run(b)
			requireReconcileRemedy(t, k.name+" diverged", err)
		})
	}
}
