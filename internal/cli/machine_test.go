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

type machine struct {
	home, dir string
	ui        string
	ms        *mirror.Store
}

func newMachine(t *testing.T) machine {
	t.Helper()
	dir := isolate(t)
	t.Setenv("SATELLE_TOKEN", testToken)
	must(t, "init")
	return machine{home: os.Getenv("HOME"), dir: dir}
}

func newMachineWithUI(t *testing.T) machine {
	t.Helper()
	return serveMirror(t, newMachine(t), filepath.Join(t.TempDir(), "m.db"))
}

func newMachineWithHomeUI(t *testing.T) machine {
	t.Helper()
	m := newMachine(t)
	path := mirror.DefaultPath(config.GlobalDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	return serveMirror(t, m, path)
}

func serveMirror(t *testing.T, m machine, path string) machine {
	t.Helper()
	ms, err := mirror.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	srv := httptest.NewServer(web.NewMirrorWithInstance(ms, config.CurrentInstanceID()).Handler)
	t.Cleanup(srv.Close)
	m.ui, m.ms = srv.URL, ms
	return m
}

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

func runOn(t *testing.T, m machine, args ...string) string {
	t.Helper()
	m.use(t)
	out := must(t, args...)
	flushUI(context.Background())
	return out
}

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
