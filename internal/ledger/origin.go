package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type Origin struct {
	Machine  string `json:"machine"`
	Role     string `json:"role"`
	Harness  string `json:"harness,omitempty"`
	Worktree string `json:"worktree"`
}

const OriginKey = "origin"

const (
	RoleSession   = "session"
	RolePerformer = "performer"
	RoleJudge     = "judge"
	RoleSync      = "sync"
	RoleNode      = "node"
)

func Roles() []string {
	return []string{RoleSession, RolePerformer, RoleJudge, RoleSync, RoleNode}
}

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
