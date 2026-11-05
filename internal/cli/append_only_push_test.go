package cli

import (
	"context"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/workitem"
)

// Operational data is pushed append-only (sty_52b93691): an execution is never
// held back. These tests fake only the hosted server, which has no hold surface
// (sty_40853d02) — any hold route the push dialled would fail the test.

const (
	appendOnlyToml = "[sync]\nhold_unpushed = false\n\n[hosted]\nproject = \"probe\"\n"
	// holdBackToml leaves hold_unpushed at its default, so held-back
	// classification runs over the batch.
	holdBackToml = "[sync]\n\n[hosted]\nproject = \"probe\"\n"
)

func createExecutionID(t *testing.T, title string) string {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	it, err := db.Stories.Create(context.Background(), workitem.CreateInput{Kind: workitem.KindExecution, Title: title}, time.Now())
	if err != nil {
		t.Fatalf("create execution %q: %v", title, err)
	}
	return it.ID
}
