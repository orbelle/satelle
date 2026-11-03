package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

// fetchRemoteHistory brings, in hosted mode, the history of a story the local
// store left on the server (ledger.MarkHistoryRemote) back from its hosted chain,
// so the read that follows folds the whole story (sty_0dc600e1). It reports
// whether it fetched. The fetched story ages out again on the next trim unless it
// is held by then.
func fetchRemoteHistory(ctx context.Context, a *app.App, id string) (bool, error) {
	if a == nil || a.Store == nil || !verb.HostedMode(a.Config) {
		return false, nil
	}
	remote, err := a.Store.Ledger.HasMark(ctx, id, ledger.MarkHistoryRemote)
	if err != nil || !remote {
		return false, err
	}
	server := resolveServer("")
	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		return false, err
	}
	conn, err := newHostedClient(ctx, server, a.RepoRoot).OpenChain(ctx)
	if err != nil {
		return false, fmt.Errorf("open the story chain: %w", err)
	}
	defer conn.Close()
	events, latest, err := conn.ReadStory(ctx, project, id, "")
	if err != nil {
		return false, fmt.Errorf("read the history of %s: %w", id, err)
	}
	now := time.Now().UTC()
	for _, ev := range events {
		e, perr := ledgerEntryOf(id, ev)
		if perr != nil {
			return false, fmt.Errorf("decode %s of %s: %w", ev.ID, id, perr)
		}
		if _, uerr := a.Store.Ledger.Upsert(ctx, e, now); uerr != nil {
			return false, fmt.Errorf("apply %s of %s: %w", ev.ID, id, uerr)
		}
	}
	if latest != "" {
		if err := hosted.SaveStoryHead(server, project, a.RepoRoot, id, latest); err != nil {
			return false, fmt.Errorf("save the head of %s: %w", id, err)
		}
	}
	return true, a.Store.Ledger.ClearMarks(ctx, ledger.MarkHistoryRemote, []string{id})
}

// fetchBeforeRead runs fetchRemoteHistory for a story verb. A fetch that fails is
// said on errw and the read goes on with what the local store holds.
func fetchBeforeRead(cmd *cobra.Command, id string) {
	a, err := appFrom(cmd)
	if err != nil {
		return
	}
	if _, ferr := fetchRemoteHistory(cmd.Context(), a, id); ferr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: history is on the server and was not fetched: %v\n", id, ferr)
	}
}

// fetchBeforeWrite runs fetchRemoteHistory for each story a write is about to
// append to, so the write builds on the hosted head instead of starting a chain
// of its own that no push can publish (sty_00212637). It is the one pre-write
// fetch: every caller that writes a story's events outside dispatch names its
// stories here. A held story costs one mark lookup. A fetch that fails is said
// on errw with the repair, and the write goes on: local first, never blocked.
func fetchBeforeWrite(cmd *cobra.Command, ids ...string) {
	a, err := appFrom(cmd)
	if err != nil {
		return
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ferr := fetchRemoteHistory(cmd.Context(), a, id); ferr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: history is on the server and was not fetched before this write: %v — if push then refuses it, run `satelle sync workstate rechain %s`\n", id, ferr, id)
		}
	}
}

// fetchChainBeforeWrite reads, in hosted mode, the hosted chain of an item a
// write is about to append to and takes it under the local chain, so the write
// builds on the hosted head (sty_0810c2a3). fetchBeforeWrite fetches only a
// story whose history was trimmed; a task's history is never trimmed, yet its
// chain may hold events another location made that this checkout never read. A
// fetch that fails is said on errw and the write goes on: local first, never
// blocked.
func fetchChainBeforeWrite(cmd *cobra.Command, id string) {
	a, err := appFrom(cmd)
	if err != nil || a == nil || a.Store == nil || id == "" || !verb.HostedMode(a.Config) {
		return
	}
	ctx, errw := cmd.Context(), cmd.ErrOrStderr()
	server := resolveServer("")
	project, err := resolveBoundProject(a.Config, a.RepoRoot)
	if err != nil {
		fmt.Fprintf(errw, "%s: the hosted chain was not read before this write: %v\n", id, err)
		return
	}
	c := newChainPull(newHostedClient(ctx, server, a.RepoRoot), server, project, a.RepoRoot, errw)
	defer c.close()
	rd, err := c.reader(ctx)
	if err == nil {
		_, err = c.materializeStory(ctx, a, rd, id, &storyOutcome{}, time.Now().UTC())
	}
	if err != nil && !errors.Is(err, hosted.ErrChainUnsupported) && !errors.Is(err, hosted.ErrLoginRequired) {
		fmt.Fprintf(errw, "%s: the hosted chain was not read before this write: %v — if push then refuses it, run `satelle sync workstate pull`\n", id, err)
	}
}

// newSyncWorkstateFetchCmd is the fetch as a verb, for a reader outside the CLI:
// satelled execs it when the web story page opens a story whose history is on the
// server, then reads the mirror the verb refreshes.
func newSyncWorkstateFetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "fetch <story-id>...",
		Short:       "Fetch from the server the history of stories this checkout does not hold",
		Annotations: needsStore(),
		Args:        cobra.MinimumNArgs(1),
		Long: `fetch reads, in hosted mode, the whole hosted chain of each named story whose
history the local store left on the server, and applies it. A story whose history
is held, or a checkout that is local only, is left as it is.

story get and story route fetch the same way before they read.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			return runStoryFetch(cmd.Context(), cmd.OutOrStdout(), a, args)
		},
	}
}

func runStoryFetch(ctx context.Context, w io.Writer, a *app.App, ids []string) error {
	fetched := 0
	for _, id := range ids {
		ok, err := fetchRemoteHistory(ctx, a, id)
		if err != nil {
			return err
		}
		if ok {
			fetched++
		}
	}
	if err := pushMirrorAfterWorkstate(ctx, a, fetched); err != nil {
		fmt.Fprintf(w, "workstate: mirror push warning: %v\n", err)
	}
	fmt.Fprintf(w, "Fetched the history of %d stor(y/ies).\n", fetched)
	return nil
}
