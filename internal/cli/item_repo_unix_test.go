//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func TestLegacyItemSyncsWithoutRepoEvent(t *testing.T) {
	root := t.TempDir()
	a := newMachine(t)
	storeOn(t, a, root)
	id := createID(t, "story", "create", "--title", "legacy")
	must(t, "ledger", "push")
	onA := chainBytes(t, id)

	b := newMachine(t)
	storeOn(t, b, root)
	must(t, "ledger", "pull")
	if m := itemJSON(t, "story", id); m["repo"] != "self" {
		t.Fatalf("pulled repo %v, want self", m["repo"])
	}
	onB := chainBytes(t, id)
	if !bytes.Equal(onA, onB) || bytes.Contains(onB, []byte(`"repo"`)) {
		t.Fatalf("the chains differ or hold a repo:\nA %s\nB %s", onA, onB)
	}
}

func TestPulledItemWithUnknownRepo(t *testing.T) {
	root := t.TempDir()
	a := newMachine(t)
	storeOn(t, a, root)
	writeRepos(t, a.dir, "web")
	id := createID(t, "story", "create", "--title", "p", "--repo", "web")
	must(t, "ledger", "push")

	b := newMachine(t)
	storeOn(t, b, root)
	must(t, "ledger", "pull")
	if out := must(t, "story", "list"); !strings.Contains(out, id) || !strings.Contains(out, " web ") {
		t.Fatalf("story list:\n%s", out)
	}
	if out := must(t, "story", "get", id); !strings.Contains(out, "web (not registered in .satelle/repos.toml; registered: self)") {
		t.Fatalf("story get:\n%s", out)
	}
	repos, _, _, _ := doctorRepos(t)
	found := false
	for _, is := range repos {
		found = found || (strings.Contains(is.Problem, id) && strings.Contains(is.Problem, "web") && strings.Contains(is.Fix, "satelle repo add web --origin"))
	}
	if !found {
		t.Fatalf("doctor names no unregistered target: %+v", repos)
	}
	before := chainBytes(t, id)
	out, err := run(t, "", "story", "seat", id, "--session", "s-1")
	if err == nil || !strings.Contains(out+err.Error(), "does not register") || !strings.Contains(out+err.Error(), "web") {
		t.Fatalf("seat: %v\n%s", err, out)
	}
	if !bytes.Equal(before, chainBytes(t, id)) {
		t.Fatal("a refused seat changed the chain")
	}
	writeRepos(t, b.dir, "web")
	must(t, "story", "seat", id, "--session", "s-1")
	repos, _, _, _ = doctorRepos(t)
	for _, is := range repos {
		if strings.Contains(is.Problem, id) {
			t.Fatalf("doctor still names %s: %+v", id, is)
		}
	}
}

func TestRemoteFireRefusesUnregisteredTarget(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)

	id := createID(t, "story", "create", "--title", "ghosted")
	plantRepo(t, id, "ghost")
	out, err := run(t, "", "remote", "fire", "ci", id)
	if err == nil || !strings.Contains(out+err.Error(), "does not register") || !strings.Contains(out+err.Error(), "ghost") {
		t.Fatalf("fire: %v\n%s", err, out)
	}
	if fireCalls(srv) != 0 || len(srv.Events("acme", id)) != 0 {
		t.Fatalf("fired %d, pushed %d events", fireCalls(srv), len(srv.Events("acme", id)))
	}

	task := createID(t, "task", "create", "--title", "release", "--input-tag", "t")
	input := createID(t, "story", "create", "--title", "input", "--tag", "t")
	plantRepo(t, input, "ghost")
	var it map[string]any
	if err := json.Unmarshal([]byte(must(t, "task", "get", task, "--json")), &it); err != nil || it["inputs"] == nil {
		t.Fatalf("task %v %v", it, err)
	}
	out, err = run(t, "", "remote", "fire", "ci", task)
	if err == nil || !strings.Contains(out+err.Error(), "ghost") {
		t.Fatalf("fire task: %v\n%s", err, out)
	}
	if fireCalls(srv) != 0 || len(srv.Events("acme", task)) != 0 || len(srv.Events("acme", input)) != 0 {
		t.Fatal("a refused task fire reached the server")
	}
}
