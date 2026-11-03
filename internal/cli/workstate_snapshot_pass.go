package cli

// The paged work-state Snapshot pass shared by `sync workstate pull` (and so
// `sync rehydrate`) and `sync workstate snapshot` (sty_2cb4e1f6). It owns the
// since-token, the resume checkpoint and the fall-back to a full pass, so the
// two verbs cannot diverge on them. The serve reconcile loop reaches it only by
// running the flag-free `sync workstate snapshot` verb.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
)

// snapshotMode picks how a page is checked before it is applied: a pull refuses
// a page that would lose local information; a snapshot applies hosted-wins.
type snapshotMode int

const (
	snapshotModePull snapshotMode = iota
	snapshotModeSnapshot
)

// Pass kinds, as printed in the summary line.
const (
	passFull    = "full"
	passSince   = "since"
	passResumed = "resumed"
	passScoped  = "scoped"
)

// passReport is what a pass did. Hosted holds the synced items this pass
// received (never ledger rows, which a pass does not retain).
type passReport struct {
	Kind                string
	Pages               int
	Items, Ledger, Kept int
	Hosted              []hosted.WorkstateItem
	// Scope is the scope a scoped pass fetched — the resolved one, widened by the
	// members a pull took from the server — so the caller's attachment and hold
	// steps cover them too. Zero for an unscoped pass.
	Scope scopeResolution
	// Stories is what the pass found about story disagreements (sty_6e5fc41c).
	Stories storyOutcome
}

// Reasons a story is recorded as in conflict.
const (
	conflictLocalAhead  = "local copy is ahead of hosted"
	conflictPullRefused = "pull refused: local and hosted differ"
	// A forced pull wrote the hosted copy onto the row and left the chain alone.
	conflictForcedHostedWins = "forced pull: the local chain disagrees with the hosted row; the row takes the hosted copy, the chain is unchanged"
)

// storyOutcome is the story disagreements one pass found: Conflicts maps a story
// id to why the pass left it alone, Cleared lists the stories the pass applied
// or found identical on both sides. Its methods accept a nil receiver, so a
// caller with nowhere to record passes nil.
type storyOutcome struct {
	Conflicts map[string]string
	Cleared   []string
	// Moved lines name a done or cancelled story a pull rebuilt to a non-terminal
	// status; the pass prints each as it lands.
	Moved []string
}

func (o *storyOutcome) moved(line string) {
	if o != nil {
		o.Moved = append(o.Moved, line)
	}
}

func (o *storyOutcome) conflict(id, reason string) {
	if o == nil {
		return
	}
	if o.Conflicts == nil {
		o.Conflicts = map[string]string{}
	}
	o.Conflicts[id] = reason
}

func (o *storyOutcome) clear(id string) {
	if o != nil {
		o.Cleared = append(o.Cleared, id)
	}
}

// refused records the stories a refused pull named. Any other error records
// nothing.
func (o *storyOutcome) refused(err error) {
	var pc *workstatePullConflict
	if errors.As(err, &pc) {
		for _, id := range pc.Stories {
			o.conflict(id, conflictPullRefused)
		}
	}
}

// complete reports whether the pass received everything it asked for rather than
// only what changed since an earlier one.
func (r passReport) complete() bool { return r.Kind == passFull || r.Kind == passScoped }

// label is the "(since pass, 2 page(s))" the summary lines carry.
func (r passReport) label() string {
	return fmt.Sprintf("%s pass, %d page(s)", r.Kind, r.Pages)
}

// snapshotAreas names the synced work-state areas a token was saved for.
func snapshotAreas(optIn map[string]bool) string {
	var areas []string
	for _, area := range WorkstateAreas {
		if optIn[area] {
			areas = append(areas, area)
		}
	}
	slices.Sort(areas)
	return strings.Join(areas, ",")
}

// snapshotPass is one attempt at a pass: the apply state its pages accumulate.
type snapshotPass struct {
	a       *app.App
	errw    io.Writer
	server  string
	project string
	optIn   map[string]bool
	force   bool
	mode    snapshotMode
	areas   string
	storeID string

	// keep is the token of the last completed pass, carried through checkpoints
	// so an interrupted pass never replaces it. since is the since_token this
	// pass began with, re-sent beside a resume page token.
	keep  string
	since string

	// chain reads the hosted chain of each story a page applies; the caller closes it.
	chain *chainPull

	rep passReport
}

