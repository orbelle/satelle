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

type State struct {
	RepoPath string `json:"repo_path"`

	SyncStore       string    `json:"sync_store,omitempty"`
	SyncLastAttempt time.Time `json:"sync_last_attempt,omitempty"`
	SyncLastSuccess time.Time `json:"sync_last_success,omitempty"`
	SyncLastFailure time.Time `json:"sync_last_failure,omitempty"`
	SyncReason      string    `json:"sync_reason,omitempty"`
	SyncPasses      int       `json:"sync_passes,omitempty"`
	SyncPushed      int       `json:"sync_pushed,omitempty"`
	SyncPulled      int       `json:"sync_pulled,omitempty"`
	SyncForked      []string  `json:"sync_forked,omitempty"`
	SyncUpgrade     string    `json:"sync_upgrade,omitempty"`

	PushHeld map[string]string `json:"push_held,omitempty"`

	SnapshotLastSuccess time.Time `json:"snapshot_last_success,omitempty"`
	SnapshotLastFailure time.Time `json:"snapshot_last_failure,omitempty"`
	SnapshotReason      string    `json:"snapshot_reason,omitempty"`
	SnapshotPasses      int       `json:"snapshot_passes,omitempty"`

	SubstrateAreas map[string]SubstrateArea `json:"substrate_areas,omitempty"`

	SubstrateCheckedAttempt time.Time `json:"substrate_checked_attempt,omitempty"`
}

type SubstrateArea struct {
	State     string    `json:"state"`
	Paths     []string  `json:"paths,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type AreaCheck struct {
	State string
	Paths []string
}

var mu sync.Mutex

func pathFor(globalDir, repoPath string) string {
	return filepath.Join(globalDir, config.RepoKey(repoPath), fileName)
}

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

const LocalStore = "local"

type Pass struct {
	Store, Reason  string
	Pushed, Pulled int
	Forked         []string
	Upgrade        string
}

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
