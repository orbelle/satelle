package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// nodeRunDir builds a node run directory whose runtime carries a node stamp
// naming the given process, with a substrate and tree of its own.
func nodeRunDir(t *testing.T, proc procgroup.Proc) (string, RuntimeStamp) {
	t.Helper()
	dir, err := CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(dir, "work")
	sub := filepath.Join(tree, DefaultDataDir)
	for _, d := range []string{sub, filepath.Join(dir, "runtime", "ledger")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime", "ledger", "sto_1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := RuntimeStamp{Kind: RuntimeNode, Node: "n1", Substrate: sub, Tree: tree, Digest: "d", Process: proc}
	if err := WriteRuntimeStamp(filepath.Join(dir, "runtime"), s); err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func parentProc(t *testing.T) procgroup.Proc {
	t.Helper()
	a := procgroup.Ancestors(os.Getpid())
	if len(a) == 0 {
		t.Skip("this platform reports no process ancestry")
	}
	return a[0]
}

func TestNodeRuntimeAcceptsAStampFromAnAncestor(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	dir, want := nodeRunDir(t, parentProc(t))
	got, runtime, err := NodeRuntime(dir)
	if err != nil {
		t.Fatalf("NodeRuntime: %v", err)
	}
	if got != want || runtime != filepath.Join(dir, "runtime") {
		t.Fatalf("NodeRuntime = %+v at %s, want %+v at %s/runtime", got, runtime, want, dir)
	}
}

func TestNodeRuntimeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(t *testing.T, dir string) string
	}{
		{"relative hint", "not absolute", func(t *testing.T, dir string) string { return "node" }},
		{"no runtime", "does not exist", func(t *testing.T, dir string) string { return t.TempDir() }},
		{"no stamp", "no readable stamp", func(t *testing.T, dir string) string {
			must(t, os.Remove(filepath.Join(dir, "runtime", RuntimeStampName)))
			return dir
		}},
		{"machine stamp", `says "machine"`, func(t *testing.T, dir string) string {
			must(t, WriteRuntimeStamp(filepath.Join(dir, "runtime"), RuntimeStamp{Kind: RuntimeMachine}))
			return dir
		}},
		{"repo.path", "repo.path", func(t *testing.T, dir string) string {
			must(t, os.WriteFile(filepath.Join(dir, "runtime", RepoPathMarkerName), []byte("/x\n"), 0o644))
			return dir
		}},
		{"missing digest", "no digest", func(t *testing.T, dir string) string {
			s, _ := ReadRuntimeStamp(filepath.Join(dir, "runtime"))
			s.Digest = ""
			must(t, WriteRuntimeStamp(filepath.Join(dir, "runtime"), s))
			return dir
		}},
		{"relative substrate", "substrate path is not an absolute path", func(t *testing.T, dir string) string {
			s, _ := ReadRuntimeStamp(filepath.Join(dir, "runtime"))
			s.Substrate = "work/.satelle"
			must(t, WriteRuntimeStamp(filepath.Join(dir, "runtime"), s))
			return dir
		}},
		{"non-canonical tree", "tree path is not canonical", func(t *testing.T, dir string) string {
			s, _ := ReadRuntimeStamp(filepath.Join(dir, "runtime"))
			s.Tree = dir + "/work/../work"
			must(t, WriteRuntimeStamp(filepath.Join(dir, "runtime"), s))
			return dir
		}},
		{"no process", "names no process", func(t *testing.T, dir string) string {
			s, _ := ReadRuntimeStamp(filepath.Join(dir, "runtime"))
			s.Process = procgroup.Proc{}
			must(t, WriteRuntimeStamp(filepath.Join(dir, "runtime"), s))
			return dir
		}},
		{"not an ancestor", "not started by the node run", func(t *testing.T, dir string) string {
			s, _ := ReadRuntimeStamp(filepath.Join(dir, "runtime"))
			s.Process = procgroup.Self()
			must(t, WriteRuntimeStamp(filepath.Join(dir, "runtime"), s))
			return dir
		}},
		{"symlinked runtime", "not a real directory", func(t *testing.T, dir string) string {
			other := t.TempDir()
			must(t, os.Symlink(filepath.Join(dir, "runtime"), filepath.Join(other, "runtime")))
			return other
		}},
		{"runtime under SATELLE_HOME", "under a machine home", func(t *testing.T, dir string) string {
			t.Setenv("SATELLE_HOME", dir)
			return dir
		}},
		{"runtime under ~/.satelle with a decoy SATELLE_HOME", "under a machine home", func(t *testing.T, dir string) string {
			home := t.TempDir()
			t.Setenv("HOME", home)
			run := filepath.Join(home, ".satelle", "x")
			must(t, os.MkdirAll(filepath.Dir(run), 0o755))
			must(t, os.Rename(dir, run))
			return run
		}},
		{"symlink in the runtime", "is not a plain file", func(t *testing.T, dir string) string {
			must(t, os.Symlink(t.TempDir(), filepath.Join(dir, "runtime", "logs")))
			return dir
		}},
		{"hard link in the runtime", "another hard link", func(t *testing.T, dir string) string {
			must(t, os.Link(filepath.Join(dir, "runtime", "ledger", "sto_1.jsonl"), filepath.Join(t.TempDir(), "elsewhere")))
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SATELLE_HOME", t.TempDir())
			dir, _ := nodeRunDir(t, parentProc(t))
			hint := tc.setup(t, dir)
			if _, _, err := NodeRuntime(hint); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NodeRuntime(%s) = %v, want a refusal naming %q", hint, err, tc.want)
			}
		})
	}
}

func TestMarkMachineRuntimeStampsOnlyKeyedRuntimes(t *testing.T) {
	keyed := filepath.Join(t.TempDir(), "repo-0123abcd")
	must(t, os.MkdirAll(keyed, 0o755))
	must(t, MarkMachineRuntime(keyed))
	if s, err := ReadRuntimeStamp(keyed); err != nil || s.Kind != RuntimeMachine {
		t.Fatalf("stamp = %+v, %v; want a machine stamp", s, err)
	}
	other := t.TempDir()
	must(t, MarkMachineRuntime(other))
	if _, err := os.Stat(filepath.Join(other, RuntimeStampName)); err == nil {
		t.Fatal("MarkMachineRuntime stamped a runtime not named like a machine runtime")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
