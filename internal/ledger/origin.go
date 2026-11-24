package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Origin names the writer of an event: the machine id, the worker role, the
// harness when one was detected, and the worktree. It is carried in the
// event's payload under OriginKey, so it is hashed with the payload and the
// envelope is unchanged.
//
// Origin is provenance for tracing faults, not an authorisation fact: the role
// is self-reported by the writing process, so no gate, rule or grant may read
// it.
type Origin struct {
	Machine  string `json:"machine"`
	Role     string `json:"role"`
	Harness  string `json:"harness,omitempty"`
	Worktree string `json:"worktree"`
}

// OriginKey is the payload key the store owns.
const OriginKey = "origin"

// The worker roles an origin names.
const (
	RoleSession   = "session"
	RolePerformer = "performer"
	RoleJudge     = "judge"
	RoleSync      = "sync"
	RoleNode      = "node"
)

// Roles returns every worker role, in a fixed order.
func Roles() []string {
	return []string{RoleSession, RolePerformer, RoleJudge, RoleSync, RoleNode}
}

// EnvRole maps a worker value read from the environment to a role. The
// environment can claim only the dispatch roles; anything else, sync and node
// included, is an interactive session.
func EnvRole(v string) string {
	switch strings.TrimSpace(v) {
	case RolePerformer:
		return RolePerformer
	case RoleJudge:
		return RoleJudge
	}
	return RoleSession
}

var errOriginNotObject = errors.New("ledger: a payload must be a JSON object to carry its origin")

// WithOrigin returns payload with its origin set to o. Any origin the payload
// already holds, under any letter case, is removed first, so a caller can
// neither set nor strip it. With o nil a payload holding no origin is returned
// unchanged.
func WithOrigin(payload json.RawMessage, o *Origin) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		if o == nil {
			return payload, nil
		}
		return json.Marshal(map[string]*Origin{OriginKey: o})
	}
	if trimmed[0] != '{' {
		if o == nil {
			return payload, nil
		}
		return nil, errOriginNotObject
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, err
	}
	removed := false
	for k := range m {
		if strings.EqualFold(k, OriginKey) {
			delete(m, k)
			removed = true
		}
	}
	if o == nil && !removed {
		return payload, nil
	}
	if o != nil {
		b, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		m[OriginKey] = b
	}
	return json.Marshal(m)
}
