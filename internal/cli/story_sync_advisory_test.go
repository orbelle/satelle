package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/verb"
)

// appendWorkingOn writes a working_on row to a's ledger as a pull from another
// machine would leave it: the story need not exist locally.
func appendWorkingOn(t *testing.T, a *app.App, storyID string, p verb.WorkingOnPayload, at time.Time) {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.Ledger.Append(t.Context(), ledger.AppendInput{
		StoryID: storyID, Kind: ledger.KindWorkingOn, Actor: "executor", Body: verb.WorkingOnText(p), Payload: payload,
	}, at); err != nil {
		t.Fatal(err)
	}
}

func TestStorySyncLine(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	row := func(location, label, session string, at time.Time) workingOnRow {
		return workingOnRow{WorkingOnPayload: verb.WorkingOnPayload{Location: location, Label: label, Session: session, State: "in_progress"}, At: at}
	}
	desk := row("loc_a", "desk", "sess_1", now.Add(-30*time.Minute))
	bare := row("loc_b", "", "", now.Add(-7*time.Hour))
	conflict := map[string]syncstate.StoryConflict{"sty_c": {Reason: conflictLocalAhead, At: fresh}}

	many := map[string]workingOnRow{}
	for _, id := range []string{"sty_1", "sty_2", "sty_3", "sty_4", "sty_5", "sty_6", "sty_7"} {
		many[id] = desk
	}

	for _, tc := range []struct {
		name      string
		conflicts map[string]syncstate.StoryConflict
		working   map[string]workingOnRow
		snapshot  time.Time
		want      []string
		not       []string
		empty     bool
	}{
		{name: "nothing", empty: true},
		{name: "conflict only", conflicts: conflict, snapshot: fresh,
			want: []string{"sty_c in conflict (" + conflictLocalAhead + ")"}, not: []string{"last worked", "unverified"}},
		{name: "another location's work", working: map[string]workingOnRow{"sty_h": desk}, snapshot: fresh,
			want: []string{"sty_h last worked at desk (sess_1) 30m0s ago (advisory)"}, not: []string{"unverified", "takeover", "held", "dead"}},
		{name: "a location without a label is named by its id", working: map[string]workingOnRow{"sty_h": bare}, snapshot: fresh,
			want: []string{"sty_h last worked at loc_b 7h0m0s ago (advisory)"}},
		{name: "conflicts come before work", conflicts: conflict, snapshot: fresh, working: map[string]workingOnRow{"sty_a": desk},
			want: []string{"sty_c in conflict"}},
		{name: "a snapshot older than the mirror window is unverified", working: map[string]workingOnRow{"sty_h": desk}, snapshot: now.Add(-2 * time.Hour),
			want: []string{"unverified (last checked 2026-10-14 10:00 UTC)"}},
		{name: "no snapshot is unverified", working: map[string]workingOnRow{"sty_h": desk},
			want: []string{"unverified (last checked never)"}},
		{name: "more than five are counted", working: many, snapshot: fresh,
			want: []string{"sty_1 last", "sty_5 last", "(+2 more)"}, not: []string{"sty_6 last", "sty_7 last"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := storySyncLine(tc.conflicts, tc.working, tc.snapshot, now)
			if tc.empty {
				if got != "" {
					t.Fatalf("line = %q, want none", got)
				}
				return
			}
			if !strings.HasPrefix(got, "⚠️ satelle:") || strings.Contains(got, "\n") {
				t.Errorf("line must be one ⚠️ satelle: line, got %q", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("line missing %q: %s", w, got)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("line must not contain %q: %s", n, got)
				}
			}
		})
	}
	if got := storySyncLine(conflict, many, fresh, now); strings.Index(got, "sty_c") > strings.Index(got, "sty_1") {
		t.Errorf("conflicts must be listed before work elsewhere: %s", got)
	}
}

// storySyncHookRepo is a bound repo, with a counting hosted server as the machine
// server so a call made at session start would be seen, whose recorded state
// holds one story in conflict and one another location last worked on.
func storySyncHookRepo(t *testing.T) (calls *atomic.Int32) {
	t.Helper()
	_, calls = storySyncScopedRepo(t, config.PersonalScope)
	return calls
}

// storySyncScopedRepo is storySyncHookRepo with the scope the last sync resolved
// for its binding given, so a test can set the scope the recorded state is read
// under.
func storySyncScopedRepo(t *testing.T, scope config.Scope) (root string, calls *atomic.Int32) {
	t.Helper()
	repo := sessionContextRepo(t)
	calls = new(atomic.Int32)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	if err := os.WriteFile(config.GlobalConfigPath(), []byte("[hosted]\nserver = \""+ts.URL+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHarnessTOML(t, repo, "[sync]\nproject = \"probe\"\n")
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	root = a.RepoRoot
	now := time.Now().UTC()
	appendWorkingOn(t, a, "sty_workedelsewhere", verb.WorkingOnPayload{Location: "loc_other_aaaaaaaa", Label: "desk", Session: "sess_other", State: "in_progress"}, now.Add(-3*time.Hour))
	_ = a.Close()
	if err := syncstate.RecordResolvedScope(config.GlobalDir(), root, scope, "probe", now); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordSnapshot(config.GlobalDir(), root, true, "", "", now); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordStoryConflicts(config.GlobalDir(), root, map[string]string{"sty_inconflict": conflictLocalAhead}, nil, now); err != nil {
		t.Fatal(err)
	}
	return root, calls
}

// recordedConflicts is the story_conflicts group of root's work-state sync file.
func recordedConflicts(t *testing.T, root string) map[string]syncstate.StoryConflict {
	t.Helper()
	st, _, err := syncstate.Load(config.GlobalDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return st.StoryConflicts
}

// sty_c45cb8d5 AC4: while the work state syncs — the ledger is where story
// conflicts are found — the recorded conflicts are reported and kept.
func TestStorySyncAdvisoryLedgerSyncedKeepsConflicts(t *testing.T) {
	root, _ := storySyncScopedRepo(t, config.PersonalScope)
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	line := storySyncAdvisory(a)
	_ = a.Close()
	if want := "sty_inconflict in conflict"; !strings.Contains(line, want) {
		t.Errorf("line missing %q: %s", want, line)
	}
	if _, ok := recordedConflicts(t, root)["sty_inconflict"]; !ok {
		t.Error("a repo still syncing its ledger must keep its recorded conflicts")
	}
}

type sessionDoc struct {
	HookSpecificOutput struct {
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
	SystemMessage string `json:"systemMessage"`
}
