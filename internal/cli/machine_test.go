package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/web"
)

const testToken = "tok-ada"

// machine is one local satelle: its own home and repo, and, from
// newMachineWithUI, its own satelled (ui, its URL, over the mirror store ms).
type machine struct {
	home, dir string
	ui        string
	ms        *mirror.Store
}

// newMachine isolates a fresh home and repo, initialises it and signs it in
// with the session token the fake accepts.
func newMachine(t *testing.T) machine {
	t.Helper()
	dir := isolate(t)
	t.Setenv("SATELLE_TOKEN", testToken)
	must(t, "init")
	return machine{home: os.Getenv("HOME"), dir: dir}
}

// newMachineWithUI is newMachine with its own satelled: a mirror store and
// the web server over it, which only this machine's CLI pushes to.
func newMachineWithUI(t *testing.T) machine {
	t.Helper()
	return serveMirror(t, newMachine(t), filepath.Join(t.TempDir(), "m.db"))
}

// newMachineWithHomeUI is newMachineWithUI whose satelled serves this home's
// own mirror database (mirror.DefaultPath), the file the CLI's partition
// readers open, so m.ms and the CLI read the same partitions.
func newMachineWithHomeUI(t *testing.T) machine {
	t.Helper()
	m := newMachine(t)
	path := mirror.DefaultPath(config.GlobalDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	return serveMirror(t, m, path)
}

// serveMirror gives m a satelled: a mirror store at path and the web server
// over it, which only this machine's CLI pushes to.
func serveMirror(t *testing.T, m machine, path string) machine {
	t.Helper()
	ms, err := mirror.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	// It serves this machine's home: another machine's CLI is refused.
	srv := httptest.NewServer(web.NewMirrorWithInstance(ms, config.CurrentInstanceID()).Handler)
	t.Cleanup(srv.Close)
	m.ui, m.ms = srv.URL, ms
	return m
}

// use makes m the current machine: its home, its repo, and its satelled (none
// when it has none), so its CLI pushes only to its own.
func (m machine) use(t *testing.T) {
	t.Helper()
	ep := m.ui
	if ep == "" {
		ep = "none"
	}
	t.Setenv("SATELLE_SERVER_ENDPOINT", ep)
	t.Setenv("HOME", m.home)
	t.Setenv("SATELLE_HOME", filepath.Join(m.home, ".satelle"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(m.home, "xdg"))
	t.Chdir(m.dir)
}

func runtimeLedger(t *testing.T) *ledger.Store {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(r.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// runOn runs one command line on m and then pushes its writes to m's
// satelled, as Execute does, so nothing is left for the next machine.
func runOn(t *testing.T, m machine, args ...string) string {
	t.Helper()
	m.use(t)
	out := must(t, args...)
	flushUI(context.Background())
	return out
}

// storiesOn is the stories fragment of m's satelled for its only partition.
func storiesOn(t *testing.T, m machine) string {
	t.Helper()
	parts, err := m.ms.ListPartitions(context.Background())
	if err != nil || len(parts) != 1 {
		t.Fatalf("satelled partitions = %d, %v; want 1", len(parts), err)
	}
	resp, err := http.Get(m.ui + "/r/" + parts[0].Slug + "/fragment/stories")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
