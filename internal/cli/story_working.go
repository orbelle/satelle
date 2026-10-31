package cli

// Who last worked on a story (sty_40853d02). Every move into a performing state
// appends a working_on row to the story's ledger (verb.recordWorkingOn,
// sty_9c782f0e); it travels with the story's other events, so another machine
// sees it after a pull. `story get`, `story list` and the session-start advisory
// show the latest row — from the local ledger only, so reading a story never
// dials the server — and a push the chain's base check refused names it
// (presenceIn). This is advisory presence: nothing here, or anywhere, reads a
// working_on row to refuse an action.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// workingOnRow is the latest working_on row of one story.
type workingOnRow struct {
	verb.WorkingOnPayload
	At time.Time
}

// name is how the row's location is shown: its label, else its location id.
func (r workingOnRow) name() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Location
}

// age is how long ago the row was written, as of now.
func (r workingOnRow) age(now time.Time) time.Duration {
	return max(0, now.Sub(r.At)).Round(time.Minute)
}

// who is the location and session the row names, as the displays quote them.
func (r workingOnRow) who() string {
	s := r.name()
	if r.Session != "" {
		s += " (" + r.Session + ")"
	}
	return s
}

// workingMark is the `working_on` object a story row carries when another
// location last worked it. Text is the human line, and what the compact (text)
// rendering shows.
// Advisory is always true: the mark is presence, and refuses nothing.
type workingMark struct {
	Location string `json:"location"`
	Label    string `json:"label,omitempty"`
	Session  string `json:"session,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	State    string `json:"state,omitempty"`
	At       string `json:"at"`
	Advisory bool   `json:"advisory"`
	Text     string `json:"text"`
}

func newWorkingMark(r workingOnRow, now time.Time) workingMark {
	return workingMark{
		Location: r.Location, Label: r.Label, Session: r.Session, Worktree: r.Worktree, State: r.State,
		At: r.At.UTC().Format(time.RFC3339), Advisory: true,
		Text: fmt.Sprintf("working: %s, %s ago (advisory)", r.who(), r.age(now)),
	}
}

// presenceIn is the newest working_on row another location wrote among events —
// who a refused push names as having moved the story (sty_9c782f0e). A row with
// no location, or self's own, names no one else.
func presenceIn(events []ledger.Entry, self string) (workingOnRow, bool) {
	var out workingOnRow
	found := false
	for _, e := range events {
		var p verb.WorkingOnPayload
		if e.Kind != ledger.KindWorkingOn || json.Unmarshal(e.Payload, &p) != nil || p.Location == "" || p.Location == self {
			continue
		}
		if !found || !e.CreatedAt.Before(out.At) {
			out, found = workingOnRow{WorkingOnPayload: p, At: e.CreatedAt}, true
		}
	}
	return out, found
}

// presenceText is the line a refusal quotes for a presence row.
func presenceText(r workingOnRow) string {
	return verb.WorkingOnText(r.WorkingOnPayload) + " since " + r.At.UTC().Format(time.RFC3339)
}

// otherWorking reads the ledger for the stories another location last worked on
// that are still open: story id → latest working_on row. A row with no location
// (an unbound repo), this machine's own row, and a story that is terminal on its
// route name no other worker and are left out. Empty when the ledger is not open.
func otherWorking(ctx context.Context, a *app.App) map[string]workingOnRow {
	if a == nil || a.Store == nil || a.Store.Ledger == nil {
		return nil
	}
	latest := map[string]workingOnRow{}
	_ = a.Store.Ledger.ForEachKind(ctx, "", ledger.KindWorkingOn, func(e ledger.Entry) error {
		var p verb.WorkingOnPayload
		if e.StoryID != "" && json.Unmarshal(e.Payload, &p) == nil {
			latest[e.StoryID] = workingOnRow{WorkingOnPayload: p, At: e.CreatedAt} // oldest first: the last one wins
		}
		return nil
	})
	self, _ := thisLocation(a)
	out := map[string]workingOnRow{}
	for id, r := range latest {
		if r.Location == "" || r.Location == self {
			continue
		}
		if it, err := a.Store.Stories.Get(ctx, id); err == nil && terminalOnItsRoute(ctx, a, it) {
			continue
		}
		out[id] = r
	}
	return out
}

// terminalOnItsRoute reports whether it sits at a terminal state of the route that
// governs it, read from the app's own document index: the session-start hook runs
// with no verb wiring, so the answer cannot come through the verb layer. A story
// whose route does not resolve is not terminal.
func terminalOnItsRoute(ctx context.Context, a *app.App, it workitem.Item) bool {
	wfs, err := a.Store.DocIndex.List(ctx, "workflows")
	if err != nil {
		return false
	}
	route, _, err := wfgovern.RouteFor(wfs, it)
	return err == nil && route.Spec.IsTerminalState(it.Status)
}

// dispatchWithWorking is dispatch for the read verbs that mark a story another
// location last worked on.
func dispatchWithWorking(cmd *cobra.Command, name string, req map[string]any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	resp, err := verb.Dispatch(cmd.Context(), name, body)
	if err != nil {
		return err
	}
	if a, aerr := appFrom(cmd); aerr == nil && a != nil {
		now := time.Now()
		marks := map[string]workingMark{}
		for id, r := range otherWorking(cmd.Context(), a) {
			marks[id] = newWorkingMark(r, now)
		}
		resp = markWorkingStories(resp, marks)
	}
	if name == "story-get" {
		resp = withRemoteRun(cmd, requestStoryID(req), resp)
	}
	return renderResponse(cmd, name, requestStoryID(req), resp)
}

// markWorkingStories adds a `working_on` member to the story object (or each
// object of a list) whose id has a mark. The member is spliced onto the row's
// own JSON, so every other field keeps its place. A response that is neither, or
// has no marked story in it, is returned as it came.
func markWorkingStories(raw json.RawMessage, marks map[string]workingMark) json.RawMessage {
	if len(marks) == 0 {
		return raw
	}
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case strings.HasPrefix(trimmed, "{"):
		if out, ok := spliceWorking(json.RawMessage(trimmed), marks); ok {
			return out
		}
	case strings.HasPrefix(trimmed, "["):
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) != nil {
			return raw
		}
		changed := false
		for i, row := range rows {
			if out, ok := spliceWorking(row, marks); ok {
				rows[i], changed = out, true
			}
		}
		if changed {
			if out, err := json.Marshal(rows); err == nil {
				return out
			}
		}
	}
	return raw
}

func spliceWorking(row json.RawMessage, marks map[string]workingMark) (json.RawMessage, bool) {
	var w struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(row, &w) != nil {
		return row, false
	}
	m, ok := marks[w.ID]
	if !ok {
		return row, false
	}
	s := strings.TrimSpace(string(row))
	end := strings.LastIndex(s, "}")
	if !strings.HasPrefix(s, "{") || end < 0 {
		return row, false
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return row, false
	}
	sep := ","
	if strings.TrimSpace(s[1:end]) == "" {
		sep = ""
	}
	return json.RawMessage(s[:end] + sep + `"working_on":` + string(mb) + s[end:]), true
}
