//go:build !windows

package remotenode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
)

// The wrapper runs the check in <root>/work with the transition in the
// environment (quoted, so a value with a quote survives) and as JSON on stdin,
// writes its output to out/log and its exit code to out/exit; a script with a
// #! runs as a file, any other through sh.
func TestGateWrapperRunsTheCheck(t *testing.T) {
	env := gate.CheckEnv{Item: "sto_1", From: "a", To: "b's", Base: "abc"}
	for _, c := range []struct {
		name, script string
		shebang      bool
		exit, log    string
	}{
		{"sh", "echo \"$SATELLE_ITEM $SATELLE_FROM $SATELLE_TO $SATELLE_BASE $(basename \"$PWD\")\"; cat; exit 3", false, "3",
			`sto_1 a b's abc work` + "\n" + `{"base":"abc","from":"a","id":"sto_1","to":"b's"}`},
		{"shebang", "#!/bin/sh\necho \"$0\" >&2\nexit 0\n", true, "0", "in/check"},
	} {
		root := t.TempDir()
		for _, d := range []string{"in", "work"} {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range map[string]string{
			gateScript: c.script, gatePayload: string(env.Payload()), gateWrapper: gateWrapperScript(env, c.shebang),
		} {
			if err := os.WriteFile(filepath.Join(root, "in", name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("sh", filepath.Join(root, "in", gateWrapper), root).CombinedOutput(); err != nil {
			t.Fatalf("%s: wrapper: %v %s", c.name, err, out)
		}
		exit, _ := os.ReadFile(filepath.Join(root, "out", gateExit))
		log, _ := os.ReadFile(filepath.Join(root, "out", gateLog))
		if strings.TrimSpace(string(exit)) != c.exit || !strings.Contains(string(log), c.log) {
			t.Fatalf("%s: exit %q log %q", c.name, exit, log)
		}
		if strings.Contains(gateWrapperScript(env, c.shebang), LocalNodeDir) {
			t.Fatalf("%s: the wrapper names the node dir", c.name)
		}
	}
}

func TestParseExit(t *testing.T) {
	for in, want := range map[string]int{"0\n": 0, " 3 ": 3, "137": 137} {
		if got, err := parseExit([]byte(in)); err != nil || got != want {
			t.Errorf("parseExit(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "\n", "x", "-1", "1 2"} {
		if _, err := parseExit([]byte(in)); err == nil {
			t.Errorf("parseExit(%q) accepted", in)
		}
	}
}
