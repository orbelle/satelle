package cli

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/kernel"
)

const checkContextName = "context"

func checkContext(ctx context.Context, in *doctorInput) ([]doctorIssue, []string) {
	if in.cfg == nil || in.cfgErr != nil || len(in.cfg.Problems()) > 0 {
		return nil, nil
	}
	src := &sessionSource{
		Session: sessionFromEnv(), Dir: filepath.Dir(in.r.DataDir), Settings: in.cfg.Settings, Harness: in.cfg.Harness,
		Seats: func(session string) []seatView { return ledgerSeats(in, session) },
	}
	harnessPath := path.Join(".satelle", kernel.HarnessFile)
	var issues []doctorIssue
	var notes []string
	for _, h := range harnessesInUse(in.r.Tree, in.cfg.Agents) {
		if ctx.Err() != nil {
			return []doctorIssue{{Check: checkContextName, Problem: errUnchecked.Error()}}, nil
		}
		text, notice, idx := renderSession(in.r, src, h, true)
		switch {
		case idx != nil && emittedBytes(h, text, notice) <= idx.Limit:
			notes = append(notes, fmt.Sprintf("%s: %s: the session-start context is %d bytes with every [hook] inject file inline, over its limit of %d bytes (%s [%s] context_limit_bytes); a session receives the files as an index",
				checkContextName, h, idx.InlineBytes, idx.Limit, harnessPath, h))
		case idx != nil:
			issues = append(issues, doctorIssue{
				Check: checkContextName,
				Problem: fmt.Sprintf("%s: the session-start context is %d bytes as an index of the [hook] inject files, over its limit of %d bytes (%s [%s] context_limit_bytes)",
					h, emittedBytes(h, text, notice), idx.Limit, harnessPath, h),
				Fix: fmt.Sprintf("raise [%s] context_limit_bytes in %s, or shorten what the index carries: %s, the description of each [hook] inject file, or the [hook] inject list in .satelle/satelle.toml",
					h, harnessPath, path.Join(".satelle", kernel.SessionFile)),
			})
		case in.cfg.Harness[h].ContextLimitBytes > 0:
		default:
			notes = append(notes, fmt.Sprintf("%s: %s: session-start context is %d bytes, unbudgeted: .satelle/harness.toml has no [%s] context_limit_bytes", checkContextName, h, emittedBytes(h, text, notice), h))
		}
	}
	return issues, notes
}

func ledgerSeats(in *doctorInput, session string) []seatView {
	items, err := readItems(in)
	if err != nil {
		return nil
	}
	type bound struct {
		view seatView
		at   string
	}
	var seats []bound
	for _, ri := range items {
		for _, s := range ri.seats {
			if s.Session != session {
				continue
			}
			edits, _ := editScope(in.cfg.Workflows, ri.it)
			seats = append(seats, bound{
				view: seatView{Item: ri.it.ID, Title: ri.it.Title, Status: ri.it.Status, Location: s.Location, Performing: edits != "", Edits: edits},
				at:   s.BoundAt.UTC().Format(time.RFC3339Nano),
			})
		}
	}
	sort.SliceStable(seats, func(i, j int) bool {
		if seats[i].at != seats[j].at {
			return seats[i].at < seats[j].at
		}
		return seats[i].view.Item < seats[j].view.Item
	})
	var out []seatView
	for _, s := range seats {
		out = append(out, s.view)
	}
	return out
}
