package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
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
