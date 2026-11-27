package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode"
)

func TestNodeRunLocalRefusesProgressWithSecrets(t *testing.T) {
	data := t.TempDir()
	def := "version = 1\nname = \"p\"\nprovider = \"docker-local\"\nsatelle_version = \"1.0.0\"\n\n" +
		"[secrets]\nnames = [\"SATELLE_PROBE_SECRET\"]\n\n[drive]\nkind = \"item\"\n\n[lifecycle]\nscope = \"run\"\n"
	dir := filepath.Join(data, remotenode.RemotesDir, "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, remotenode.DefinitionFile), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	_, bundle, err := remotenode.LoadLocal(data, "p")
	if err != nil {
		t.Fatal(err)
	}
	node := t.TempDir()
	if err := os.MkdirAll(filepath.Join(node, "in"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(node, "in", "definition.bundle"), bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := json.Marshal(remotenode.LocalRun{NodeID: "nod_p",
		Run: remotenode.RunDescriptor{Mode: remotenode.ModeItem, ItemID: "sto_p"}, Layout: remotenode.NodeLayout,
		SubstrateDigest: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(node, "in", "run.json"), run, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := nodeRunLocalCmd()
	cmd.SetArgs([]string{"--dir", node, "--progress"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err = cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SATELLE_PROBE_SECRET") || !strings.Contains(err.Error(), "--progress") {
		t.Fatalf("run-local --progress with [secrets] = %v", err)
	}
	if _, err := os.Stat(filepath.Join(node, "out", "progress")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused node wrote progress: %v", err)
	}
}
