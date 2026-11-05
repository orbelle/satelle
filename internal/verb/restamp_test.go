package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
)

// fakeWorkflowResolver drives story-restamp in tests: byCategory answers
// WorkflowNameFor, and presence in states marks a workflow as resolving (its
// value is the declared lifecycle states).
type fakeWorkflowResolver struct {
	byCategory map[string]string
	states     map[string][]string
}

func (f *fakeWorkflowResolver) WorkflowNameFor(_ context.Context, category string) (string, error) {
	return f.byCategory[category], nil
}

func (f *fakeWorkflowResolver) WorkflowStates(_ context.Context, name string) ([]string, bool, error) {
	s, ok := f.states[name]
	return s, ok, nil
}

// wireResolver wires a fake workflow resolver, resetting on cleanup.
func wireResolver(t *testing.T, f *fakeWorkflowResolver) {
	t.Helper()
	withWiring(t)
	verb.SetWorkflowResolver(f)
}

// dispatchErr dispatches a verb expected to FAIL and returns the error.
func dispatchErr(t *testing.T, name string, req any) error {
	t.Helper()
	b, _ := json.Marshal(req)
	_, err := verb.Dispatch(context.Background(), name, b)
	if err == nil {
		t.Fatalf("dispatch %s: expected an error", name)
	}
	return err
}

// --- small tag helpers ---

func tagValue(tags []string, key string) string {
	for _, t := range tags {
		if strings.HasPrefix(t, key+":") {
			return strings.TrimPrefix(t, key+":")
		}
	}
	return ""
}

func countPrefix(tags []string, prefix string) int {
	n := 0
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			n++
		}
	}
	return n
}
