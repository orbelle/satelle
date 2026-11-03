package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func newSyncWorkstateFollowCmd() *cobra.Command {
	var server string
	return &cobra.Command{
		Use:   "follow",
		Short: "Follow the bound project's story chains live through the chain subscription",
		// Every event follow applies came from the hosted project (sty_0367d20c).
		Annotations: hostedOrigin(needsStore()),
		Args:        cobra.NoArgs,
		Long: `follow subscribes, in hosted mode, to every event appended to the bound
project's stories and applies each as it arrives, resuming each story after the
last event this checkout holds. A story whose history the local store left on
the server is fetched whole when it changes recently, and any story the store
need not hold is trimmed again after its event. An event for a story this
checkout has no row of is left on the server.

It runs until it is stopped or the hosted server ends the stream. When the
hosted server has no subscription it exits non-zero, and satelled then keeps the
snapshot pass. It never pushes. A local-only checkout is a no-op.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWorkstateFollow(cmd, server)
		},
	}
}

func runSyncWorkstateFollow(cmd *cobra.Command, serverArg string) error {
	a, err := appFrom(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if repoNotBound(out, a, "workstate follow") {
		return nil
	}
	if !verb.HostedMode(a.Config) {
		fmt.Fprintln(out, "workstate follow: not signed in — run \"satelle login\"; the local store is kept whole")
		return nil
	}
	server := resolveServer(serverArg)
	if scope, err := syncScopeGate(cmd, a.Config, a.RepoRoot, server, "workstate follow: nothing to follow"); err != nil || scope == config.LocalScope {
		return err
	}
	if server == "" {
		return fmt.Errorf("no hosted server configured — run \"satelle login\" or pass --server <url>")
	}
	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		return err
	}
	conn, err := newHostedClient(cmd.Context(), server, a.RepoRoot).OpenChain(cmd.Context())
	if err != nil {
		return fmt.Errorf("open the story chain: %w", err)
	}
	defer conn.Close()
	f := &follower{a: a, w: cmd.ErrOrStderr(), conn: conn, server: server, project: project}
	cur, err := hosted.LoadWorkstateCursor(server, project, a.RepoRoot)
	if err != nil {
		return fmt.Errorf("load story heads: %w", err)
	}
	err = conn.SubscribeStories(cmd.Context(), project, cur.StoryHeads, f.apply)
	fmt.Fprintf(out, "Followed project %q: %d event(s) applied, %d stor(y/ies) fetched, %d trimmed.\n", project, f.applied, f.fetched, f.trimmed)
	switch {
	case errors.Is(err, hosted.ErrChainUnsupported):
		return fmt.Errorf("workstate follow: the server has no story chain subscription — keep the snapshot pass: %w", err)
	case errors.Is(err, context.Canceled):
		return nil
	}
	return err
}

// follower applies the events a subscription delivers, one story at a time.
type follower struct {
	a               *app.App
	w               io.Writer
	conn            *hosted.ChainConn
	server, project string

	applied, fetched, trimmed int
}

// apply takes one delivered event. A story with no local row is left on the
// server. A story whose history is on the server is fetched whole when it is now
// performing — its row, moved by the event when the event is a status transition —
// and otherwise left there; outside team scope it is always fetched, since only a
// team-scope store leaves history on the server (sty_7c01ca14). Any other story
// takes the event, its row is rebuilt from its chain, its head is saved, and in team
// scope it is trimmed when the store need not hold it.
func (f *follower) apply(storyID string, ev hosted.ChainEvent) error {
	ctx := context.Background()
	led := f.a.Store.Ledger
	row, err := f.a.Store.Stories.Get(ctx, storyID)
	if errors.Is(err, workitem.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	e, err := ledgerEntryOf(storyID, ev)
	if err != nil {
		return fmt.Errorf("decode %s of %s: %w", ev.ID, storyID, err)
	}
	now := time.Now().UTC()
	team := teamScope(f.a)
	remote, err := led.HasMark(ctx, storyID, ledger.MarkHistoryRemote)
	if err != nil {
		return err
	}
	if remote {
		moved := row
		if e.Kind == ledger.KindStatusTransition {
			if to := verb.TransitionTo(e); to != "" {
				moved.Status = to
			}
		}
		if team && !verb.StoryPerforming(ctx)(moved) {
			return nil
		}
		if _, err := fetchRemoteHistory(ctx, f.a, storyID); err != nil {
			return err
		}
		f.fetched++
	} else {
		if _, err := led.Upsert(ctx, e, now); err != nil {
			return fmt.Errorf("apply %s of %s: %w", ev.ID, storyID, err)
		}
		if err := hosted.SaveStoryHead(f.server, f.project, f.a.RepoRoot, storyID, ev.ID); err != nil {
			return fmt.Errorf("save the head of %s: %w", storyID, err)
		}
	}
	f.applied++
	if row.Kind == workitem.KindStory {
		if _, err := verb.RebuildRow(ctx, f.a.Store.Stories, led, row, now); err != nil {
			fmt.Fprintf(f.w, "workstate follow: rebuild %s from its chain: %v\n", storyID, err)
		}
	}
	if !team {
		return nil
	}
	held, err := verb.HeldStories(ctx, led, f.a.Store.Stories)
	if err != nil {
		return err
	}
	n, err := verb.TrimStoryHistory(ctx, led, held, map[string]string{storyID: ev.ID})
	f.trimmed += n
	return err
}
