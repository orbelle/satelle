package syncstate

import (
	"testing"
	"time"
)

func TestMarkSubstrateAttemptMergesOnlyItsStamp(t *testing.T) {
	home := t.TempDir()
	checked := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	attempt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := RecordSubstrate(home, "/repo", map[string]AreaCheck{"skills": {State: "behind"}}, checked); err != nil {
		t.Fatal(err)
	}
	if err := MarkSubstrateAttempt(home, "/repo", attempt); err != nil {
		t.Fatal(err)
	}
	got, _, _ := Load(home, "/repo")
	if !got.SubstrateCheckedAttempt.Equal(attempt) {
		t.Fatalf("attempt stamp = %v, want %v", got.SubstrateCheckedAttempt, attempt)
	}
	if a := got.SubstrateAreas["skills"]; a.State != "behind" || !a.CheckedAt.Equal(checked) {
		t.Fatalf("the stamp clobbered the recorded areas: %+v", got.SubstrateAreas)
	}
	if err := RecordSubstrate(home, "/repo", map[string]AreaCheck{"skills": {State: "in step"}}, attempt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = Load(home, "/repo"); !got.SubstrateCheckedAttempt.Equal(attempt) {
		t.Fatalf("RecordSubstrate clobbered the attempt stamp: %v", got.SubstrateCheckedAttempt)
	}
}
