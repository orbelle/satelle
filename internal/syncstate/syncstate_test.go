package syncstate

import (
	"os"
	"path/filepath"
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
	if err := RecordPass(home, "/repo", Pass{Store: "satelle.dev project p"}, now); err != nil {
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
	if err := RecordPass(home, "/repo", Pass{Store: "s"}, pushAt); err != nil {
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
	if err := RecordPass(home, repo, Pass{Store: "s"}, time.Now()); err != nil {
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
	if err := RecordPass(home, "/r", Pass{Store: "s", Reason: "boom"}, now); err != nil {
		t.Fatal(err)
	}
	if err := RecordPass(home, "/r", Pass{Store: "s", Reason: "boom"}, now.Add(time.Minute)); err != nil {
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

func TestRecordPassFailureThenSuccess(t *testing.T) {
	home := t.TempDir()
	okAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	failAt := okAt.Add(time.Minute)
	againAt := failAt.Add(time.Minute)
	store := "satelle.dev project shared at https://satelle.dev"
	if err := RecordPass(home, "/repo", Pass{Store: store}, okAt); err != nil {
		t.Fatal(err)
	}
	if err := RecordPass(home, "/repo", Pass{Store: store, Reason: "rpc error: code = Unavailable"}, failAt); err != nil {
		t.Fatal(err)
	}
	got, _, _ := Load(home, "/repo")
	if got.SyncReason != "rpc error: code = Unavailable" || !got.SyncLastFailure.Equal(failAt) || !got.SyncLastSuccess.Equal(okAt) || !got.SyncLastAttempt.Equal(failAt) || got.SyncPasses != 1 || got.SyncStore != store {
		t.Fatalf("after a failure: %+v", got)
	}
	if err := RecordPass(home, "/repo", Pass{Store: store}, againAt); err != nil {
		t.Fatal(err)
	}
	got, _, _ = Load(home, "/repo")
	if got.SyncReason != "" || got.SyncPasses != 0 || !got.SyncLastSuccess.Equal(againAt) || !got.SyncLastFailure.Equal(failAt) {
		t.Fatalf("after a success: %+v", got)
	}
	if err := RecordPass(home, "/local", Pass{Store: LocalStore, Reason: "ignored"}, againAt); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Load(home, "/local"); got.SyncStore != LocalStore || got.SyncReason != "" || !got.SyncLastSuccess.Equal(againAt) {
		t.Fatalf("a local store: %+v", got)
	}
}

func TestOldPushKeysAreIgnored(t *testing.T) {
	home := t.TempDir()
	if err := RecordPass(home, "/repo", Pass{Store: "local"}, time.Now()); err != nil {
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

func TestRecordPassCounts(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	if err := RecordPass(home, "/repo", Pass{Store: "s", Reason: "1 item(s) not synced: sto_x (diverged)", Pushed: 2, Pulled: 3, Forked: []string{"sto_x (diverged)"}}, now); err != nil {
		t.Fatal(err)
	}
	st, ok, err := Load(home, "/repo")
	if err != nil || !ok || st.SyncPushed != 2 || st.SyncPulled != 3 || len(st.SyncForked) != 1 || st.SyncForked[0] != "sto_x (diverged)" || st.SyncReason == "" {
		t.Fatalf("recorded %+v %v %v", st, ok, err)
	}
	if err := RecordPass(home, "/repo", Pass{Store: "s", Pushed: 1}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _, _ = Load(home, "/repo")
	if st.SyncPushed != 1 || st.SyncPulled != 0 || len(st.SyncForked) != 0 || st.SyncReason != "" {
		t.Fatalf("a later pass did not replace the counts: %+v", st)
	}
}

func TestRecordPassUpgrade(t *testing.T) {
	home := t.TempDir()
	ok1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	fail := ok1.Add(time.Hour)
	up := fail.Add(time.Hour)
	if err := RecordPass(home, "/repo", Pass{Store: "s", Pushed: 1}, ok1); err != nil {
		t.Fatal(err)
	}
	if err := RecordPass(home, "/repo", Pass{Store: "s", Reason: "unknown key review", Forked: []string{"x (y)"}}, fail); err != nil {
		t.Fatal(err)
	}
	if err := RecordPass(home, "/repo", Pass{Upgrade: "satelle upgrade --archive"}, up); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(home, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncUpgrade != "satelle upgrade --archive" || got.SyncReason != "" || got.SyncPasses != 0 ||
		got.SyncStore != "" || got.SyncPushed != 0 || len(got.SyncForked) != 0 {
		t.Fatalf("upgrade pass recorded %+v", got)
	}
	if !got.SyncLastAttempt.Equal(up) || !got.SyncLastSuccess.Equal(ok1) || !got.SyncLastFailure.Equal(fail) {
		t.Fatalf("upgrade pass moved the stamps: %+v", got)
	}
	next := up.Add(time.Minute)
	if err := RecordPass(home, "/repo", Pass{Store: LocalStore}, next); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Load(home, "/repo"); got.SyncUpgrade != "" || !got.SyncLastSuccess.Equal(next) {
		t.Fatalf("an ordinary pass kept the upgrade: %+v", got)
	}
}

func TestLoadReadsAFileWithTheDroppedSubstrateKeys(t *testing.T) {
	home := t.TempDir()
	repo := "/repo"
	p := pathFor(home, repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"repo_path":"/repo","sync_store":"s","sync_last_success":"2026-08-01T12:00:00Z","sync_pushed":2,"snapshot_reason":"snap fail","substrate_areas":{"skills":{"state":"ok","paths":["a"],"checked_at":"2026-08-01T12:00:00Z"}},"substrate_checked_attempt":"2026-08-01T12:00:00Z"}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(home, repo)
	if err != nil || !ok {
		t.Fatalf("load = ok %v, err %v; want the state", ok, err)
	}
	want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if got.RepoPath != "/repo" || got.SyncStore != "s" || !got.SyncLastSuccess.Equal(want) || got.SyncPushed != 2 || got.SnapshotReason != "snap fail" {
		t.Fatalf("got %+v", got)
	}
}
