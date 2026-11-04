package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// nodeFetchOn runs a node's pre-run fetch (nodeChainFetch) for ids against the
// session's checkout, under a node token, as runTaskWake wires it.
func nodeFetchOn(t *testing.T, server string, ids ...string) (string, error) {
	t.Helper()
	cfg, cfgPath, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	a := &app.App{Config: cfg, RepoRoot: config.RepoRootFromConfigPath(cfgPath), DBPath: runtimeDBPath(t), Store: db}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.WithValue(context.Background(), appCtxKey{}, a))
	hc := hosted.NewClient(server, hosted.NodeTokenStore{Server: server, Token: "node-token"}, nil)
	err = nodeChainFetch(cmd, hc, server, "probe")(context.Background(), ids...)
	return out.String(), err
}

// sty_a291a081 AC1, AC2: a node's store does not hold the story its run was woken
// for. The pre-run fetch writes the story's row from the hosted copy and brings
// its hosted chain under it, so the row's status is the chain's head — not the
// stale status on the hosted row — and story attach and story get then work on it.
func TestNodeChainFetchMaterialisesAnAbsentStory(t *testing.T) {
	ts, f, ids, want, _ := publishedStoriesWith(t, func(id string) []ledger.AppendInput {
		return []ledger.AppendInput{{StoryID: id, Kind: ledger.KindStatusTransition, Actor: "bob",
			Body: "backlog → in_progress", Payload: json.RawMessage(`{"from":"backlog","to":"in_progress"}`)}}
	}, "woken for")
	id := ids[0]
	f.mu.Lock()
	f.itemsByID["probe"][id].(map[string]any)["status"] = "backlog"
	f.mu.Unlock()
	withRepoStore(t, func(db *store.DB) {
		if _, err := db.Stories.Get(context.Background(), id); !errors.Is(err, workitem.ErrNotFound) {
			t.Fatalf("the fresh store holds %s (%v), want none", id, err)
		}
	})

	if out, err := nodeFetchOn(t, ts.URL, id); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if got := idsOf(localChain(t, id)); !slices.Equal(got, idsOf(want[id])) {
		t.Errorf("local chain = %v, want the hosted chain %v", got, idsOf(want[id]))
	}

	if out, err := runRoot(t, "story", "attach", id, "--name", "live-evidence", "--body", "attached on the node"); err != nil {
		t.Fatalf("story attach: %v\n%s", err, out)
	}
	out, err := runRoot(t, "story", "get", id)
	if err != nil {
		t.Fatalf("story get: %v\n%s", err, out)
	}
	var got workitem.Item
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&got); err != nil {
		t.Fatalf("parse story get: %v\n%s", err, out)
	}
	if got.Status != "in_progress" {
		t.Errorf("status = %q, want the chain head's in_progress, not the hosted row's backlog", got.Status)
	}
	if out, err := runRoot(t, "story", "docs", id); err != nil || !strings.Contains(out, "live-evidence") {
		t.Errorf("story docs: %v, want live-evidence listed\n%s", err, out)
	}
}

// sty_a291a081: a story the node's store already holds keeps the chain read alone;
// no Snapshot is asked for.
func TestNodeChainFetchHeldStoryReadsOnlyItsChain(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	workstateRepo(t, holdOffToml)
	id := createStoryID(t, "held")

	if out, err := nodeFetchOn(t, ts.URL, id); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if reqs := f.snapshotRequests(); len(reqs) != 0 {
		t.Errorf("snapshot requests = %d, want none for a held story", len(reqs))
	}
	if n := f.chain.readCount(id); n != 1 {
		t.Errorf("chain reads of %s = %d, want one", id, n)
	}
}

// sty_a291a081 AC3: a story neither the node's store nor the hosted project holds
// is the named error, and nothing is written.
func TestNodeChainFetchStoryAbsentFromTheProject(t *testing.T) {
	ts, _ := newFakeWorkstateServer(t)
	workstateRepo(t, holdOffToml)
	const id = "sty_nothere"

	_, err := nodeFetchOn(t, ts.URL, id)
	if !errors.Is(err, remotenode.ErrSubjectAbsent) || !strings.Contains(err.Error(), id) {
		t.Fatalf("fetch = %v, want %v naming %s", err, remotenode.ErrSubjectAbsent, id)
	}
	withRepoStore(t, func(db *store.DB) {
		if _, gerr := db.Stories.Get(context.Background(), id); !errors.Is(gerr, workitem.ErrNotFound) {
			t.Errorf("the store holds %s after the refusal (%v)", id, gerr)
		}
	})
}
