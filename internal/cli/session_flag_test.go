package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// lastActor is who the newest event on id's chain was recorded as.
func lastActor(t *testing.T, id string) string {
	t.Helper()
	evs := chainKinds(t, id)
	if len(evs) == 0 {
		t.Fatalf("%s has no events", id)
	}
	return evs[len(evs)-1].Actor
}

// sto_2a41b9e4 AC1: --session names the acting session as SATELLE_SESSION
// does, before or after the command; with neither, the OS user, as before.
func TestSetRecordsTheSessionFlagAsActor(t *testing.T) {
	isolate(t)
	t.Setenv("USER", "os-user")
	must(t, "init")
	id := createID(t, "story", "create", "--title", "one")

	for _, c := range []struct {
		name, env string
		args      []string
		want      string
	}{
		{"flag after", "", []string{"story", "set", id, "--title", "a", "--session", "s-flag"}, "s-flag"},
		{"flag before", "", []string{"--session", "s-flag", "story", "set", id, "--title", "b"}, "s-flag"},
		{"env only", "s-env", []string{"story", "set", id, "--title", "c"}, "s-env"},
		{"flag wins", "s-env", []string{"story", "set", id, "--title", "d", "--session", "s-flag"}, "s-flag"},
		{"neither", "", []string{"story", "set", id, "--title", "e"}, "os-user"},
		{"empty flag keeps env", "s-env", []string{"story", "set", id, "--title", "f", "--session", ""}, "s-env"},
	} {
		t.Setenv("SATELLE_SESSION", c.env)
		must(t, c.args...)
		if got := lastActor(t, id); got != c.want {
			t.Errorf("%s: actor %q, want %q", c.name, got, c.want)
		}
		if got := os.Getenv("SATELLE_SESSION"); got != c.env {
			t.Errorf("%s: the call left SATELLE_SESSION %q, want %q", c.name, got, c.env)
		}
	}

	t.Setenv("SATELLE_SESSION", "")
	route := must(t, "story", "route", id, "--json")
	var r struct {
		Transitions []struct {
			From []string `json:"from"`
			To   string   `json:"to"`
		} `json:"transitions"`
	}
	next := ""
	if json.Unmarshal([]byte(route), &r) == nil {
		for _, tr := range r.Transitions {
			if tr.To != "cancelled" && tr.To != "blocked" && tr.To != "held" {
				next = tr.To
				break
			}
		}
	}
	if next == "" {
		next = "plan"
	}
	must(t, "story", "set", id, "--status", next, "--session", "s-move")
	if got := lastActor(t, id); got != "s-move" {
		t.Errorf("--status with --session: actor %q, want s-move", got)
	}
	if _, err := run(t, "", "story", "set", id, "--reselect", "delivery", "--session", "s-flag"); err != nil && strings.Contains(err.Error(), "--reselect takes no other field flag") {
		t.Errorf("--reselect refused the root flag: %v", err)
	}
}

// sessionCommands are the commands that act as a session; each takes
// --session (sto_2a41b9e4 AC2).
var sessionCommands = func() [][]string {
	var out [][]string
	for _, kind := range []string{"story", "task", "execution", "queue"} {
		for _, v := range []string{"set", "attach", "record", "seat", "release", "perform", "collect"} {
			out = append(out, []string{kind, v})
		}
	}
	for _, kind := range []string{"story", "task", "queue"} {
		out = append(out, []string{kind, "create"})
	}
	return append(out,
		[]string{"task", "run"}, []string{"queue", "add"}, []string{"queue", "remove"}, []string{"queue", "fire"},
		[]string{"ledger", "append"}, []string{"remote", "fire"}, []string{"upgrade"}, []string{"doctor"}, []string{"status"},
		[]string{"hook", "gate"}, []string{"hook", "context"}, []string{"hook", "prompt"}, []string{"hook", "stopcheck"},
	)
}()

