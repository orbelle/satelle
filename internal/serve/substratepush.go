// Substrate push leg (sty_a66e1846).
//
// Work state reaches the developer's personal project on its own: the ingest
// Pusher execs `satelle sync workstate push`. Documents used to move only on an
// explicit `satelle sync`, so a document edit reached nothing until the
// developer remembered. This leg closes that on the same seam: each reconcile
// pass execs the documents push, then `sync pull --fast-forward-only` so what is
// only behind the hosted copy comes in with no command typed (sty_0fa4f732).
// Config areas are git-owned (sty_70acfe3a), so no pass pushes them
// (sty_a003a459).
//
// serve decides nothing here. Whether an area is opted in, whether the repo is
// bound, and whether the machine is behind, diverged or unmerged are the verbs'
// calls — an all-local repo gets a no-op exit zero, a refused area records its
// state in syncstate and exits non-zero. serve only schedules, runs, and logs a
// failure once per streak (Reconciler.reportSubstrate).
package serve

import (
	"context"
	"errors"
)

// substratePushArgs is the exact argv of each substrate verb the loop runs: the
// documents push, then the fast-forward pull (sty_0fa4f732). --snapshot-only is
// what makes an unattended push safe: it never falls back to the plain upload that
// cannot tell a diverged machine from an in-step one. --fast-forward-only is
// what makes the unattended pull safe: it writes only an area that is behind with
// no local change, so it never overwrites a local edit, and it never touches
// work-state. The pull runs after the push, so a push refused because the
// hosted copy is ahead (it writes nothing) is followed by the pull that brings
// the area in. The loop never deploys or reconciles, and never passes --force
// (which would override a newer hosted snapshot) or --prune (which would drop
// hosted files).
var substratePushArgs = [][]string{
	{"sync", "documents", "push", "--snapshot-only"},
	{"sync", "pull", "--fast-forward-only"},
}

// substratePushViaCLI runs every substrate verb inside repoPath. A refusal in one
// does not stop the next; the errors are joined.
// The leg runs under one gate key, skipped as errChildBusy while it is running.
func substratePushViaCLI(ctx context.Context, repoPath string) error {
	return gated(ctx, repoPath, kindSubstrate, false, func(bin string) error {
		var errs []error
		for _, args := range substratePushArgs {
			if _, err := execVerb(ctx, bin, repoPath, nil, args...); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
}
