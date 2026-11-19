package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	var issues []doctorIssue
	var notes []string
	for _, h := range harnessesInUse(in.r.Tree, in.cfg.Agents) {
		if ctx.Err() != nil {
			return []doctorIssue{{Check: checkContextName, Problem: errUnchecked.Error()}}, nil
		}
		text, notice, idx := renderSession(in.r, src, h, true)
		switch {
		case idx != nil:
			paths := make([]string, 0, len(idx.Entries))
			for _, e := range idx.Entries {
				paths = append(paths, e.Path)
			}
			issues = append(issues, doctorIssue{
				Check: checkContextName,
				Problem: fmt.Sprintf("%s: the session-start context is %d bytes with every [hook] inject file inline, over its limit of %d bytes (.satelle/harness.toml [%s] context_limit_bytes); a session receives each of these as an index line: %s",
					h, idx.InlineBytes, idx.Limit, h, strings.Join(paths, ", ")),
				Fix: fmt.Sprintf("cut or move files out of [hook] inject in .satelle/satelle.toml, or raise [%s] context_limit_bytes in .satelle/harness.toml", h),
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
			edits := editScope(in.cfg.Workflows, ri.it)
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
