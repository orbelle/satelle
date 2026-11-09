package syncstate

import (
	"os"
	"testing"
	"time"
)

func TestLoadMissingIsNotAnError(t *testing.T) {
	s, ok, err := Load(t.TempDir(), "/repo")
	if err != nil || ok {
		t.Fatalf("missing = (%+v, %v, %v)", s, ok, err)
	}
}

func TestRoundTrip(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := RecordSync(home, "/repo", "satelle.dev project p", "", now); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(home, "/repo")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if !got.SyncLastSuccess.Equal(now) || got.SyncStore != "satelle.dev project p" || got.SyncReason != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestSyncMergeDoesNotClobberSnapshot(t *testing.T) {
	home := t.TempDir()
	snapAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	pushAt := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	if err := RecordSnapshot(home, "/repo", false, "snap fail", snapAt); err != nil {
		t.Fatal(err)
	}
	if err := RecordSync(home, "/repo", "s", "", pushAt); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(home, "/repo")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.SnapshotReason != "snap fail" || !got.SnapshotLastFailure.Equal(snapAt) {
		t.Fatalf("snapshot clobbered: %+v", got)
	}
	if !got.SyncLastSuccess.Equal(pushAt) {
		t.Fatalf("sync missing: %+v", got)
	}
}

func TestTornFileIsNoState(t *testing.T) {
	home := t.TempDir()
	repo := "/repo"
	if err := RecordSync(home, repo, "s", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	p := pathFor(home, repo)
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok, err := Load(home, repo)
	if err != nil {
		t.Fatalf("torn must not error: %v", err)
	}
	if ok {
		t.Fatal("torn file must be ok=false")
	}
}

func TestRepeatedFailureIncrementsPasses(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	if err := RecordSync(home, "/r", "s", "boom", now); err != nil {
		t.Fatal(err)
	}
	if err := RecordSync(home, "/r", "s", "boom", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(home, "/r")
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncPasses != 2 || got.SyncReason != "boom" {
		t.Fatalf("got %+v", got)
	}
}

func TestRecordSubstrateMergesOnlyItsGroupAndTheGivenAreas(t *testing.T) {
	home := t.TempDir()
	pushAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	first := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	second := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := RecordSync(home, "/repo", "s", "", pushAt); err != nil {
		t.Fatal(err)
	}
	if err := RecordSubstrate(home, "/repo", map[string]AreaCheck{"skills": {State: "behind"}, "documents": {State: "in step"}}, first); err != nil {
		t.Fatal(err)
	}
	if err := RecordSubstrate(home, "/repo", map[string]AreaCheck{"skills": {State: "in step"}}, second); err != nil {
		t.Fatal(err)
	}
	if err := RecordSubstrate(home, "/repo", nil, second); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(home, "/repo")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if !got.SyncLastSuccess.Equal(pushAt) {
		t.Fatalf("sync group clobbered: %+v", got)
	}
	if a := got.SubstrateAreas["skills"]; a.State != "in step" || !a.CheckedAt.Equal(second) {
		t.Fatalf("skills = %+v, want in step at %v", a, second)
	}
	if a := got.SubstrateAreas["documents"]; a.State != "in step" || !a.CheckedAt.Equal(first) {
		t.Fatalf("documents must keep its own record: %+v", a)
	}
}

// RecordSync: a failure keeps the last success, a success clears the reason,
// and a local store is never a failure (sto_5c706973).
func TestRecordSyncFailureThenSuccess(t *testing.T) {
	home := t.TempDir()
	okAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	failAt := okAt.Add(time.Minute)
	againAt := failAt.Add(time.Minute)
	store := "satelle.dev project shared at https://satelle.dev"
	if err := RecordSync(home, "/repo", store, "", okAt); err != nil {
		t.Fatal(err)
	}
	if err := RecordSync(home, "/repo", store, "rpc error: code = Unavailable", failAt); err != nil {
		t.Fatal(err)
	}
	got, _, _ := Load(home, "/repo")
	if got.SyncReason != "rpc error: code = Unavailable" || !got.SyncLastFailure.Equal(failAt) || !got.SyncLastSuccess.Equal(okAt) || !got.SyncLastAttempt.Equal(failAt) || got.SyncPasses != 1 || got.SyncStore != store {
		t.Fatalf("after a failure: %+v", got)
	}
	if err := RecordSync(home, "/repo", store, "", againAt); err != nil {
		t.Fatal(err)
	}
	got, _, _ = Load(home, "/repo")
	if got.SyncReason != "" || got.SyncPasses != 0 || !got.SyncLastSuccess.Equal(againAt) || !got.SyncLastFailure.Equal(failAt) {
		t.Fatalf("after a success: %+v", got)
	}
	if err := RecordSync(home, "/local", LocalStore, "ignored", againAt); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Load(home, "/local"); got.SyncStore != LocalStore || got.SyncReason != "" || !got.SyncLastSuccess.Equal(againAt) {
		t.Fatalf("a local store: %+v", got)
	}
}

// Keys an older satelle wrote (the removed push group) are dropped on load.
func TestOldPushKeysAreIgnored(t *testing.T) {
	home := t.TempDir()
	if err := RecordSync(home, "/repo", "local", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathFor(home, "/repo"), []byte(`{"repo_path":"/repo","push_reason":"old","scope":"personal","sync_store":"local"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(home, "/repo")
	if err != nil || !ok || got.SyncStore != "local" || got.SyncReason != "" {
		t.Fatalf("load = %+v %v %v", got, ok, err)
	}
}
