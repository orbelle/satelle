// Package syncstate is a leaf: durable per-repo workstate-sync outcomes
// (sty_30696eeb). serve, doctor, and the mirror UI all read/write it without
// crossing the serve→cli/hosted fences.
//
// Five field groups share one file (push, snapshot, substrate areas, the
// substrate refresh attempt stamp, and story conflicts). Each writer re-loads and merges only its
// own group (satelled and `satelle sync` are separate processes). A torn read
// is "no state", never a parse error that fails doctor.
package syncstate

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

const fileName = "workstate-sync.json"

// State is one repo's last recorded workstate-sync outcome.
type State struct {
	RepoPath string `json:"repo_path"`
	// Scope is the sync scope (local|personal|team) and Binding the [sync]
	// project it was resolved for; ScopeAt is when a resolve first found it.
	// The offline checks read them through RecordedScope.
	Scope   string    `json:"scope,omitempty"`
	Binding string    `json:"binding,omitempty"`
	ScopeAt time.Time `json:"scope_at,omitempty"`

	PushLastSuccess time.Time `json:"push_last_success,omitempty"`
	PushLastFailure time.Time `json:"push_last_failure,omitempty"`
	PushReason      string    `json:"push_reason,omitempty"`
	PushPasses      int       `json:"push_passes,omitempty"`
	// PushHeld is the stories the last project push held until their code is on
	// the git remote (story id -> reason, sty_c203fd62). Holding is intended while
	// a story is in flight, so it is kept apart from PushReason, which names only
	// what the push failed on.
	PushHeld map[string]string `json:"push_held,omitempty"`

	// The snapshot group is the outcome of the full or delta snapshot this
	// home's CLI posts to satelled, written by cli.withUISource: a refused
	// snapshot sets SnapshotReason, a full snapshot that lands clears it, and
	// `satelle status` names it.
	SnapshotLastSuccess time.Time `json:"snapshot_last_success,omitempty"`
	SnapshotLastFailure time.Time `json:"snapshot_last_failure,omitempty"`
	SnapshotReason      string    `json:"snapshot_reason,omitempty"`
	SnapshotPasses      int       `json:"snapshot_passes,omitempty"`

	// SubstrateAreas is the last verified state of each substrate area against
	// the hosted copy (sty_7562055c). The states are the words snapsync.Status
	// produces, recorded by whoever just asked it — never assumed from an
	// operation having succeeded. Which paths are unmerged is NOT kept here: the
	// area's synced base owns that.
	SubstrateAreas map[string]SubstrateArea `json:"substrate_areas,omitempty"`

	// SubstrateCheckedAttempt is when a session start last started a detached
	// `satelle sync status` to refresh SubstrateAreas. It only debounces that
	// refresh; it says nothing about the areas' state.
	SubstrateCheckedAttempt time.Time `json:"substrate_checked_attempt,omitempty"`
	// StoryConflicts are the stories a sync pass did not apply because the local
	// and hosted copies differ in content (sty_6e5fc41c). A pass records the
	// disagreement and never resolves it; a later pass that applies the story, or
	// finds both copies identical, clears it.
	StoryConflicts map[string]StoryConflict `json:"story_conflicts,omitempty"`
}

// StoryConflict is one story the local and hosted copies disagree on: why the
// pass left it, and when. The holders name this location and the other one that
// last worked the story ("unknown" when none is known); Source is what found it.
// A conflict is local sync state only: no pass writes it onto the story's chain.
type StoryConflict struct {
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
	LocalHolder  string    `json:"local_holder,omitempty"`
	RemoteHolder string    `json:"remote_holder,omitempty"`
	Source       string    `json:"source,omitempty"`
}

