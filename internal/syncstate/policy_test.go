package syncstate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPolicyCacheRoundTripAndFailureKeepsLastGood(t *testing.T) {
	home := t.TempDir()
	t1 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	good := PolicySub{Source: "w:p", SourceKind: "catalog", SHA256: "abc", RulesTOML: "rules"}
	if err := RecordPolicy(home, "/repo", "a", good, t1); err != nil {
		t.Fatal(err)
	}
	if err := RecordPolicyFailure(home, "/repo", "a", "w:p", "catalog", "server down", t2); err != nil {
		t.Fatal(err)
	}
	st, ok, err := LoadPolicy(home, "/repo")
	if err != nil || !ok {
		t.Fatalf("load = %v, %v", ok, err)
	}
	got := st.Subs["a"]
	if got.RulesTOML != "rules" || !got.FetchedAt.Equal(t1) || got.SHA256 != "abc" {
		t.Fatalf("a failure touched the last good list: %+v", got)
	}
	if got.LastError != "server down" || !got.LastErrorAt.Equal(t2) {
		t.Fatalf("failure not recorded: %+v", got)
	}
	// The next success clears the failure.
	if err := RecordPolicy(home, "/repo", "a", good, t2); err != nil {
		t.Fatal(err)
	}
	if st, _, _ = LoadPolicy(home, "/repo"); st.Subs["a"].LastError != "" {
		t.Fatalf("success left the failure: %+v", st.Subs["a"])
	}
}

func TestPolicyFailureOnRepointedSourceKeepsNoOldRules(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	_ = RecordPolicy(home, "/repo", "a", PolicySub{Source: "old", SourceKind: "url", RulesTOML: "old rules"}, now)
	_ = RecordPolicyFailure(home, "/repo", "a", "new", "url", "down", now)
	st, _, _ := LoadPolicy(home, "/repo")
	if st.Subs["a"].RulesTOML != "" || st.Subs["a"].Source != "new" {
		t.Fatalf("a re-pointed subscription inherited the old list: %+v", st.Subs["a"])
	}
}

func TestMarkPolicyAttemptLeavesListsAlone(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	_ = RecordPolicy(home, "/repo", "a", PolicySub{Source: "s", SourceKind: "url", RulesTOML: "r"}, now)
	if err := MarkPolicyAttempt(home, "/repo", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _, _ := LoadPolicy(home, "/repo")
	if st.Subs["a"].RulesTOML != "r" || !st.CheckedAttempt.Equal(now.Add(time.Minute)) {
		t.Fatalf("state = %+v", st)
	}
}

func TestPolicyCacheIsASiblingOfWorkstateSync(t *testing.T) {
	home := t.TempDir()
	_ = RecordPass(home, "/repo", Pass{Store: "s"}, time.Now())
	_ = MarkPolicyAttempt(home, "/repo", time.Now())
	if filepath.Dir(PolicyPath(home, "/repo")) != filepath.Dir(pathFor(home, "/repo")) {
		t.Fatal("the cache must sit beside workstate-sync.json")
	}
	if _, err := os.Stat(pathFor(home, "/repo")); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := Load(home, "/repo"); s.SyncLastSuccess.IsZero() {
		t.Fatal("policy writes clobbered workstate-sync.json")
	}
}

func TestPolicyTornFileIsNoState(t *testing.T) {
	home := t.TempDir()
	p := PolicyPath(home, "/repo")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("{not json"), 0o644)
	st, ok, err := LoadPolicy(home, "/repo")
	if err != nil || ok || len(st.Subs) != 0 {
		t.Fatalf("torn file = %+v, %v, %v", st, ok, err)
	}
}
