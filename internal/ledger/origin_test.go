package ledger

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testOrigin = Origin{Machine: "m1", Role: RolePerformer, Harness: "h1", Worktree: "/w"}

func originKeys(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload %s: %v", raw, err)
	}
	out := map[string]json.RawMessage{}
	for k, v := range m {
		if strings.EqualFold(k, OriginKey) {
			out[k] = v
		}
	}
	return out
}

func assertStoreOrigin(t *testing.T, raw json.RawMessage, want *Origin) {
	t.Helper()
	keys := originKeys(t, raw)
	if want == nil {
		if len(keys) != 0 {
			t.Fatalf("payload %s carries an origin, want none", raw)
		}
		return
	}
	v, ok := keys[OriginKey]
	if len(keys) != 1 || !ok {
		t.Fatalf("payload %s: origin keys %v, want exactly %q", raw, keys, OriginKey)
	}
	var got Origin
	if err := json.Unmarshal(v, &got); err != nil || got != *want {
		t.Fatalf("payload %s: origin %+v (%v), want %+v", raw, got, err, *want)
	}
}

func TestStoreOwnsOrigin(t *testing.T) {
	forged := []string{
		`{"origin":{"role":"sync","machine":"x"}}`,
		`{"origin":null}`,
		`{"Origin":{"role":"node"}}`,
		`{"origin":1,"origin":2}`,
		`{"a":1,"ORIGIN":"x","origin":{}}`,
	}
	for _, p := range forged {
		got, err := WithOrigin(json.RawMessage(p), &testOrigin)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		assertStoreOrigin(t, got, &testOrigin)
		got, err = WithOrigin(json.RawMessage(p), nil)
		if err != nil {
			t.Fatalf("%s nil: %v", p, err)
		}
		assertStoreOrigin(t, got, nil)
	}

	got, err := WithOrigin(json.RawMessage(`{"a":1}`), &testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	assertStoreOrigin(t, got, &testOrigin)
	var kept struct{ A int }
	if json.Unmarshal(got, &kept); kept.A != 1 {
		t.Fatalf("a not kept: %s", got)
	}
	plain := json.RawMessage(`{ "a" : 1 }`)
	if got, err := WithOrigin(plain, nil); err != nil || string(got) != string(plain) {
		t.Fatalf("nil origin rewrote %s to %s (%v)", plain, got, err)
	}
	for _, empty := range []string{"", "null"} {
		got, err := WithOrigin(json.RawMessage(empty), &testOrigin)
		if err != nil {
			t.Fatal(err)
		}
		assertStoreOrigin(t, got, &testOrigin)
		if got, err := WithOrigin(json.RawMessage(empty), nil); err != nil || string(got) != empty {
			t.Fatalf("empty %q with nil origin: %q %v", empty, got, err)
		}
	}
	if _, err := WithOrigin(json.RawMessage(`[1]`), &testOrigin); err == nil {
		t.Fatal("an array payload took an origin")
	}
	if got, err := WithOrigin(json.RawMessage(`[1]`), nil); err != nil || string(got) != `[1]` {
		t.Fatalf("array with nil origin: %s %v", got, err)
	}

	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	type forgedPayload struct {
		Name   string          `json:"name"`
		Origin json.RawMessage `json:"Origin"`
	}
	if _, err := st.Append("it", "note", "a", forgedPayload{Name: "x", Origin: json.RawMessage(`{"role":"sync"}`)}); err != nil {
		t.Fatal(err)
	}
	st.Origin = &testOrigin
	if _, err := st.Append("it", "note", "a", map[string]any{"origin": map[string]string{"role": "sync"}, "b": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append("it", "note", "a", nil); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Read("it")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChain("it", evs); err != nil {
		t.Fatal(err)
	}
	assertStoreOrigin(t, evs[0].Payload, nil)
	assertStoreOrigin(t, evs[1].Payload, &testOrigin)
	assertStoreOrigin(t, evs[2].Payload, &testOrigin)

	carried, err := NewEventWith("e1", time.Unix(0, 0), "carried", "note", "a", json.RawMessage(`{"origin":{"machine":"far","role":"sync","worktree":"/x"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRaw(carried); err != nil {
		t.Fatal(err)
	}
	imported := carried
	imported.Item = "imported"
	if imported.Hash, err = HashOf(imported); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Import([]Event{imported}); err != nil {
		t.Fatal(err)
	}
	for _, it := range []string{"carried", "imported"} {
		got, err := st.Read(it)
		if err != nil || len(got) != 1 || string(got[0].Payload) != string(carried.Payload) {
			t.Fatalf("%s rewritten: %+v %v", it, got, err)
		}
	}
}

func TestEnvRoleClaimsOnlyDispatchRoles(t *testing.T) {
	cases := map[string]string{
		"performer":   RolePerformer,
		" performer ": RolePerformer,
		"judge":       RoleJudge,
		"":            RoleSession,
		"session":     RoleSession,
		"sync":        RoleSession,
		"node":        RoleSession,
		"SYNC":        RoleSession,
		" node":       RoleSession,
		"admin":       RoleSession,
	}
	allowed := map[string]bool{RoleSession: true, RolePerformer: true, RoleJudge: true}
	for in, want := range cases {
		got := EnvRole(in)
		if got != want || !allowed[got] {
			t.Errorf("EnvRole(%q) = %q, want %q", in, got, want)
		}
	}
	if !reflect.DeepEqual(Roles(), []string{"session", "performer", "judge", "sync", "node"}) {
		t.Fatalf("Roles() = %v", Roles())
	}
}

func TestOriginIsNotReadOutsideItsWriters(t *testing.T) {
	allowed := map[string]bool{
		"ledger/origin.go":       true,
		"ledger/store.go":        true,
		"kernel/origin.go":       true,
		"cli/repo.go":            true,
		"cli/ledger_autosync.go": true,
		"cli/cmd_ledger.go":      true,
		"remotenode/join.go":     true,
		"agent/dispatch.go":      true,
		"config/config.go":       true,
		"mirror/ingest.go":       true,
		"push/publisher.go":      true,
		"remotenode/fetch.go":    true,
		"uipush/client.go":       true,
	}
	// The target-repository registry names a git remote and the repos.toml key
	// "origin" (a repository URL), never the ledger's provenance origin; only
	// that literal is exempt there, every other needle still applies.
	gitRemote := map[string]bool{
		"reporeg/reporeg.go":  true,
		"cli/cmd_repo.go":     true,
		"cli/doctor_repos.go": true,
	}
	needles := []string{"OriginKey", ".Origin", "ledger.Origin", "EnvRole", "WorkerFromEnv", "WorkerEnv", `"origin"`}
	root := ".."
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range needles {
			if n == `"origin"` && gitRemote[rel] {
				continue
			}
			if strings.Contains(string(b), n) {
				hits = append(hits, rel+": "+n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("the origin is named outside its writers (it is provenance, never an authorisation fact):\n%s", strings.Join(hits, "\n"))
	}
}