// SubstrateArea is one area's recorded state, the files that state is about
// (what a push would publish, or where the hosted copy differs) and when it was
// verified. A record written before Paths existed loads with none.
type SubstrateArea struct {
	State     string    `json:"state"`
	Paths     []string  `json:"paths,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// AreaCheck is what one check verified for an area: its state and the files
// that state is about.
type AreaCheck struct {
	State string
	Paths []string
}

var mu sync.Mutex

func pathFor(globalDir, repoPath string) string {
	return filepath.Join(globalDir, config.RepoKey(repoPath), fileName)
}

// Load returns the recorded state. Missing or torn file → ok=false, err=nil.
func Load(globalDir, repoPath string) (State, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked(globalDir, repoPath)
}

func loadLocked(globalDir, repoPath string) (State, bool, error) {
	b, err := os.ReadFile(pathFor(globalDir, repoPath))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, false, nil
		}
		return State{}, false, err
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return State{}, false, nil
	}
	return s, true, nil
}

// RecordPush merges the push-leg fields and writes atomically.
func RecordPush(globalDir, repoPath string, success bool, reason, scope string, now time.Time) error {
	return record(globalDir, repoPath, scope, func(s *State) {
		if success {
			s.PushLastSuccess = now
			s.PushReason = ""
			s.PushPasses = 0
			return
		}
		s.PushLastFailure = now
		if reason != "" && reason == s.PushReason {
			s.PushPasses++
		} else {
			s.PushReason = reason
			s.PushPasses = 1
		}
	})
}

// RecordPushHeld replaces the held stories of the push leg with held; an empty
// map clears them. Nothing is written when there is nothing held and nothing
// recorded, so a repo with no state gains no file.
func RecordPushHeld(globalDir, repoPath string, held map[string]string) error {
	if len(held) == 0 {
		s, ok, err := Load(globalDir, repoPath)
		if err != nil || !ok || len(s.PushHeld) == 0 {
			return err
		}
	}
	return record(globalDir, repoPath, "", func(s *State) {
		if len(held) == 0 {
			s.PushHeld = nil
			return
		}
		s.PushHeld = maps.Clone(held)
	})
}

// ClearPushReasonIf drops the recorded push failure when match accepts its
// reason, leaving the failure and success times as they were: for a failure
// something other than a successful push has healed, such as `satelle init`
// re-stamping past the drift gate that refused it. Nothing is written when no
// recorded reason matches, so a repo with no state gains no file.
func ClearPushReasonIf(globalDir, repoPath string, match func(string) bool) error {
	s, ok, err := Load(globalDir, repoPath)
	if err != nil || !ok || s.PushReason == "" || !match(s.PushReason) {
		return err
	}
	return record(globalDir, repoPath, "", func(s *State) {
		if s.PushReason != "" && match(s.PushReason) {
			s.PushReason = ""
			s.PushPasses = 0
		}
	})
}

// RecordSnapshot merges the snapshot-leg fields (the CLI's snapshot to
// satelled) and writes atomically.
func RecordSnapshot(globalDir, repoPath string, success bool, reason, scope string, now time.Time) error {
	return record(globalDir, repoPath, scope, func(s *State) {
		if success {
			s.SnapshotLastSuccess = now
			s.SnapshotReason = ""
			s.SnapshotPasses = 0
			return
		}
		s.SnapshotLastFailure = now
		if reason != "" && reason == s.SnapshotReason {
			s.SnapshotPasses++
		} else {
			s.SnapshotReason = reason
			s.SnapshotPasses = 1
		}
	})
}

// RecordScope records the work-state scope a pass resolved and nothing else, so
// a pass skipped because the work state is local writes neither a success nor a
// failure. An empty scope writes nothing.
func RecordScope(globalDir, repoPath, scope string) error {
	if scope == "" {
		return nil
	}
	return record(globalDir, repoPath, scope, func(*State) {})
}

// RecordResolvedScope records the scope a resolve found for binding, leaving
// every other field as it was. A resolve that finds what is already recorded
// writes nothing, so a read-only command leaves the state as it was.
func RecordResolvedScope(globalDir, repoPath string, scope config.Scope, binding string, now time.Time) error {
	binding = strings.TrimSpace(binding)
	if s, ok, err := Load(globalDir, repoPath); err == nil && ok && s.Scope == scope.String() && s.Binding == binding && !s.ScopeAt.IsZero() {
		return nil
	}
	return record(globalDir, repoPath, scope.String(), func(s *State) {
		s.Binding = binding
		s.ScopeAt = now
	})
}

// RecordedScope is the scope the offline checks judge sync from: the last
// resolve's, when it was made for the current binding. An empty binding, no
// record, or a record for another binding (a rebind since) is local.
func RecordedScope(globalDir, repoPath, binding string) config.Scope {
	binding = strings.TrimSpace(binding)
	if binding == "" {
		return config.LocalScope
	}
	s, ok, err := Load(globalDir, repoPath)
	if err != nil || !ok || s.Binding != binding {
		return config.LocalScope
	}
	scope, err := config.ParseScope(s.Scope)
	if err != nil {
		return config.LocalScope
	}
	return scope
}

// RecordSubstrate merges the verified check of the given areas into the
// substrate group, leaving every other area's record as it was.
func RecordSubstrate(globalDir, repoPath string, areas map[string]AreaCheck, now time.Time) error {
	if len(areas) == 0 {
		return nil
	}
	return record(globalDir, repoPath, "", func(s *State) {
		if s.SubstrateAreas == nil {
			s.SubstrateAreas = map[string]SubstrateArea{}
		}
		for area, check := range areas {
			s.SubstrateAreas[area] = SubstrateArea{State: check.State, Paths: check.Paths, CheckedAt: now}
		}
	})
}

// MarkSubstrateAttempt stamps now as the last start of a substrate refresh,
// leaving every other field as it was.
func MarkSubstrateAttempt(globalDir, repoPath string, now time.Time) error {
	return record(globalDir, repoPath, "", func(s *State) { s.SubstrateCheckedAttempt = now })
}

// RecordStoryConflicts merges one pass's findings into the story-conflict group:
// conflicts (id -> reason) are recorded, cleared ids are dropped, and every other
// story's record is left as it was. Nothing is written when there is nothing to
// change, so an in-step repo gains no state file.
func RecordStoryConflicts(globalDir, repoPath string, conflicts map[string]string, cleared []string, now time.Time) error {
	detailed := make(map[string]StoryConflict, len(conflicts))
	for id, reason := range conflicts {
		detailed[id] = StoryConflict{Reason: reason}
	}
	return RecordStoryConflictDetails(globalDir, repoPath, detailed, cleared, now)
}

// RecordStoryConflictDetails is RecordStoryConflicts with the holders and source
// of each conflict. A conflict's At is set here; a story still in conflict for
// the same reason keeps the time it was first found.
func RecordStoryConflictDetails(globalDir, repoPath string, conflicts map[string]StoryConflict, cleared []string, now time.Time) error {
	if len(conflicts) == 0 && len(cleared) == 0 {
		return nil
	}
	if len(conflicts) == 0 {
		s, ok, err := Load(globalDir, repoPath)
		if err != nil || !ok || len(s.StoryConflicts) == 0 {
			return err
		}
	}
	return record(globalDir, repoPath, "", func(s *State) {
		for _, id := range cleared {
			delete(s.StoryConflicts, id)
		}
		for id, c := range conflicts {
			if s.StoryConflicts == nil {
				s.StoryConflicts = map[string]StoryConflict{}
			}
			c.At = now
			if prev, ok := s.StoryConflicts[id]; ok && prev.Reason == c.Reason {
				c.At = prev.At
				if c.LocalHolder == "" && c.RemoteHolder == "" && c.Source == "" {
					c = prev
				}
			}
			s.StoryConflicts[id] = c
		}
		if len(s.StoryConflicts) == 0 {
			s.StoryConflicts = nil
		}
	})
}

func record(globalDir, repoPath, scope string, merge func(*State)) error {
	mu.Lock()
	defer mu.Unlock()
	s, _, err := loadLocked(globalDir, repoPath)
	if err != nil {
		return err
	}
	s.RepoPath = strings.TrimSpace(repoPath)
	if scope != "" {
		s.Scope = scope
	}
	merge(&s)
	return writeLocked(globalDir, repoPath, s)
}

func writeLocked(globalDir, repoPath string, s State) error {
	dir := filepath.Join(globalDir, config.RepoKey(repoPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dst := pathFor(globalDir, repoPath)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
