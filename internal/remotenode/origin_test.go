package remotenode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// TestMain gives the package a satelle home of its own: a node kernel records
// the machine id it reads from there in every event's origin.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "satelle-remotenode-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("SATELLE_HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func TestNodeKernelEventsCarryNodeOrigin(t *testing.T) {
	t.Setenv(config.WorkerEnv, "judge")
	base := t.TempDir()
	root := filepath.Join(base, "tree", ".satelle")
	tree := filepath.Join(base, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	k, err := openNodeKernel(root, tree, filepath.Join(base, "runtime"), nil, agent.Runner{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	kind, title := "story", "node work"
	it, err := k.Create(item.Fields{Kind: &kind, Title: &title}, "node")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := k.Ledger.Read(it.ID)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events %v %v", evs, err)
	}
	machine, ok := config.ReadMachineID(config.GlobalDir())
	if !ok {
		t.Fatal("no machine id recorded")
	}
	want := ledger.Origin{Machine: machine, Role: ledger.RoleNode, Harness: hook.Caller(), Worktree: canonicalTree(t, tree)}
	for _, e := range evs {
		var p struct{ Origin ledger.Origin }
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.Origin != want {
			t.Fatalf("%s origin %+v (%v), want %+v", e.Kind, p.Origin, err, want)
		}
	}
	if k.Machine != "" {
		t.Fatalf("node kernel machine %q: node seats would change", k.Machine)
	}
}

func canonicalTree(t *testing.T, tree string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(tree)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(r)
}
