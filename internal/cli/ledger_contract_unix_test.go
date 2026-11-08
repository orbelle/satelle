//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// agentRunMachine is a container runtime as id4me's agent-run makes one: not
// signed in, SATELLE_HOME a custom path, and [ledger] store the store command
// satelle-store-azure with no args, found on PATH and configured only by
// LEDGER_ACCOUNT and LEDGER_CONTAINER in its environment.
func agentRunMachine(t *testing.T, bin string) agentRun {
	t.Helper()
	dir := isolate(t)
	m := agentRun{home: os.Getenv("HOME"), satelleHome: filepath.Join(t.TempDir(), "custom", "satelle-home"), dir: dir}
	t.Setenv("SATELLE_HOME", m.satelleHome)
	if !strings.Contains(os.Getenv("PATH"), bin) {
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	t.Setenv("LEDGER_ACCOUNT", "acct")
	t.Setenv("LEDGER_CONTAINER", "ctr")
	must(t, "init")
	configureLedger(t, dir, `store = { command = "satelle-store-azure" }`)
	return m
}

// agentRun is one container runtime.
type agentRun struct{ home, satelleHome, dir string }

// use makes m the current runtime.
func (m agentRun) use(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", m.home)
	t.Setenv("SATELLE_HOME", m.satelleHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(m.home, "xdg"))
	t.Chdir(m.dir)
}

// eventKeys is the sorted key set of each event `ledger list <id> --json`
// prints.
func eventKeys(t *testing.T, out string) [][]string {
	t.Helper()
	var events []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &events); err != nil {
		t.Fatalf("ledger list --json is not an array of objects: %v\n%s", err, out)
	}
	var keys [][]string
	for _, e := range events {
		var k []string
		for name := range e {
			k = append(k, name)
		}
		sort.Strings(k)
		keys = append(keys, k)
	}
	return keys
}

// The downstream contract of id4me's agent-run (flow/agent_run.py): ledger
// pull, push and verify run with no flags and keep their exit codes; the
// store command with no args gets LEDGER_ACCOUNT and LEDGER_CONTAINER from
// the environment; ledger list <id> --json keeps its shape; all of it works
// not signed in with a custom SATELLE_HOME.
func TestAgentRunContract(t *testing.T) {
	bin, base := t.TempDir(), t.TempDir()
	envLog := filepath.Join(t.TempDir(), "env.log")
	adapter := "#!/bin/sh\nprintf '%s %s %s\\n' \"$LEDGER_ACCOUNT\" \"$LEDGER_CONTAINER\" \"$1\" >> '" + envLog + "'\n" +
		"if [ -z \"$LEDGER_ACCOUNT\" ] || [ -z \"$LEDGER_CONTAINER\" ]; then echo 'error: auth: LEDGER_ACCOUNT and LEDGER_CONTAINER are required' >&2; exit 2; fi\n" +
		"root='" + base + "'/\"$LEDGER_ACCOUNT\"/\"$LEDGER_CONTAINER\"; mkdir -p \"$root\"\n" +
		"exec sh '" + storeAdapter(t) + "' --root \"$root\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "satelle-store-azure"), []byte(adapter), 0o755); err != nil {
		t.Fatal(err)
	}

	// The first run: create, attach, push.
	agentRunMachine(t, bin)
	id := createID(t, "story", "create", "--title", "agent run", "--criteria", "c")
	if err := os.WriteFile("evidence.md", []byte("# evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--file", "evidence.md")
	listA := must(t, "ledger", "list", id, "--json")
	if out, err := run(t, "", "ledger", "push"); err != nil || !strings.Contains(out, "ledger store: command satelle-store-azure") {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(base, "acct", "ctr", "objects")); err != nil {
		t.Fatalf("the adapter did not write under its account and container: %v", err)
	}
	logged, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if !strings.HasPrefix(line, "acct ctr ") {
			t.Fatalf("the adapter ran without LEDGER_ACCOUNT and LEDGER_CONTAINER: %q", line)
		}
	}
	if !strings.Contains(string(logged), "acct ctr put-if-absent") {
		t.Fatalf("the adapter was never given an operation as its first argument:\n%s", logged)
	}

	// The next run: a fresh runtime pulls, verifies, lists.
	agentRunMachine(t, bin)
	if out, err := run(t, "", "ledger", "pull"); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if out, err := run(t, "", "ledger", "verify"); err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	listB := must(t, "ledger", "list", id, "--json")
	if listA != listB {
		t.Errorf("ledger list --json differs after the round trip\nfirst: %s\nnext:  %s", listA, listB)
	}
	want := []string{"actor", "at", "hash", "id", "item", "kind", "payload", "prev"}
	keys := eventKeys(t, listB)
	if len(keys) < 2 {
		t.Fatalf("ledger list --json has %d events", len(keys))
	}
	for i, k := range keys {
		if !slices.Equal(k, want) {
			t.Errorf("event %d keys %v, want %v", i, k, want)
		}
	}

	// verify exits 3 while a referenced blob is missing ...
	l := runtimeLedger(t)
	atts := attachments(t, id)
	if err := os.Remove(filepath.Join(l.Dir(), "blobs", atts[0].SHA256)); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "ledger", "verify"); ExitCodeOf(err) != 3 {
		t.Fatalf("verify with a missing blob: exit %d (%v), want 3", ExitCodeOf(err), err)
	}
	// ... the next pull fills it ...
	if out, err := run(t, "", "ledger", "pull"); err != nil || !strings.Contains(out, "filled: "+id) {
		t.Fatalf("pull to fill the gap: %v\n%s", err, out)
	}
	if _, err := run(t, "", "ledger", "verify"); err != nil {
		t.Fatalf("verify after the fill: %v", err)
	}
	// ... and exits 1 for a broken chain.
	path := l.Path(id)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	forged := bytes.Replace(b, []byte("agent run"), []byte("forged run"), 1)
	if bytes.Equal(forged, b) {
		t.Fatal("the fixture title is not on the chain")
	}
	if err := os.WriteFile(path, forged, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "ledger", "verify"); ExitCodeOf(err) != 1 {
		t.Fatalf("verify with a broken chain: exit %d (%v), want 1", ExitCodeOf(err), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	// push and pull exit 1 on a divergence, naming the item.
	x := agentRunMachine(t, bin)
	must(t, "ledger", "pull")
	y := agentRunMachine(t, bin)
	must(t, "ledger", "pull")
	x.use(t)
	must(t, "story", "set", id, "--title", "edited on x")
	must(t, "ledger", "push")
	y.use(t)
	must(t, "story", "set", id, "--title", "edited on y")
	for _, verb := range []string{"push", "pull"} {
		out, err := run(t, "", "ledger", verb)
		if ExitCodeOf(err) != 1 || !strings.Contains(out, "diverged: "+id+" at event ") {
			t.Errorf("%s over a diverged chain: exit %d (%v)\n%s", verb, ExitCodeOf(err), err, out)
		}
	}
	// With no flags and nothing to do, push and pull exit 0.
	x.use(t)
	for _, verb := range []string{"push", "pull"} {
		if _, err := run(t, "", "ledger", verb); err != nil {
			t.Errorf("%s in sync: %v", verb, err)
		}
	}
}
