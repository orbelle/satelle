package syncstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

const policyFileName = "command-policy.json"

type PolicySub struct {
	Source      string    `json:"source"`
	SourceKind  string    `json:"source_kind"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	RulesTOML   string    `json:"rules_toml,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

type PolicyState struct {
	CheckedAttempt time.Time            `json:"checked_attempt,omitempty"`
	Subs           map[string]PolicySub `json:"subs,omitempty"`
}

func PolicyPath(globalDir, repoPath string) string {
	return filepath.Join(globalDir, config.RepoKey(repoPath), policyFileName)
}

func LoadPolicy(globalDir, repoPath string) (PolicyState, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadPolicyLocked(globalDir, repoPath)
}

func loadPolicyLocked(globalDir, repoPath string) (PolicyState, bool, error) {
	b, err := os.ReadFile(PolicyPath(globalDir, repoPath))
	if err != nil {
		if os.IsNotExist(err) {
			return PolicyState{}, false, nil
		}
		return PolicyState{}, false, err
	}
	var s PolicyState
	if json.Unmarshal(b, &s) != nil {
		return PolicyState{}, false, nil
	}
	return s, true, nil
}

func RecordPolicy(globalDir, repoPath, name string, sub PolicySub, now time.Time) error {
	return recordPolicy(globalDir, repoPath, func(s *PolicyState) {
		sub.FetchedAt, sub.LastError, sub.LastErrorAt = now, "", time.Time{}
		s.Subs[name] = sub
	})
}

func RecordPolicyFailure(globalDir, repoPath, name, source, kind, reason string, now time.Time) error {
	return recordPolicy(globalDir, repoPath, func(s *PolicyState) {
		sub := s.Subs[name]
		if sub.Source != source || sub.SourceKind != kind {
			sub = PolicySub{Source: source, SourceKind: kind}
		}
		sub.LastError, sub.LastErrorAt = reason, now
		s.Subs[name] = sub
	})
}

func MarkPolicyAttempt(globalDir, repoPath string, now time.Time) error {
	return recordPolicy(globalDir, repoPath, func(s *PolicyState) { s.CheckedAttempt = now })
}

func recordPolicy(globalDir, repoPath string, merge func(*PolicyState)) error {
	mu.Lock()
	defer mu.Unlock()
	s, _, err := loadPolicyLocked(globalDir, repoPath)
	if err != nil {
		return err
	}
	if s.Subs == nil {
		s.Subs = map[string]PolicySub{}
	}
	merge(&s)
	dst := PolicyPath(globalDir, repoPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), policyFileName+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
