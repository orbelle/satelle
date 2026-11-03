//go:build clislow

package cli

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// sty_8fc70019 AC3, AC4: over one set of stories in every shape, the dry run reports
// each story as the real run then does, each exactly once, and exits non-zero for
// the refusals in both.
func TestSlowRechainDryRunMatchesRun(t *testing.T) {
	ts, f, ids, wants, _ := publishedStories(t, "head seed", "mid seed", "reordered", "local work", "unlinkable", "in sync")
	forkAll(t, ts, f)
	headSeed, midSeed, reordered, work, unlinkable, inSync := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]

	w := wants[midSeed]
	setLocalChain(t, midSeed, relinked(spliced(w[:2], localChain(t, midSeed), w[2:])...))
	w = wants[reordered]
	setLocalChain(t, reordered, relinked(spliced([]ledger.Entry{w[1], w[0]}, w[2:])...))
	w = wants[work]
	setLocalChain(t, work, relinked(spliced(w[:2], localChain(t, work), []ledger.Entry{localWork(work)}, w[2:])...))
	setLocalChain(t, inSync, wants[inSync])
	f.chain.mu.Lock()
	f.chain.chains[chainKey("probe", unlinkable)][2].BaseId = "evt_nowhere"
	f.chain.mu.Unlock()

	shapes := map[string][]string{}
	for _, id := range ids {
		shapes[id] = chainShape(localChain(t, id))
	}

	dryOut, dryErr := runRoot(t, "sync", "workstate", "rechain", "--dry-run", "--server", ts.URL)
	if dryErr == nil {
		t.Errorf("--dry-run exited 0 with refusals:\n%s", dryOut)
	}
	for _, id := range ids {
		if got := chainShape(localChain(t, id)); !slices.Equal(got, shapes[id]) {
			t.Errorf("--dry-run changed %s: %v, want %v", id, got, shapes[id])
		}
	}
	runOut, runErr := runRoot(t, "sync", "workstate", "rechain", "--server", ts.URL)
	if runErr == nil {
		t.Errorf("rechain exited 0 with refusals:\n%s", runOut)
	}

	dry, run := rechainOutcomes(t, dryOut), rechainOutcomes(t, runOut)
	if len(dry) != len(run) {
		t.Fatalf("dry run reported %v, run reported %v", dry, run)
	}
	for id, d := range dry {
		if run[id] != d {
			t.Errorf("%s: dry run says %q, run says %q", id, d, run[id])
		}
	}
	for _, id := range []string{headSeed, midSeed, reordered} {
		if !strings.HasPrefix(run[id], "rechain: ") {
			t.Errorf("%s = %q, want it rechained", id, run[id])
		}
		sameChain(t, localChain(t, id), wants[id])
	}
	if !strings.HasPrefix(run[work], "refuse: ") || !strings.Contains(run[work], "evt_localwork (comment)") {
		t.Errorf("local work = %q, want it refused by name", run[work])
	}
	if !strings.HasPrefix(run[unlinkable], "refuse: ") || !strings.Contains(run[unlinkable], "does not link at "+wants[unlinkable][2].ID) {
		t.Errorf("unlinkable = %q, want it refused with the reason", run[unlinkable])
	}
	for _, id := range []string{work, unlinkable} {
		if got := chainShape(localChain(t, id)); !slices.Equal(got, shapes[id]) {
			t.Errorf("refused %s changed: %v, want %v", id, got, shapes[id])
		}
	}
	if _, ok := run[inSync]; ok || len(run) != 5 {
		t.Errorf("run reported %v, want the five stories that differ and not the one in sync", run)
	}

	out, err := runRoot(t, "sync", "workstate", "rechain", inSync, "--dry-run", "--server", ts.URL)
	if err != nil || rechainOutcomes(t, out)[inSync] != "nothing: the local chain is the hosted chain" {
		t.Errorf("rechain of a story in sync = %v\n%s, want nothing to rechain", err, out)
	}
}

// sty_8fc70019 AC4: discovery lists no story that is only ahead of its hosted chain,
// none the hosted project does not hold and none already on it, and a run with only
// repairable stories exits 0.
func TestSlowRechainDiscoverySkipsAheadAndUnpublished(t *testing.T) {
	ts, f, ids, wants, _ := publishedStories(t, "seeded", "ahead", "synced")
	forkAll(t, ts, f)
	seeded, ahead, synced := ids[0], ids[1], ids[2]
	setLocalChain(t, synced, wants[synced])
	setLocalChain(t, ahead, wants[ahead])
	withRepoStore(t, func(db *store.DB) {
		if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: ahead, Kind: ledger.KindComment, Actor: "bob", Body: "not pushed yet"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	unpublished := createStoryID(t, "unpublished")
	skipped := []string{ahead, synced, unpublished}
	shapes := map[string][]string{}
	for _, id := range skipped {
		shapes[id] = chainShape(localChain(t, id))
	}

	for _, args := range [][]string{{"--dry-run"}, {}} {
		out, err := runRoot(t, append([]string{"sync", "workstate", "rechain", "--server", ts.URL}, args...)...)
		if err != nil {
			t.Fatalf("rechain %v = %v\n%s, want exit 0", args, err, out)
		}
		got := rechainOutcomes(t, out)
		if len(got) != 1 || !strings.HasPrefix(got[seeded], "rechain: ") {
			t.Errorf("rechain %v reported %v, want only %s", args, got, seeded)
		}
		for _, id := range skipped {
			if strings.Contains(out, id) {
				t.Errorf("rechain %v named %s, want it skipped:\n%s", args, id, out)
			}
			if g := chainShape(localChain(t, id)); !slices.Equal(g, shapes[id]) {
				t.Errorf("rechain %v changed %s: %v, want %v", args, id, g, shapes[id])
			}
		}
	}
}
