//go:build unix

package remotenode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

func TestRunLocalMarksTheNodeRun(t *testing.T) {
	f := secretFixture(t, `printf '%s\n%s\n' "$SATELLE_NODE_RUN" "$SATELLE_NODE_WITHHOLD" > "$SATELLE_NODE_RUN/seen"`, nil, "")
	t.Setenv(config.NodeRunEnv, "before")
	t.Setenv(config.NodeWithholdEnv, "")
	os.Unsetenv(config.NodeWithholdEnv)
	nodeDir := f.stage(t, "nod_mark")
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
		Secrets: secretPayload(t, map[string]string{probeSecret: "v"})})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("RunLocal = %+v, %v", out, err)
	}
	dir, err := config.CanonicalPath(nodeDir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.ReadRuntimeStamp(filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.SubstrateDigest(filepath.Join(dir, "work", ".satelle"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != config.RuntimeNode || s.Node != "nod_mark" || s.Substrate != filepath.Join(dir, "work", ".satelle") ||
		s.Tree != filepath.Join(dir, "work") || s.Digest != digest || s.Process != procgroup.Self() {
		t.Fatalf("stamp = %+v, want a node stamp of this process with digest %s", s, digest)
	}
	seen, err := os.ReadFile(filepath.Join(dir, "seen"))
	if err != nil {
		t.Fatal(err)
	}
	if want := dir + "\n" + probeSecret + "\n"; string(seen) != want {
		t.Fatalf("the check saw %q, want %q", seen, want)
	}
	if v := os.Getenv(config.NodeRunEnv); v != "before" {
		t.Fatalf("%s after the run = %q, want it restored", config.NodeRunEnv, v)
	}
	if _, ok := os.LookupEnv(config.NodeWithholdEnv); ok {
		t.Fatalf("%s is still set after the run", config.NodeWithholdEnv)
	}
}

func TestRunLocalRefusesAStampChangedDuringTheRun(t *testing.T) {
	f := secretFixture(t, `printf '{"kind":"node"}\n' > "$SATELLE_NODE_RUN/runtime/runtime.json"`, nil, "")
	nodeDir := f.stage(t, "nod_forg")
	_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
		Secrets: secretPayload(t, map[string]string{probeSecret: "v"})})
	if err == nil || !strings.Contains(err.Error(), "changed during the run") {
		t.Fatalf("RunLocal = %v, want the changed stamp refused", err)
	}
	if _, err := os.Stat(filepath.Join(nodeDir, "out", localOutcome)); err == nil {
		t.Fatal("a run whose stamp changed wrote an outcome")
	}
}

func TestNodeRunVerifiesANamedSubstrate(t *testing.T) {
	t.Setenv(config.NodeRunEnv, "")
	t.Setenv(config.NodeWithholdEnv, "")
	runDir, tree, sub := t.TempDir(), t.TempDir(), t.TempDir()
	writeFiles(t, sub, map[string]string{"satelle.toml": ""})
	nr, err := MarkNodeRun(runDir, "n", sub, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer nr.Restore()
	if err := nr.Verify(); err != nil {
		t.Fatalf("Verify of an unchanged run = %v", err)
	}
	writeFiles(t, sub, map[string]string{"agents.local.toml": "# changed\n"})
	if err := nr.Verify(); err == nil || !strings.Contains(err.Error(), "changed during the run") {
		t.Fatalf("Verify after a named substrate changed = %v", err)
	}
}