// runPagedWorkstateSnapshot pulls the project's work-state in pages, applying
// and checkpointing each page as it arrives. It starts from the stored position
// — a resume checkpoint, else the last completed pass's since-token — unless
// force is set, none is stored, or the synced areas changed; a token the
// server rejects falls back to one full pass. The token is replaced only when
// every page has landed.
func runPagedWorkstateSnapshot(ctx context.Context, a *app.App, errw io.Writer, client *hosted.Client, server, project string, optIn map[string]bool, force bool, mode snapshotMode) (passReport, error) {
	areas := snapshotAreas(optIn)
	stored, err := hosted.LoadSnapshotToken(server, project, a.RepoRoot)
	if err != nil {
		return passReport{}, fmt.Errorf("load snapshot token: %w", err)
	}
	storeID, err := a.Store.Ledger.InstanceID(ctx)
	if err != nil {
		return passReport{}, fmt.Errorf("local store id: %w", err)
	}
	p := &snapshotPass{
		a: a, errw: errw, server: server, project: project,
		optIn: optIn, force: force, mode: mode, areas: areas, storeID: storeID,
		keep: stored.Token, rep: passReport{Kind: passFull},
		chain: newChainPull(client, server, project, a.RepoRoot, errw),
	}
	defer p.chain.close()
	if err := holdHosted(ctx, a, p.chain); err != nil {
		return passReport{}, err
	}
	var opts hosted.SnapshotOptions
	positioned := stored.Token != "" || stored.ResumePageToken != ""
	discard := ""
	switch {
	case stored.Areas != areas:
		discard = "synced work-state areas changed"
	case stored.StoreID != storeID:
		discard = "local store changed"
	}
	switch {
	case force:
		// A forced pass is full and ignores any checkpoint; the stored token
		// stays until the pass completes.
	case positioned && discard != "":
		fmt.Fprintf(errw, "workstate: since-token discarded: %s\n", discard)
		if err := hosted.ClearSnapshotToken(server, project, a.RepoRoot); err != nil {
			return passReport{}, fmt.Errorf("clear snapshot token: %w", err)
		}
		p.keep = ""
	case stored.ResumePageToken != "":
		opts = hosted.SnapshotOptions{PageToken: stored.ResumePageToken, SinceToken: stored.ResumeSince}
		p.since = stored.ResumeSince
		p.rep.Kind = passResumed
	case stored.Token != "":
		opts = hosted.SnapshotOptions{SinceToken: stored.Token}
		p.since = stored.Token
		p.rep.Kind = passSince
	}

	onPage := func(pg hosted.SnapshotPage) error { return p.page(ctx, pg) }
	res, err := client.Snapshot(ctx, project, "", opts, onPage)
	if errors.Is(err, hosted.ErrSinceTokenRejected) {
		what := "since-token"
		if opts.PageToken != "" {
			what = "resume page token"
		}
		fmt.Fprintf(errw, "workstate: %s discarded: rejected by server (%s); ran a full pass\n",
			what, strings.TrimPrefix(err.Error(), hosted.ErrSinceTokenRejected.Error()+": "))
		if cerr := hosted.ClearSnapshotToken(server, project, a.RepoRoot); cerr != nil {
			return passReport{}, fmt.Errorf("clear snapshot token: %w", cerr)
		}
		p.keep, p.since = "", ""
		p.rep = passReport{Kind: passFull}
		res, err = client.Snapshot(ctx, project, "", hosted.SnapshotOptions{}, onPage)
	}
	p.rep.Pages = res.Pages
	if err != nil {
		// Pages already applied stay; their story views are regenerated now so
		// the interrupted pass leaves nothing stale behind.
		if p.rep.Items+p.rep.Ledger > 0 {
			_, _, _ = verb.SyncStoryBacklog(ctx, a.Store.Stories, time.Now().UTC())
		}
		return p.rep, err
	}
	now := time.Now().UTC()
	if err := p.settle(ctx, now); err != nil {
		return p.rep, err
	}
	// A task this page set did not carry is read once per project pass: a since
	// pass carries no row the server did not change (sty_0810c2a3).
	if err := p.chain.sweepTasks(ctx, a, optIn, &p.rep.Stories, now); err != nil {
		return p.rep, fmt.Errorf("%w — re-run is safe", err)
	}
	p.chain.flush()
	if _, err := trimHostedHistory(ctx, a, server, project); err != nil {
		return p.rep, fmt.Errorf("trim the local history: %w", err)
	}
	if res.SyncToken == "" {
		// The server issued no token (it does not page yet): nothing to send
		// next time, so the next pass is a full one.
		err = hosted.ClearSnapshotToken(server, project, a.RepoRoot)
	} else {
		err = hosted.SaveSnapshotToken(server, project, a.RepoRoot, hosted.SnapshotToken{Token: res.SyncToken, Areas: areas, StoreID: storeID, SavedAt: now})
	}
	if err != nil {
		return p.rep, fmt.Errorf("save snapshot token: %w", err)
	}
	return p.rep, nil
}

