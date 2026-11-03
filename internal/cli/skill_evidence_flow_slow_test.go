//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestSlowSkillEvidenceFlowPassesWhenTheDeclaredCommandRan(t *testing.T) {
	repo := evidenceFlowRepo(t, "reject")
	id := newEvidenceStory(t, repo)
	if _, err := setStatus(t, id, "in_progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitgateOn(t, "claude", "cd /r && go test -count=1 ./..."); err != nil {
		t.Fatal(err)
	}
	stderr, err := setStatus(t, id, "verifying")
	if err != nil {
		t.Fatalf("a met declaration must not refuse, even under reject: %v\n%s", err, stderr)
	}
	notes, ok := evidenceVerdict(t, ledger.KindReviewAccept)
	if !ok || !strings.Contains(notes, "1 declared item(s) met") {
		t.Fatalf("want an accepted skill-evidence verdict reporting the met item, got ok=%v notes=%q", ok, notes)
	}
}

func TestSlowSkillEvidenceFlowNamesTheSeededOmission(t *testing.T) {
	repo := evidenceFlowRepo(t, "")
	id := newEvidenceStory(t, repo)
	if _, err := setStatus(t, id, "in_progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitgateOn(t, "claude", "ls"); err != nil { // a command, but not the declared one
		t.Fatal(err)
	}
	// Default (record): the gap is recorded and the transition proceeds.
	stderr, err := setStatus(t, id, "verifying")
	if err != nil {
		t.Fatalf("record mode must not refuse: %v\n%s", err, stderr)
	}
	notes, ok := evidenceVerdict(t, ledger.KindReviewAccept)
	if !ok || !strings.Contains(notes, `ev-step: missing command "go test"`) {
		t.Fatalf("want the omission named on the accepted verdict, got ok=%v notes=%q", ok, notes)
	}
}

func TestSlowSkillEvidenceFlowRefusesTheOmissionUnderReject(t *testing.T) {
	repo := evidenceFlowRepo(t, "reject")
	id := newEvidenceStory(t, repo)
	if _, err := setStatus(t, id, "in_progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitgateOn(t, "claude", "ls"); err != nil {
		t.Fatal(err)
	}
	stderr, err := setStatus(t, id, "verifying")
	if err == nil || !strings.Contains(err.Error()+stderr, `missing command "go test"`) {
		t.Fatalf("reject mode must refuse, naming the command: err=%v stderr=%s", err, stderr)
	}
	if notes, ok := evidenceVerdict(t, ledger.KindReviewReject); !ok || !strings.Contains(notes, `missing command "go test"`) {
		t.Errorf("want a rejecting skill-evidence verdict row naming the command, got ok=%v notes=%q", ok, notes)
	}
}