// sto_2a41b9e4 AC2: every command takes --session and -C as root flags; none
// shadows them or skips the root's pre-run.
func TestEverySessionCommandTakesSession(t *testing.T) {
	root := NewRootCmd()
	for _, path := range sessionCommands {
		c, rest, err := root.Find(path)
		if err != nil || len(rest) != 0 || c.Name() != path[len(path)-1] {
			t.Errorf("%v: not a command (%v, rest %v)", path, err, rest)
			continue
		}
		if c.Flag(sessionFlag) == nil {
			t.Errorf("%v takes no --session", path)
		}
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != root {
			if c.LocalNonPersistentFlags().Lookup(sessionFlag) != nil || c.LocalNonPersistentFlags().Lookup(directoryFlag) != nil ||
				c.PersistentFlags().Lookup(sessionFlag) != nil || c.PersistentFlags().Lookup(directoryFlag) != nil {
				t.Errorf("%s defines its own --session or --directory", c.CommandPath())
			}
			if c.PersistentPreRun != nil || c.PersistentPreRunE != nil {
				t.Errorf("%s has its own persistent pre-run, which skips the root's", c.CommandPath())
			}
			if c.DisableFlagParsing {
				t.Errorf("%s disables flag parsing", c.CommandPath())
			}
			if c.Runnable() && (c.Flag(sessionFlag) == nil || c.Flag(directoryFlag) == nil) {
				t.Errorf("%s does not inherit --session and -C", c.CommandPath())
			}
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)

	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "t", "--session", "s")
	if got := lastActor(t, id); got != "s" {
		t.Errorf("create: actor %q, want s", got)
	}
	if err := os.WriteFile("note.md", []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--name", "n", "--file", "note.md", "--session", "s")
	if got := lastActor(t, id); got != "s" {
		t.Errorf("attach: actor %q, want s", got)
	}
	must(t, "story", "seat", id, "--session", "s")
	if out := must(t, "status", "--session", "s"); !strings.Contains(out, id) {
		t.Errorf("status --session s does not list the seat:\n%s", out)
	}
	if _, err := run(t, "", "story", "seat", id); err == nil || !strings.Contains(err.Error(), "no session: pass --session <id>") {
		t.Errorf("seat with no session: %v", err)
	}
	must(t, "story", "release", id, "--session", "s")
	must(t, "story", "get", id, "--session", "s")
}

// sto_2a41b9e4 AC2: building trees inside a running call (as doctor and
// validate do) never resets the call's session.
func TestBuildingATreeKeepsTheCallSession(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "t")
	must(t, "validate", "--session", "s-keep")
	for range 10 {
		NewRootCmd()
	}
	must(t, "story", "set", id, "--title", "kept", "--session", "s-keep")
	if got := lastActor(t, id); got != "s-keep" {
		t.Errorf("actor %q after tree builds, want s-keep", got)
	}
	// Inside one call: the pre-run sets the session, then trees are built.
	root := NewRootCmd()
	root.SetArgs([]string{"version", "--session", "s-in"})
	root.SetOut(&strings.Builder{})
	t.Setenv("SATELLE_SESSION", "")
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		substrateTree(NewRootCmd())
	}
	if got := sessionFromEnv(); got != "s-in" {
		t.Errorf("session after building trees in the call: %q, want s-in", got)
	}
}

// sto_2a41b9e4 AC3: the context line names only --session.
func TestHookContextNamesOnlyTheSessionFlag(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	payload := fmt.Sprintf(`{"session_id":"s-line","cwd":%q}`, dir)
	for _, ev := range []string{"context", "prompt"} {
		raw, err := run(t, payload, "hook", ev, "--harness", "claude")
		if err != nil {
			t.Fatalf("hook %s: %v", ev, err)
		}
		out := hookText(raw)
		for _, want := range []string{"satelle session: s-line", "Pass --session s-line on every satelle command",
			"not joined to cd, export, ; or &&", "satelle -C <dir>", "`satelle help edit-gate --session <session>`"} {
			if !strings.Contains(out, want) {
				t.Errorf("hook %s lacks %q:\n%s", ev, want, out)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "satelle session:") && !strings.HasPrefix(line, "Edits are allowed") {
				continue
			}
			if strings.Contains(line, "SATELLE_SESSION") || strings.Contains(line, "export SATELLE") {
				t.Errorf("hook %s line names the variable: %q", ev, line)
			}
		}
	}
}

// hookText is the text a hook printed: every string in its JSON output
// joined by newlines, or the output itself when it is not JSON.
func hookText(out string) string {
	var v any
	if json.Unmarshal([]byte(out), &v) != nil {
		return out
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			b.WriteString(x)
			b.WriteString("\n")
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}

// sto_2a41b9e4 AC5: -C runs the command in another directory.
func TestDashCRunsInTheDirectory(t *testing.T) {
	a := isolate(t)
	must(t, "init")
	aID := createID(t, "story", "create", "--title", "in-a")
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, b, "init", "-q")
	gittest.Git(t, b, "commit", "-q", "--allow-empty", "-m", "init")
	t.Chdir(b)
	must(t, "init")
	bID := createID(t, "story", "create", "--title", "in-b")
	t.Chdir(a)

	if out := must(t, "-C", b, "story", "list"); !strings.Contains(out, bID) || strings.Contains(out, aID) {
		t.Errorf("-C <b> story list from a:\n%s", out)
	}
	if wd, _ := os.Getwd(); wd != a {
		t.Errorf("the call left the directory at %s", wd)
	}
	t.Chdir(t.TempDir())
	must(t, "-C", a, "story", "set", aID, "--title", "x", "--session", "s")
	var evs []struct {
		Actor string `json:"actor"`
	}
	if err := json.Unmarshal([]byte(must(t, "-C", a, "ledger", "list", aID, "--json")), &evs); err != nil || len(evs) == 0 || evs[len(evs)-1].Actor != "s" {
		t.Errorf("-C <a> story set: events %+v (%v)", evs, err)
	}
	if err := os.WriteFile(filepath.Join(a, "rel.md"), []byte("r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "-C", a, "story", "attach", aID, "--name", "n", "--file", "rel.md", "--session", "s")
	missing := filepath.Join(a, "no-such-dir")
	if _, err := run(t, "", "-C", missing, "status"); err == nil || !strings.Contains(err.Error(), "-C "+missing) {
		t.Errorf("-C <missing>: %v", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("-C <missing> created the directory")
	}
}
