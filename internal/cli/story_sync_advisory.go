package cli

// Session-start story-sync advisory (sty_6e5fc41c): one line naming the stories
// whose local and hosted copies disagree and the stories another location last
// worked on, with where and when (sty_40853d02). It reads recorded state only —
// what the last work-state passes wrote and the local ledger's working_on rows —
// and never dials, so a session start never waits on the server. The line is
// marked unverified when the last good snapshot it rests on is older than the
// mirror freshness window. It is advisory presence, and says so: nothing is
// blocked by a story another location is on. Conflicts are reported only while some work-state area
// syncs: once every area resolves local they are dropped from the state file, so
// turning sync back on starts from a fresh check (sty_c45cb8d5).

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// storySyncMaxIDs caps how many stories the line names before it counts the rest.
const storySyncMaxIDs = 5

// storySyncAdvisoryFor is the seam runHookContext reads the line through; tests
// replace it.
var storySyncAdvisoryFor = storySyncAdvisory

// storySyncAdvisory gathers the recorded conflicts and the stories another
// location last worked on, and decides the line. It has no error channel: it
// feeds `satelle hook context`, which must always exit 0, so unreadable state is
// "nothing to report".
func storySyncAdvisory(a *app.App) string {
	if a == nil {
		return ""
	}
	var conflicts map[string]syncstate.StoryConflict
	var snapshotAt time.Time
	if st, ok, _ := syncstate.Load(config.GlobalDir(), a.RepoRoot); ok {
		conflicts, snapshotAt = st.StoryConflicts, st.SnapshotLastSuccess
	}
	// Under a local recorded scope the conflicts are moot. Offline: the scope is
	// the one the last sync resolved for the repo's binding (syncstate.RecordedScope).
	if syncstate.RecordedScope(config.GlobalDir(), a.RepoRoot, a.Config.SyncProject()) == config.LocalScope && len(conflicts) > 0 {
		_ = syncstate.RecordStoryConflicts(config.GlobalDir(), a.RepoRoot, nil, slices.Sorted(maps.Keys(conflicts)), time.Now().UTC())
		conflicts = nil
	}
	return storySyncLine(conflicts, otherWorking(context.Background(), a), snapshotAt, time.Now())
}

// storySyncLine is the DECISION: one advisory line, "" when no story is in
// conflict or last worked on elsewhere. Conflicts come first, then the stories
// another location is on, each in id order; at most storySyncMaxIDs stories are
// named. The line is marked unverified when the last good snapshot it rests on is
// older than the mirror freshness window — or unknown.
func storySyncLine(conflicts map[string]syncstate.StoryConflict, working map[string]workingOnRow, snapshotAt time.Time, now time.Time) string {
	if len(conflicts) == 0 && len(working) == 0 {
		return ""
	}
	var entries []string
	for _, id := range slices.Sorted(maps.Keys(conflicts)) {
		entries = append(entries, fmt.Sprintf("%s in conflict (%s)", id, conflicts[id].Reason))
	}
	for _, id := range slices.Sorted(maps.Keys(working)) {
		r := working[id]
		entries = append(entries, fmt.Sprintf("%s last worked at %s %s ago (advisory)", id, r.who(), r.age(now)))
	}
	more := ""
	if len(entries) > storySyncMaxIDs {
		more = fmt.Sprintf(" (+%d more)", len(entries)-storySyncMaxIDs)
		entries = entries[:storySyncMaxIDs]
	}
	line := "⚠️ satelle: story sync — " + strings.Join(entries, "; ") + more
	if snapshotAt.IsZero() || now.Sub(snapshotAt) > mirror.StaleAfter {
		when := "never"
		if !snapshotAt.IsZero() {
			when = snapshotAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		line += fmt.Sprintf(" (unverified (last checked %s))", when)
	}
	return line + " — run `satelle story list`."
}
