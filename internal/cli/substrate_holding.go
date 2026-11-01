package cli

// Whether `substrate review --apply` may raise a team-owned item's cut here
// (sty_9fc02aa4). The project tier owns a team item, and only the project's
// substrate holder publishes it (holderGate); so the cut is raised in this repo
// only when the caller is that holder and this tree's copy is the tier's, the
// copy the holder's push publishes. Everyone else is refused, told who holds it.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/substratereview"
)

// substrateHolding is the team-holding check for a's repo against server. The
// project is looked up once, on the first team item asked about, so an apply of
// repo items alone makes no network call. Every failure to establish the
// caller as the holder — no tier, no server, no project, a lookup error — holds
// nothing: the check fails closed.
func substrateHolding(ctx context.Context, a *app.App, server string) func(substratereview.ReportItem) substratereview.TeamHolding {
	var (
		once   sync.Once
		holder bool
		who    string
		tier   config.ProjectTier
	)
	lookup := func() {
		var ok bool
		if tier, ok = projectTierFor(a.RepoRoot); !ok {
			who = "unknown (no project tier)"
			return
		}
		if server == "" {
			who = `unknown (no hosted server configured — run "satelle login" or pass --server <url>)`
			return
		}
		project, err := resolveBoundProject(a.Config, a.RepoRoot)
		if err != nil {
			who = holderRefusal{LookupErr: err}.holderName()
			return
		}
		p, err := newHostedClient(ctx, server, a.RepoRoot).GetProject(ctx, project)
		switch {
		case err != nil:
			who = holderRefusal{LookupErr: err}.holderName()
		case p.Substrate == nil:
			who = "unknown (the server reports no substrate for the project)"
		case p.Substrate.Source == hosted.SubstrateRepo:
			who = "substrate repo " + p.Substrate.Repo
		case p.Substrate.Source != hosted.SubstrateHosted:
			who = fmt.Sprintf("unknown (the project's substrate source is %q)", p.Substrate.Source)
		default:
			var ref holderRefusal
			if p.Substrate.Holder != nil {
				ref.Holder = *p.Substrate.Holder
			}
			who = ref.holderName()
			holder = isSubstrateHolder(server, a.RepoRoot, p.Substrate.Holder)
		}
	}
	return func(it substratereview.ReportItem) substratereview.TeamHolding {
		once.Do(lookup)
		if !holder {
			return substratereview.TeamHolding{Holder: who}
		}
		rel, err := filepath.Rel(tier.Dir, it.Source)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return substratereview.TeamHolding{Holder: who + " (the item has no file in the project tier)"}
		}
		rel = filepath.ToSlash(rel)
		if !tier.Team.Owns(rel) {
			return substratereview.TeamHolding{Holder: who + " (the project tier does not own " + rel + ")"}
		}
		want, terr := os.ReadFile(tier.Path(rel))
		got, gerr := os.ReadFile(filepath.Join(a.DataDir, filepath.FromSlash(rel)))
		if terr != nil || gerr != nil || !bytes.Equal(got, want) {
			return substratereview.TeamHolding{Holder: who + " (this tree's copy differs from the tier; push and deploy first)"}
		}
		return substratereview.TeamHolding{Held: true, Holder: who}
	}
}
