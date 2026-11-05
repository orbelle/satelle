package cli

import (
	"context"
	"testing"
)

func transitionPayload(from, to string) map[string]any {
	return map[string]any{"from": from, "to": to}
}

func localStatus(t *testing.T, id string) string {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	row, err := db.Stories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Status
}
