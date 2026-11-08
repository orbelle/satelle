package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

const testToken = "tok-ada"

// machine is one local satelle: its own home and repo.
type machine struct {
	home, dir string
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

// use makes m the current machine.
func (m machine) use(t *testing.T) {
	t.Helper()
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
