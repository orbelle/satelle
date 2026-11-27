package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func eventOrigin(t *testing.T, e ledger.Event) (map[string]json.RawMessage, ledger.Origin) {
	t.Helper()
	var p map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("%s payload %s: %v", e.Kind, e.Payload, err)
	}
	raw, ok := p[ledger.OriginKey]
	if !ok {
		t.Fatalf("%s carries no origin: %s", e.Kind, e.Payload)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["host"]; ok {
		t.Fatalf("%s origin names a host: %s", e.Kind, raw)
	}
	var o ledger.Origin
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	return keys, o
}

func readChain(t *testing.T, id string) (repo, []ledger.Event) {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ledger.OpenExisting(r.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return r, evs
}

func TestCLIEventsCarryOrigin(t *testing.T) {
	cases := []struct{ worker, role string }{
		{"", ledger.RoleSession},
		{"performer", ledger.RolePerformer},
		{"judge", ledger.RoleJudge},
		{"sync", ledger.RoleSession},
	}
	for _, c := range cases {
		t.Run(c.role+"/"+c.worker, func(t *testing.T) {
			isolate(t)
			t.Setenv(config.WorkerEnv, c.worker)
			must(t, "init")
			id := createID(t, "story", "create", "--title", "traced", "--criteria", "c")
			must(t, "story", "set", id, "--title", "traced again")
			r, evs := readChain(t, id)
			machine, ok := config.ReadMachineID(config.GlobalDir())
			if !ok {
				t.Fatal("no machine id")
			}
			seen := 0
			for _, e := range evs {
				if e.Kind != item.KindCreated && e.Kind != item.KindUpdated {
					continue
				}
				seen++
				keys, o := eventOrigin(t, e)
				if o.Machine != machine || o.Role != c.role || o.Worktree != canonical(r.Tree) {
					t.Fatalf("%s origin %+v, want machine %s role %s worktree %s", e.Kind, o, machine, c.role, canonical(r.Tree))
				}
				if _, ok := keys["harness"]; ok {
					t.Fatalf("%s names a harness with every caller marker cleared: %+v", e.Kind, o)
				}
			}
			if seen < 2 {
				t.Fatalf("%d created/updated events", seen)
			}
		})
	}
}

func TestCLIEventsCarryDetectedHarness(t *testing.T) {
	isolate(t)
	names, _ := hook.CallerMarkers()
	if len(names) == 0 {
		t.Skip("no caller markers")
	}
	t.Setenv(names[0], "1")
	harness := hook.Caller()
	if harness == "" {
		t.Fatalf("marker %s detects no harness", names[0])
	}
	must(t, "init")
	id := createID(t, "story", "create", "--title", "traced", "--criteria", "c")
	_, evs := readChain(t, id)
	if _, o := eventOrigin(t, evs[0]); o.Harness != harness {
		t.Fatalf("origin harness %q, want %q", o.Harness, harness)
	}
}

func TestSyncServiceEventsCarrySyncOrigin(t *testing.T) {
	isolate(t)
	t.Setenv(config.WorkerEnv, "performer")
	must(t, "init")
	id := createID(t, "story", "create", "--title", "synced", "--criteria", "c")
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	var appended *ledger.Event
	fix := func(k *kernel.Kernel, _ string, _ func(string) bool) []string {
		if k == nil || appended != nil {
			return nil
		}
		e, err := k.Ledger.Append(id, "note", "satelle", map[string]string{"why": "sync"})
		if err != nil {
			t.Fatal(err)
		}
		appended = &e
		return nil
	}
	syncRepo(context.Background(), r, fix, nil)
	if appended == nil {
		t.Fatal("the sync pass opened no kernel")
	}
	machine, _ := config.ReadMachineID(config.GlobalDir())
	_, evs := readChain(t, id)
	last := evs[len(evs)-1]
	if last.ID != appended.ID {
		t.Fatalf("tail %s, want %s", last.ID, appended.ID)
	}
	if _, o := eventOrigin(t, last); o.Role != ledger.RoleSync || o.Machine != machine || o.Worktree != canonical(r.Tree) {
		t.Fatalf("sync origin %+v", o)
	}
}
