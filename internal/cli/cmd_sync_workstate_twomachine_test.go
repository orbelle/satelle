package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// twoMachineRoute is the fixture lifecycle: backlog → in_progress → done.
var twoMachineRoute = map[string]string{
	"done": "[meta]\nname = \"done\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"fixture\"\n\n" +
		"[\"*\"]\nobligations = [\"raised\", \"coded\", \"closed\"]\n",
	"step": "[meta]\nname = \"step\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"fixture\"\n\n" +
		"[raised]\nstatus = \"backlog\"\nstart = true\n\n" +
		"[coded]\nstatus = \"in_progress\"\nagent = \"executor\"\nrequires = [\"raised\"]\n\n" +
		"[closed]\nstatus = \"done\"\nterminal = true\nrequires = [\"coded\"]\n",
}

// machine is one checkout of the project: its own repo, database and app.
type machine struct {
	repo string
	db   *store.DB
	app  *app.App
}

func newMachine(t *testing.T, git bool) *machine {
	t.Helper()
	repo := t.TempDir()
	if git {
		for _, args := range [][]string{
			{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		} {
			c := exec.Command("git", args...)
			c.Dir = repo
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
			c := exec.Command("git", args...)
			c.Dir = repo
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(ledgerPushToml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rt := t.TempDir()
	db, err := store.Open(filepath.Join(rt, config.DefaultDBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &machine{repo: repo, db: db, app: &app.App{
		Config: cfg, RepoRoot: repo, RuntimeDir: rt,
		DBPath: filepath.Join(rt, config.DefaultDBName), Store: db,
	}}
}

// run invokes a work-state sync verb's RunE-level function against this machine.
func (m *machine) run(t *testing.T, fn func(*cobra.Command) error) {
	t.Helper()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.WithValue(context.Background(), appCtxKey{}, m.app))
	if err := fn(cmd); err != nil {
		t.Fatalf("sync verb: %v\n%s", err, out.String())
	}
}

func (m *machine) push(t *testing.T, server string) {
	t.Helper()
	m.run(t, func(c *cobra.Command) error { return runSyncWorkstatePush(c, server, false, false, false) })
}

func (m *machine) pull(t *testing.T, server string) {
	t.Helper()
	m.run(t, func(c *cobra.Command) error { return runSyncWorkstatePull(c, server, false, false, false) })
}

// pushInGateGater attaches a document and pushes this machine's work-state from
// inside the gate window of every edge into `done` — the window in which the
// transition's own rows are not yet written. Everything else passes at once.
type pushInGateGater struct {
	t      *testing.T
	m      *machine
	server string
}

func (g pushInGateGater) Gate(ctx context.Context, item workitem.Item, to string) (verb.GateDecision, error) {
	if to == "done" {
		if _, _, err := verb.AttachItemDoc(ctx, item, "mid-gate", "output", "attached while gated"); err != nil {
			return verb.GateDecision{}, err
		}
		time.Sleep(5 * time.Millisecond) // time-subject: timestamp separation, so the mid-gate attachment predates the push cursor
		g.m.push(g.t, g.server)
		time.Sleep(5 * time.Millisecond) // time-subject: timestamp separation, so the transition commit stamps after the push
	}
	return verb.GateDecision{Gated: false}, nil
}

func ledgerRowsOf(t *testing.T, m *machine, storyID, kind string) map[string]ledger.Entry {
	t.Helper()
	es, err := m.db.Ledger.ListByStory(context.Background(), storyID, kind)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ledger.Entry{}
	for _, e := range es {
		out[e.ID] = e
	}
	return out
}

func entryIDs(m map[string]ledger.Entry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