// settle closes a pass whose pages are all in: the story views are regenerated
// once. A story's status needs no forwarding here: each page rebuilds, from the
// fold of their chains, the rows it applied and the row of every story it wrote
// events to, so a transition that lands on a later page than its story moves
// that row when the page lands (sty_a28c64dd).
func (p *snapshotPass) settle(ctx context.Context, now time.Time) error {
	if _, _, verr := verb.SyncStoryBacklog(ctx, p.a.Store.Stories, now); verr != nil {
		return fmt.Errorf("regenerate story views after %d item(s), %d ledger: %w — store rows landed; re-run is safe", p.rep.Items, p.rep.Ledger, verr)
	}
	return nil
}

// optedIn is the part of a page the synced areas let through.
func (p *snapshotPass) optedIn(pg hosted.SnapshotPage) ([]hosted.WorkstateItem, []hosted.WorkstateLedgerRow) {
	items, ledgerRows := pg.Items, pg.Ledger
	if !(p.optIn["stories"] || p.optIn["executions"]) {
		items = nil
	}
	if !p.optIn["ledger"] {
		ledgerRows = nil
	}
	return items, ledgerRows
}

// page applies one page and checkpoints it. A repeated row is absorbed by the
// upsert-by-id, so a resumed or since pass may deliver one twice.
func (p *snapshotPass) page(ctx context.Context, pg hosted.SnapshotPage) error {
	if err := p.apply(ctx, pg); err != nil {
		return err
	}
	if pg.NextPageToken == "" {
		return nil
	}
	now := time.Now().UTC()
	cp := hosted.SnapshotToken{
		Token: p.keep, Areas: p.areas, StoreID: p.storeID, SavedAt: now,
		ResumePageToken: pg.NextPageToken, ResumeSince: p.since,
	}
	if err := hosted.SaveSnapshotToken(p.server, p.project, p.a.RepoRoot, cp); err != nil {
		return fmt.Errorf("save snapshot checkpoint: %w", err)
	}
	return nil
}

// apply checks one page against the local store (a pull only), upserts it and
// forwards the statuses it carries. It touches no token or checkpoint, so the
// scoped pass can share it with the full one.
func (p *snapshotPass) apply(ctx context.Context, pg hosted.SnapshotPage) error {
	items, ledgerRows := p.optedIn(pg)
	if p.mode == snapshotModePull && !p.force {
		if err := checkWorkstatePullConflicts(ctx, p.a, p.optIn, items, ledgerRows); err != nil {
			p.rep.Stories.refused(err)
			return err
		}
	}
	now := time.Now().UTC()
	_, nItems, nLedger, nKept, err := upsertWorkstatePage(ctx, p.a, p.optIn, items, ledgerRows, p.force, now, &p.rep.Stories, p.chain)
	p.rep.Items += nItems
	p.rep.Ledger += nLedger
	p.rep.Kept += nKept
	for _, line := range p.rep.Stories.Moved {
		fmt.Fprintf(p.errw, "workstate: %s\n", line)
	}
	p.rep.Stories.Moved = nil
	if err != nil {
		return err
	}
	if p.mode == snapshotModePull {
		p.rep.Hosted = append(p.rep.Hosted, items...)
	}
	return nil
}
