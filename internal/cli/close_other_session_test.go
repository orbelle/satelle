package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
)

// closeDiffRunner is every reviewer on the close edge. It records the diff each
// payload references (the material file the reviewer reads); the diff rubric
// accepts only when that diff names the story's committed slice, and the reject
// rubric always rejects.
type closeDiffRunner struct {
	mu    sync.Mutex
	diffs []agentstep.DiffState
}

const closeRejectMarker = "ALWAYS-REJECT-RUBRIC"

func (*closeDiffRunner) Name() string    { return "close-diff-runner" }
func (*closeDiffRunner) Command() string { return "close-diff-runner -p {system}" }
func (r *closeDiffRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var top struct {
		Diff *struct {
			PatchPath string `json:"patch_path"`
		} `json:"diff"`
	}
	var diff agentstep.DiffState
	if json.Unmarshal([]byte(req.Payload), &top) == nil && top.Diff != nil {
		if b, err := os.ReadFile(top.Diff.PatchPath); err == nil {
			_ = json.Unmarshal(b, &diff)
		}
	}
	r.mu.Lock()
	r.diffs = append(r.diffs, diff)
	r.mu.Unlock()
	if strings.Contains(req.SystemPrompt, closeRejectMarker) {
		return []byte(`{"decision":"reject","notes":"the second gate rejects"}`), nil
	}
	for _, f := range diff.Files {
		if f == "slice.go" {
			return []byte(`{"decision":"accept","notes":"the change set is in the payload"}`), nil
		}
	}
	return []byte(`{"decision":"reject","notes":"the payload carries no change set"}`), nil
}

func (r *closeDiffRunner) seen() []agentstep.DiffState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agentstep.DiffState(nil), r.diffs...)
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
