// Package syncstate is a leaf: durable per-repo workstate-sync outcomes
// (sty_30696eeb). serve, doctor, and the mirror UI all read/write it without
// crossing the serve→cli/hosted fences.
//
// Five field groups share one file (the ledger sync pass, the held stories,
// the snapshot, substrate areas and the substrate refresh attempt stamp). Each
// writer re-loads and merges only its own group (satelled and the satelle CLI
// are separate processes). A torn read is "no state", never a parse error that
// fails doctor.
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

	// The sync group is the outcome of the last automatic ledger sync pass
	// (`satelle ledger autosync`, sto_5c706973), written by RecordSync.
	// SyncStore names the store the pass replicated with (LocalStore when the
	// repo's store is local and nothing was replicated); SyncReason is why
	// the last pass failed, empty after a success; SyncPasses is how many
	// failures in a row gave the same reason.
	SyncStore       string    `json:"sync_store,omitempty"`
	SyncLastAttempt time.Time `json:"sync_last_attempt,omitempty"`
	SyncLastSuccess time.Time `json:"sync_last_success,omitempty"`
	SyncLastFailure time.Time `json:"sync_last_failure,omitempty"`
	SyncReason      string    `json:"sync_reason,omitempty"`
	SyncPasses      int       `json:"sync_passes,omitempty"`
	// SyncPushed and SyncPulled are how many items the last pass pushed and
	// pulled; SyncForked names each item it left unsynced, "<item> (<state>)".
	// satelle doctor reports them without contacting the store (sto_3cf973ae).
	SyncPushed int      `json:"sync_pushed,omitempty"`
	SyncPulled int      `json:"sync_pulled,omitempty"`
	SyncForked []string `json:"sync_forked,omitempty"`
	// SyncUpgrade is the command the repo needs before this satelle can sync
	// it (a pre-1.0 .satelle, sto_0f76dd4e), empty for none. The pass that
	// records it skips the repo: nothing synced and nothing failed.
	SyncUpgrade string `json:"sync_upgrade,omitempty"`

	// PushHeld is the stories the last project push held until their code is on
	// the git remote (story id -> reason, sty_c203fd62). Holding is intended while
	// a story is in flight, so it is kept apart from SyncReason.
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
	// refresh of SubstrateAreas. It only debounces that refresh; it says
	// nothing about the areas' state.
	SubstrateCheckedAttempt time.Time `json:"substrate_checked_attempt,omitempty"`
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

// LocalStore is the SyncStore a pass records for a repo whose ledger store is
// local: nothing was replicated, and nothing failed.
const LocalStore = "local"

// Pass is one sync pass's outcome: the store it replicated with (LocalStore
// when nothing), why it failed ("" for a success), and its counts. Upgrade,
// when set, is the command the repo needs before it can be synced: the pass
// skipped it.
type Pass struct {
	Store, Reason  string
	Pushed, Pulled int
	Forked         []string
	Upgrade        string
}

// RecordSync merges one sync pass into the sync group and writes atomically:
// store is what the pass replicated with (LocalStore when nothing), reason is
// why it failed ("" for a success).
func RecordSync(globalDir, repoPath, store, reason string, now time.Time) error {
	return RecordPass(globalDir, repoPath, Pass{Store: store, Reason: reason}, now)
}

// RecordPass is RecordSync with the pass's counts, which replace the last
// pass's. A pass with Upgrade set records the command and clears the store,
// reason and counts, leaving the last success and failure as they were;
// every other pass clears SyncUpgrade.
func RecordPass(globalDir, repoPath string, p Pass, now time.Time) error {
	return record(globalDir, repoPath, func(s *State) {
		if p.Upgrade != "" {
			s.SyncUpgrade = p.Upgrade
			s.SyncLastAttempt = now
			s.SyncStore, s.SyncReason, s.SyncPasses = "", "", 0
			s.SyncPushed, s.SyncPulled, s.SyncForked = 0, 0, nil
			return
		}
		s.SyncUpgrade = ""
		s.SyncStore = p.Store
		s.SyncLastAttempt = now
		s.SyncPushed, s.SyncPulled, s.SyncForked = p.Pushed, p.Pulled, p.Forked
		if p.Reason == "" || p.Store == LocalStore {
			s.SyncLastSuccess = now
			s.SyncReason = ""
			s.SyncPasses = 0
			return
		}
		s.SyncLastFailure = now
		if p.Reason == s.SyncReason {
			s.SyncPasses++
		} else {
			s.SyncReason = p.Reason
			s.SyncPasses = 1
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
	return record(globalDir, repoPath, func(s *State) {
		if len(held) == 0 {
			s.PushHeld = nil
			return
		}
		s.PushHeld = maps.Clone(held)
	})
}

// RecordSnapshot merges the snapshot-leg fields (the CLI's snapshot to
// satelled) and writes atomically.
func RecordSnapshot(globalDir, repoPath string, success bool, reason string, now time.Time) error {
	return record(globalDir, repoPath, func(s *State) {
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

// RecordSubstrate merges the verified check of the given areas into the
// substrate group, leaving every other area's record as it was.
func RecordSubstrate(globalDir, repoPath string, areas map[string]AreaCheck, now time.Time) error {
	if len(areas) == 0 {
		return nil
	}
	return record(globalDir, repoPath, func(s *State) {
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
	return record(globalDir, repoPath, func(s *State) { s.SubstrateCheckedAttempt = now })
}

func record(globalDir, repoPath string, merge func(*State)) error {
	mu.Lock()
	defer mu.Unlock()
	s, _, err := loadLocked(globalDir, repoPath)
	if err != nil {
		return err
	}
	s.RepoPath = strings.TrimSpace(repoPath)
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
