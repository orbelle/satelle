package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
)

func rewriteRun(t *testing.T, nodeDir string, edit func(map[string]any)) {
	t.Helper()
	p := filepath.Join(nodeDir, "in", localRunFile)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	if b, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunLocalRefusesAnotherLayout(t *testing.T) {
	if NodeLayout != 3 {
		t.Fatalf("NodeLayout = %d; this test names the layouts either side of 3", NodeLayout)
	}
	for _, layout := range []int{2, 4} {
		t.Run(fmt.Sprintf("layout %d", layout), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "marker")
			f := secretFixture(t, "echo checked", []string{"touch " + marker}, "")
			nodeDir := f.stage(t, "nod_1a70")
			rewriteRun(t, nodeDir, func(m map[string]any) { m["layout"] = layout })
			_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
				Secrets: secretPayload(t, map[string]string{probeSecret: "the-value"})})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("laid out for node layout %d", layout)) ||
				!strings.Contains(err.Error(), fmt.Sprintf("this node implements node layout %d", NodeLayout)) {
				t.Fatalf("RunLocal on layout %d = %v", layout, err)
			}
			for _, d := range []string{"substrate", "work", "runtime", "out"} {
				if _, err := os.Stat(filepath.Join(nodeDir, d)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("a refused run made %s: %v", d, err)
				}
			}
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused run ran its setup: %v", err)
			}
		})
	}
}

func TestRunLocalRefusesNoLayout(t *testing.T) {
	for _, c := range []struct {
		name   string
		nodeID string
		edit   func(map[string]any)
	}{
		{"layout missing", "nod_1a71", func(m map[string]any) { delete(m, "layout") }},
		{"layout 0 is no layout", "nod_1a73", func(m map[string]any) { m["layout"] = 0 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "marker")
			f := secretFixture(t, "echo checked", []string{"touch " + marker}, "")
			nodeDir := f.stage(t, c.nodeID)
			rewriteRun(t, nodeDir, c.edit)
			_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
				Secrets: secretPayload(t, map[string]string{probeSecret: "the-value"})})
			if err == nil || !strings.Contains(err.Error(), "names no node layout") ||
				!strings.Contains(err.Error(), fmt.Sprintf("this node implements node layout %d", NodeLayout)) {
				t.Fatalf("RunLocal with no layout = %v", err)
			}
			for _, d := range []string{"substrate", "work", "runtime", "out"} {
				if _, err := os.Stat(filepath.Join(nodeDir, d)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("a refused run made %s: %v", d, err)
				}
			}
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused run ran its setup: %v", err)
			}
		})
	}
	t.Run("zero is never read as a layout", func(t *testing.T) {
		err := checkLayout(0, NodeLayout)
		if err == nil || !strings.Contains(err.Error(), "names no node layout") ||
			strings.Contains(err.Error(), "laid out for node layout") {
			t.Fatalf("checkLayout(0, %d) = %v", NodeLayout, err)
		}
	})
}

func TestStageInNamesLayout(t *testing.T) {
	tree, k, it := localFixture(t, "1.0.0")
	nodeDir := stageNode(t, tree, k, it.ID, "nod_1a72", 1)
	var run LocalRun
	b, err := os.ReadFile(filepath.Join(nodeDir, "in", localRunFile))
	if err != nil || json.Unmarshal(b, &run) != nil || run.Layout != NodeLayout {
		t.Fatalf("staged run.json = %s, %v", b, err)
	}
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != kernel.VerdictAccept || !strings.Contains(out.Notes, "moved a → b") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunLocalRefusesALayoutOneRunBeforeSetup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	f := secretFixture(t, "echo checked", []string{"touch " + marker}, "")
	nodeDir := f.stage(t, "nod_1a71")
	rewriteRun(t, nodeDir, func(m map[string]any) { m["layout"] = 1 })
	_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
		Secrets: secretPayload(t, map[string]string{probeSecret: "the-value"})})
	if err == nil || !strings.Contains(err.Error(), "is laid out for node layout 1; this node implements node layout 3; refused before setup") {
		t.Fatalf("RunLocal on a layout-1 run = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused run ran its setup: %v", err)
	}
}
