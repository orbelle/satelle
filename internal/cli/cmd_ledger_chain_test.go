package cli

import (
	"encoding/json"
	"testing"
)

func createChainStory(t *testing.T, title string) string {
	t.Helper()
	out, err := runRoot(t, "story", "create", "--title", title)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	return created.ID
}
